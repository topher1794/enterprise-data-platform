package lineage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/edp/edp-control-plane/internal/platform"
)

// Auditor records state changes.
type Auditor interface {
	Record(ctx context.Context, entry AuditEntry) error
}

// AuditEntry describes a state change to record.
type AuditEntry struct {
	Action     string
	EntityType string
	EntityID   uuid.UUID
	EntityName string
	Changes    map[string]any
}

// AuditEntityEdge is the audit entity type for lineage edges.
const AuditEntityEdge = "lineage_edge"

// Service holds the lineage business rules.
type Service struct {
	repo    Repository
	auditor Auditor
	log     *slog.Logger
}

// ServiceOption customises a Service.
type ServiceOption func(*Service)

// WithAuditor attaches an audit sink.
func WithAuditor(a Auditor) ServiceOption {
	return func(s *Service) { s.auditor = a }
}

// WithLogger overrides the logger.
func WithLogger(l *slog.Logger) ServiceOption {
	return func(s *Service) {
		if l != nil {
			s.log = l
		}
	}
}

// NewService builds a lineage service.
func NewService(repo Repository, opts ...ServiceOption) (*Service, error) {
	if repo == nil {
		return nil, errors.New("lineage service: repository is required")
	}

	s := &Service{repo: repo, log: slog.Default()}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// RecordEdge registers or refreshes a lineage edge.
func (s *Service) RecordEdge(ctx context.Context, req CreateEdgeRequest) (*Edge, error) {
	if err := validateCreateEdge(req); err != nil {
		return nil, err
	}

	from := Endpoint{
		EntityType: normaliseEntityType(string(req.From.EntityType)),
		EntityID:   req.From.EntityID,
		Column:     strings.TrimSpace(req.From.Column),
	}
	to := Endpoint{
		EntityType: normaliseEntityType(string(req.To.EntityType)),
		EntityID:   req.To.EntityID,
		Column:     strings.TrimSpace(req.To.Column),
	}

	edge := &Edge{
		From:                from,
		To:                  to,
		TransformType:       normaliseTransformType(string(req.TransformType)),
		TransformExpression: strings.TrimSpace(req.TransformExpression),
		Confidence:          1.0,
		ObservedAt:          time.Now().UTC(),
		ObservedByRunID:     req.ObservedByRunID,
		Metadata:            req.Metadata,
	}
	if req.Confidence != nil {
		edge.Confidence = *req.Confidence
	}
	if edge.Metadata == nil {
		edge.Metadata = map[string]any{}
	}

	if err := s.repo.RecordEdge(ctx, edge); err != nil {
		return nil, platform.AsError(err)
	}

	// Recording lineage is a machine-driven background activity, so it is not
	// audited: a nightly parse would otherwise write tens of thousands of audit
	// rows describing its own work. Only hand-curated changes are audited.
	return edge, nil
}

// RecordEdges registers many edges, used by the lineage parser after it walks a
// set of queries.
func (s *Service) RecordEdges(ctx context.Context, reqs []CreateEdgeRequest) ([]Edge, error) {
	if len(reqs) == 0 {
		return []Edge{}, nil
	}

	out := make([]Edge, 0, len(reqs))
	for _, req := range reqs {
		edge, err := s.RecordEdge(ctx, req)
		if err != nil {
			return nil, err
		}
		out = append(out, *edge)
	}
	return out, nil
}

// GetEdge returns a single edge.
func (s *Service) GetEdge(ctx context.Context, id uuid.UUID) (*Edge, error) {
	edge, err := s.repo.GetEdgeByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, platform.NewNotFound("lineage edge", id)
		}
		return nil, platform.AsError(err)
	}
	return edge, nil
}

// ListEdges returns a page of edges.
func (s *Service) ListEdges(ctx context.Context, q ListEdgesQuery) (platform.PageResult[*Edge], error) {
	page, err := s.repo.ListEdges(ctx, q)
	if err != nil {
		return platform.PageResult[*Edge]{}, platform.AsError(err)
	}
	return page, nil
}

// DeleteEdge removes a single hand-curated edge and records why.
func (s *Service) DeleteEdge(ctx context.Context, id uuid.UUID, actor platform.Actor) error {
	edge, err := s.GetEdge(ctx, id)
	if err != nil {
		return err
	}

	if err := s.repo.DeleteEdge(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return platform.NewNotFound("lineage edge", id)
		}
		return platform.AsError(err)
	}

	s.record(ctx, AuditEntry{
		Action:     "deleted",
		EntityType: AuditEntityEdge,
		EntityID:   id,
		Changes:    map[string]any{"before": edgeSnapshot(edge)},
	})

	return nil
}

// Impact walks the lineage graph and reports what an entity affects.
//
// The walk is breadth-first with an explicit visited set, which matters because
// lineage is cyclic in practice: a dashboard derived from a mart feeds a
// quality check that feeds a mart, and a naive recursion would loop forever.
func (s *Service) Impact(ctx context.Context, q ImpactQuery) (*Graph, error) {
	if q.Root.IsZero() {
		return nil, platform.NewBadRequest("a root entity type and entity_id are required")
	}
	if !q.Root.EntityType.Valid() {
		return nil, unsupportedEnum("entity_type", q.Root.EntityType, listEntityTypes()...)
	}
	if q.Direction == "" {
		q.Direction = Downstream
	}
	if !q.Direction.Valid() {
		return nil, unsupportedEnum("direction", q.Direction, "upstream", "downstream", "both")
	}

	depth := q.Depth
	if depth <= 0 {
		depth = DefaultDepth
	}
	if depth > MaxDepth {
		depth = MaxDepth
	}

	maxNodes := q.MaxNodes
	if maxNodes <= 0 {
		maxNodes = DefaultMaxNodes
	}
	if maxNodes > MaxNodesLimit {
		maxNodes = MaxNodesLimit
	}

	root := Endpoint{
		EntityType: normaliseEntityType(string(q.Root.EntityType)),
		EntityID:   q.Root.EntityID,
		Column:     strings.TrimSpace(q.Root.Column),
	}

	graph := &Graph{
		Root:  root,
		Nodes: []Node{{Endpoint: root, Depth: 0}},
		Edges: []Edge{},
	}

	// visited is keyed by entity identity. Column is excluded deliberately: a
	// column-level traversal reports the containing entities as nodes, and the
	// edges carry the column detail.
	visited := map[string]bool{entityKey(root): true}
	edgeSeen := map[uuid.UUID]bool{}

	frontier := []Node{{Endpoint: root, Depth: 0}}
	truncated := false

	for level := 0; level < depth && len(frontier) > 0; level++ {
		var next []Node

		for _, node := range frontier {
			edges, err := s.repo.Neighbours(ctx, node.Endpoint, q.Direction, q.IncludeColumnLevel)
			if err != nil {
				return nil, platform.AsError(err)
			}

			for _, edge := range edges {
				if !edgeSeen[edge.ID] {
					edgeSeen[edge.ID] = true
					graph.Edges = append(graph.Edges, edge)
				}

				neighbour := otherEndpoint(edge, node.Endpoint, q.Direction)
				if neighbour.IsZero() {
					continue
				}

				key := entityKey(neighbour)
				if visited[key] {
					// Already reached, possibly by a shorter path. Since the
					// walk is breadth-first, the first visit is the shortest.
					continue
				}

				if len(graph.Nodes) >= maxNodes {
					truncated = true
					continue
				}

				visited[key] = true
				child := Node{
					Endpoint:      neighbour,
					Depth:         node.Depth + 1,
					TransformType: edge.TransformType,
					Path:          append(append([]uuid.UUID{}, node.Path...), edge.ID),
				}
				graph.Nodes = append(graph.Nodes, child)
				next = append(next, child)
			}
		}

		frontier = next
	}

	graph.Truncated = truncated
	if truncated {
		s.log.WarnContext(ctx, "lineage traversal hit the node limit",
			"root", edgeKey(root), "max_nodes", maxNodes, "direction", q.Direction)
	}

	return graph, nil
}

// ImpactSummary is the condensed answer to "what breaks if this changes", which
// is what a UI wants and what a change-management gate reads.
type ImpactSummary struct {
	Root Endpoint `json:"root"`

	DirectConsumers   int `json:"direct_consumers"`
	DirectProducers   int `json:"direct_producers"`
	AffectedDatasets  int `json:"affected_datasets"`
	AffectedPipelines int `json:"affected_pipelines"`
	TotalAffected     int `json:"total_affected"`

	// Classified is false when any affected entity is classified above
	// "internal", so a caller can require sign-off before proceeding.
	RequiresReview bool `json:"requires_review"`

	Truncated bool `json:"truncated"`
}

// SummarizeImpact condenses a traversal into counts and a review flag.
func (s *Service) SummarizeImpact(ctx context.Context, q ImpactQuery) (*ImpactSummary, error) {
	graph, err := s.Impact(ctx, q)
	if err != nil {
		return nil, err
	}

	summary := &ImpactSummary{
		Root:              graph.Root,
		DirectConsumers:   0,
		DirectProducers:   0,
		AffectedDatasets:  0,
		AffectedPipelines: 0,
		TotalAffected:     0,
		Truncated:         graph.Truncated,
	}

	for _, node := range graph.Nodes {
		if node.Depth == 0 {
			continue
		}
		summary.TotalAffected++

		switch node.Endpoint.EntityType {
		case EntityDataset:
			summary.AffectedDatasets++
		case EntityPipeline, EntityTask:
			summary.AffectedPipelines++
		}

		if node.Depth == 1 {
			if q.Direction == Upstream {
				summary.DirectProducers++
			} else {
				summary.DirectConsumers++
			}
		}
	}

	// A change that reaches any pipeline or more than a handful of datasets
	// needs a human. This is a deliberate heuristic, not a rule derived from the
	// graph: the alternative is a threshold nobody agreed on.
	if summary.AffectedPipelines > 0 || summary.TotalAffected > 5 {
		summary.RequiresReview = true
	}

	return summary, nil
}

// Record is a bulk entry point used by the lineage parser, which discovers many
// edges from one query and wants them attributed to the same run.
func (s *Service) RecordForRun(ctx context.Context, runID uuid.UUID, reqs []CreateEdgeRequest) ([]Edge, error) {
	out := make([]Edge, 0, len(reqs))
	for _, req := range reqs {
		if req.ObservedByRunID == nil {
			req.ObservedByRunID = &runID
		}
		edge, err := s.RecordEdge(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("record lineage edge for run %s: %w", runID, err)
		}
		out = append(out, *edge)
	}
	return out, nil
}

// otherEndpoint returns the end of the edge that is not the node we came from.
func otherEndpoint(edge Edge, from Endpoint, direction Direction) Endpoint {
	if direction == Upstream {
		return Endpoint{
			EntityType: edge.From.EntityType,
			EntityID:   edge.From.EntityID,
			Column:     edge.From.Column,
		}
	}
	if direction == Downstream {
		return Endpoint{
			EntityType: edge.To.EntityType,
			EntityID:   edge.To.EntityID,
			Column:     edge.To.Column,
		}
	}

	// Both: whichever end is not where we started.
	if entityKey(edge.From) != entityKey(from) {
		return Endpoint{
			EntityType: edge.From.EntityType,
			EntityID:   edge.From.EntityID,
			Column:     edge.From.Column,
		}
	}
	return Endpoint{
		EntityType: edge.To.EntityType,
		EntityID:   edge.To.EntityID,
		Column:     edge.To.Column,
	}
}

// entityKey identifies an entity without its column, for visited-set purposes.
func entityKey(e Endpoint) string {
	return string(e.EntityType) + ":" + e.EntityID.String()
}

// record writes an audit entry, tolerating its absence.
func (s *Service) record(ctx context.Context, entry AuditEntry) {
	if s.auditor == nil {
		return
	}
	if err := s.auditor.Record(ctx, entry); err != nil {
		s.log.ErrorContext(ctx, "failed to record audit event",
			"action", entry.Action, "entity_type", entry.EntityType,
			"entity_id", entry.EntityID, "error", err)
	}
}
