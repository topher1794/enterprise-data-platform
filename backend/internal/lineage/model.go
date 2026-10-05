// Package lineage records and traverses the provenance graph connecting
// sources, datasets, pipelines and tasks.
package lineage

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// EntityType is a kind of node in the lineage graph.
type EntityType string

// Supported entity types.
const (
	EntitySource   EntityType = "source"
	EntityDataset  EntityType = "dataset"
	EntityPipeline EntityType = "pipeline"
	EntityTask     EntityType = "task"
)

var entityTypes = map[EntityType]bool{
	EntitySource: true, EntityDataset: true,
	EntityPipeline: true, EntityTask: true,
}

// Valid reports whether t is a supported entity type.
func (t EntityType) Valid() bool { return entityTypes[t] }

// TransformType describes how data was changed between two endpoints.
type TransformType string

// Supported transform types.
const (
	TransformCopy      TransformType = "copy"
	TransformAggregate TransformType = "aggregate"
	TransformJoin      TransformType = "join"
	TransformFilter    TransformType = "filter"
	TransformDerive    TransformType = "derive"
	TransformUnion     TransformType = "union"
	TransformEnrich    TransformType = "enrich"
	TransformML        TransformType = "ml_transform"
)

var transformTypes = map[TransformType]bool{
	TransformCopy: true, TransformAggregate: true, TransformJoin: true,
	TransformFilter: true, TransformDerive: true, TransformUnion: true,
	TransformEnrich: true, TransformML: true,
}

// Valid reports whether t is a supported transform type.
func (t TransformType) Valid() bool { return transformTypes[t] }

// Endpoint is one end of a lineage edge.
//
// Storing both endpoints as typed pairs, rather than as a self-referencing
// table, is what allows an edge to join any two entity types.
type Endpoint struct {
	EntityType EntityType `json:"entity_type"`
	EntityID   uuid.UUID  `json:"entity_id"`
	// Column is empty for table-level lineage, set for column-level lineage.
	Column string `json:"column,omitempty"`
}

// IsZero reports whether the endpoint is unset.
func (e Endpoint) IsZero() bool {
	return e.EntityType == "" || e.EntityID == uuid.Nil
}

// Edge is a directed provenance relationship: From produced To.
type Edge struct {
	ID uuid.UUID `json:"id"`

	From Endpoint `json:"from"`
	To   Endpoint `json:"to"`

	TransformType       TransformType `json:"transform_type"`
	TransformExpression string        `json:"transform_expression"`

	// Confidence distinguishes lineage inferred by query parsing from lineage
	// an owner declared by hand. 1.0 means certain.
	Confidence float64 `json:"confidence"`

	ObservedAt      time.Time  `json:"observed_at"`
	ObservedByRunID *uuid.UUID `json:"observed_by_run_id,omitempty"`

	Metadata map[string]any `json:"metadata"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateEdgeRequest is the input to RecordEdge.
type CreateEdgeRequest struct {
	From Endpoint `json:"from" validate:"required"`
	To   Endpoint `json:"to" validate:"required"`

	TransformType       TransformType `json:"transform_type" validate:"required"`
	TransformExpression string        `json:"transform_expression" validate:"max=4000"`

	// Confidence defaults to 1.0 for a hand-declared edge.
	Confidence *float64 `json:"confidence" validate:"omitempty,gte=0,lte=1"`

	ObservedByRunID *uuid.UUID     `json:"observed_by_run_id"`
	Metadata        map[string]any `json:"metadata"`
}

// ListEdgesQuery filters the edge list.
type ListEdgesQuery struct {
	platform.Page
	platform.Sort

	// FromEntityType and FromEntityID bound the upstream endpoint.
	FromEntityType string `json:"from_entity_type"`
	FromEntityID   string `json:"from_entity_id"`
	FromColumn     string `json:"from_column"`

	// ToEntityType and ToEntityID bound the downstream endpoint.
	ToEntityType string `json:"to_entity_type"`
	ToEntityID   string `json:"to_entity_id"`
	ToColumn     string `json:"to_column"`

	TransformType string `json:"transform_type"`
	// ColumnLevelOnly restricts results to edges that name a column.
	ColumnLevelOnly bool `json:"column_level_only"`
}

// SortableColumns maps API sort keys onto real columns.
func SortableColumns() map[string]string {
	return map[string]string{
		"observed_at": "observed_at",
		"created_at":  "created_at",
		"confidence":  "confidence",
	}
}

// DefaultSort orders newest first.
func DefaultSort() platform.Sort {
	return platform.Sort{By: "observed_at", Order: "desc"}
}

// Direction selects which way to walk the graph.
type Direction string

// Traversal directions.
const (
	// Upstream walks towards producers.
	Upstream Direction = "upstream"
	// Downstream walks towards consumers.
	Downstream Direction = "downstream"
	// Both walks in either direction.
	Both Direction = "both"
)

// Valid reports whether d is a supported direction.
func (d Direction) Valid() bool {
	return d == Upstream || d == Downstream || d == Both
}

// ImpactQuery asks what would be affected if an entity changed.
type ImpactQuery struct {
	// Root is the entity whose impact is being assessed.
	Root Endpoint
	// Direction selects the traversal; Downstream answers "what breaks if this
	// changes".
	Direction Direction
	// Depth bounds the walk. Zero means DefaultDepth.
	Depth int
	// IncludeColumnLevel descends to column-level edges, which is a much larger
	// result set than table-level traversal.
	IncludeColumnLevel bool
	// MaxNodes caps the response so a dense graph cannot exhaust memory.
	MaxNodes int
}

// Node is one entity in a traversal result.
type Node struct {
	Endpoint Endpoint `json:"endpoint"`

	// Depth is the number of hops from the root; the root itself is 0.
	Depth int `json:"depth"`

	// TransformType is how this node consumes or produces the edge that led to
	// it. Empty for the root.
	TransformType TransformType `json:"transform_type,omitempty"`

	// Path is the edge ids traversed to reach this node, useful for explaining
	// why a node is affected.
	Path []uuid.UUID `json:"path,omitempty"`
}

// Graph is the result of a traversal.
type Graph struct {
	Root Endpoint `json:"root"`
	// Nodes are in breadth-first order from the root.
	Nodes []Node `json:"nodes"`
	// Edges are the distinct edges within the traversed region.
	Edges []Edge `json:"edges"`
	// Truncated is true when the walk hit MaxNodes before exhausting the
	// reachable set, so the caller knows the answer is partial.
	Truncated bool `json:"truncated"`
}

// Traversal limits. Depth is capped because lineage chains in a real warehouse
// can be long, and MaxNodes bounds the response size.
const (
	DefaultDepth    = 10
	MaxDepth        = 50
	DefaultMaxNodes = 500
	MaxNodesLimit   = 5000
)

func normaliseEntityType(v string) EntityType {
	return EntityType(strings.ToLower(strings.TrimSpace(v)))
}

func normaliseTransformType(v string) TransformType {
	return TransformType(strings.ToLower(strings.TrimSpace(v)))
}

func normaliseDirection(v string) Direction {
	return Direction(strings.ToLower(strings.TrimSpace(v)))
}

// decodeJSONMap converts a nullable JSONB column into a map.
func decodeJSONMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}
