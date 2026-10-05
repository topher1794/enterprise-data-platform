package lineage

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// LineageService is the business contract the handler depends on.
type LineageService interface {
	RecordEdge(ctx context.Context, req CreateEdgeRequest) (*Edge, error)
	GetEdge(ctx context.Context, id uuid.UUID) (*Edge, error)
	ListEdges(ctx context.Context, q ListEdgesQuery) (platform.PageResult[*Edge], error)
	DeleteEdge(ctx context.Context, id uuid.UUID, actor platform.Actor) error

	Impact(ctx context.Context, q ImpactQuery) (*Graph, error)
	SummarizeImpact(ctx context.Context, q ImpactQuery) (*ImpactSummary, error)
}

// Handler exposes the lineage domain over HTTP.
type Handler struct {
	svc LineageService
}

// NewHandler builds a lineage handler.
func NewHandler(svc LineageService) *Handler {
	return &Handler{svc: svc}
}

// Mount registers the lineage routes on r.
//
//	GET    /lineage/edges                      list
//	POST   /lineage/edges                      record or refresh an edge
//	GET    /lineage/edges/{edgeID}             read
//	DELETE /lineage/edges/{edgeID}             remove
//	GET    /lineage/graph                       traverse the graph
//	GET    /lineage/impact                     condensed impact summary
func (h *Handler) Mount(r chi.Router) {
	r.Get("/lineage/graph", h.Graph)
	r.Get("/lineage/impact", h.Impact)

	r.Route("/lineage/edges", func(r chi.Router) {
		r.Get("/", h.ListEdges)
		r.Post("/", h.CreateEdge)

		r.Route("/{edgeID}", func(r chi.Router) {
			r.Get("/", h.GetEdge)
			r.Delete("/", h.DeleteEdge)
		})
	})
}

// ListEdges handles GET /lineage/edges.
func (h *Handler) ListEdges(w http.ResponseWriter, r *http.Request) {
	page, err := platform.ParsePage(r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	sort, err := platform.ParseSort(r, SortableColumns(), DefaultSort())
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	query := r.URL.Query()

	columnOnly := false
	if raw := strings.TrimSpace(query.Get("column_level_only")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			platform.Fail(w, r, platform.NewBadRequest("column_level_only must be true or false"))
			return
		}
		columnOnly = parsed
	}

	result, err := h.svc.ListEdges(r.Context(), ListEdgesQuery{
		Page:            page,
		Sort:            sort,
		FromEntityType:  strings.TrimSpace(query.Get("from_entity_type")),
		FromEntityID:    strings.TrimSpace(query.Get("from_entity_id")),
		FromColumn:      strings.TrimSpace(query.Get("from_column")),
		ToEntityType:    strings.TrimSpace(query.Get("to_entity_type")),
		ToEntityID:      strings.TrimSpace(query.Get("to_entity_id")),
		ToColumn:        strings.TrimSpace(query.Get("to_column")),
		TransformType:   strings.TrimSpace(query.Get("transform_type")),
		ColumnLevelOnly: columnOnly,
	})
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, result)
}

// CreateEdge handles POST /lineage/edges.
func (h *Handler) CreateEdge(w http.ResponseWriter, r *http.Request) {
	req, err := platform.FromRequest[CreateEdgeRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	created, err := h.svc.RecordEdge(r.Context(), req)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/"+created.ID.String())
	platform.JSON(w, http.StatusCreated, created)
}

// GetEdge handles GET /lineage/edges/{edgeID}.
func (h *Handler) GetEdge(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "edgeID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	found, err := h.svc.GetEdge(r.Context(), id)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, found)
}

// DeleteEdge handles DELETE /lineage/edges/{edgeID}.
func (h *Handler) DeleteEdge(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "edgeID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	if err := h.svc.DeleteEdge(r.Context(), id, actorOrAnonymous(r)); err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.NoContent(w)
}

// Graph handles GET /lineage/graph.
func (h *Handler) Graph(w http.ResponseWriter, r *http.Request) {
	query, err := impactQuery(r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	graph, err := h.svc.Impact(r.Context(), *query)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, graph)
}

// Impact handles GET /lineage/impact.
func (h *Handler) Impact(w http.ResponseWriter, r *http.Request) {
	query, err := impactQuery(r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	summary, err := h.svc.SummarizeImpact(r.Context(), *query)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, summary)
}

// impactQuery parses the traversal parameters shared by /graph and /impact.
func impactQuery(r *http.Request) (*ImpactQuery, error) {
	query := r.URL.Query()

	root := Endpoint{
		EntityType: normaliseEntityType(query.Get("entity_type")),
		Column:     strings.TrimSpace(query.Get("column")),
	}

	rootID := strings.TrimSpace(query.Get("entity_id"))
	if rootID == "" {
		return nil, platform.NewBadRequest(
			"entity_id is required, together with entity_type")
	}
	id, err := uuid.Parse(rootID)
	if err != nil {
		return nil, platform.NewBadRequest("entity_id must be a valid UUID")
	}
	root.EntityID = id

	if !root.EntityType.Valid() {
		return nil, unsupportedEnum("entity_type", root.EntityType, listEntityTypes()...)
	}

	depth := 0
	if raw := strings.TrimSpace(query.Get("depth")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return nil, platform.NewBadRequest(
				"depth must be a positive whole number")
		}
		depth = parsed
	}

	maxNodes := 0
	if raw := strings.TrimSpace(query.Get("max_nodes")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return nil, platform.NewBadRequest("max_nodes must be a positive whole number")
		}
		maxNodes = parsed
	}

	includeColumns := false
	if raw := strings.TrimSpace(query.Get("include_column_level")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, platform.NewBadRequest("include_column_level must be true or false")
		}
		includeColumns = parsed
	}

	return &ImpactQuery{
		Root:               root,
		Direction:          normaliseDirection(query.Get("direction")),
		Depth:              depth,
		IncludeColumnLevel: includeColumns,
		MaxNodes:           maxNodes,
	}, nil
}

// pathUUID extracts and validates a UUID path parameter.
func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, platform.NewBadRequest("%s must be a valid UUID", name)
	}
	return id, nil
}

// actorOrAnonymous returns the authenticated principal, or a synthetic one when
// authentication is disabled.
func actorOrAnonymous(r *http.Request) platform.Actor {
	if actor, ok := platform.ActorFrom(r.Context()); ok {
		return actor
	}
	return platform.Actor{ID: "anonymous", Roles: []string{}, Scopes: []string{}}
}
