package lineage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/edp/edp-control-plane/internal/infrastructure/postgres"
	"github.com/edp/edp-control-plane/internal/platform"
)

// Repository is the persistence contract for lineage.
type Repository interface {
	// RecordEdge upserts an edge. Re-observing the same endpoint pair updates
	// the existing row rather than duplicating it.
	RecordEdge(ctx context.Context, e *Edge) error
	GetEdgeByID(ctx context.Context, id uuid.UUID) (*Edge, error)
	ListEdges(ctx context.Context, q ListEdgesQuery) (platform.PageResult[*Edge], error)
	DeleteEdge(ctx context.Context, id uuid.UUID) error

	// Neighbours returns the edges adjacent to an endpoint in one direction.
	// Traversal is done in Go, one hop at a time, so that a single bad edge
	// cannot trigger a recursive query that runs away.
	Neighbours(ctx context.Context, from Endpoint, direction Direction, includeColumns bool) ([]Edge, error)
	// EdgesWithin fetches the edges among a set of endpoints, for assembling a
	// traversal result once the node set is known.
	EdgesWithin(ctx context.Context, endpoints []Endpoint) ([]Edge, error)
}

// pgRepository is the PostgreSQL implementation.
type pgRepository struct {
	db *pgxpool.Pool
}

var _ Repository = (*pgRepository)(nil)

// NewRepository builds a PostgreSQL-backed lineage repository.
func NewRepository(db postgres.DB) Repository {
	return &pgRepository{db: db.Pool()}
}

const edgeColumns = `
	id,
	from_entity_type, from_entity_id, from_column,
	to_entity_type, to_entity_id, to_column,
	transform_type, transform_expression, confidence,
	observed_at, observed_by_run_id,
	metadata, created_at, updated_at`

func scanEdge(row pgx.Row) (*Edge, error) {
	var (
		e             Edge
		fromType      string
		toType        string
		transformType string
		metadataJSON  []byte
	)

	err := row.Scan(
		&e.ID,
		&fromType, &e.From.EntityID, &e.From.Column,
		&toType, &e.To.EntityID, &e.To.Column,
		&transformType, &e.TransformExpression, &e.Confidence,
		&e.ObservedAt, &e.ObservedByRunID,
		&metadataJSON, &e.CreatedAt, &e.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	e.From.EntityType = EntityType(fromType)
	e.To.EntityType = EntityType(toType)
	e.TransformType = TransformType(transformType)
	e.Metadata = decodeJSONMap(metadataJSON)
	return &e, nil
}

// RecordEdge upserts an edge on its natural key.
//
// ON CONFLICT means re-running a parser over the same queries converges on one
// row with a refreshed observation time, instead of accumulating near-duplicate
// lineage that would make the graph unusable.
func (r *pgRepository) RecordEdge(ctx context.Context, e *Edge) error {
	metadata, err := postgres.JSONB(e.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	const query = `
		INSERT INTO lineage_edges (
			from_entity_type, from_entity_id, from_column,
			to_entity_type, to_entity_id, to_column,
			transform_type, transform_expression, confidence,
			observed_at, observed_by_run_id, metadata
		) VALUES (
			$1, $2, $3,
			$4, $5, $6,
			$7, $8, $9,
			$10, $11, $12
		)
		ON CONFLICT (
			from_entity_type, from_entity_id, from_column,
			to_entity_type, to_entity_id, to_column
		) DO UPDATE SET
			transform_type = EXCLUDED.transform_type,
			transform_expression = EXCLUDED.transform_expression,
			confidence = EXCLUDED.confidence,
			observed_at = EXCLUDED.observed_at,
			observed_by_run_id = EXCLUDED.observed_by_run_id,
			metadata = EXCLUDED.metadata,
			updated_at = now()
		RETURNING id, created_at, updated_at`

	err = r.db.QueryRow(ctx, query,
		string(e.From.EntityType), e.From.EntityID, e.From.Column,
		string(e.To.EntityType), e.To.EntityID, e.To.Column,
		string(e.TransformType), e.TransformExpression, e.Confidence,
		e.ObservedAt, e.ObservedByRunID, metadata,
	).Scan(&e.ID, &e.CreatedAt, &e.UpdatedAt)

	if err != nil {
		return fmt.Errorf("upsert lineage edge: %w", err)
	}
	return nil
}

func (r *pgRepository) GetEdgeByID(ctx context.Context, id uuid.UUID) (*Edge, error) {
	query := `SELECT ` + edgeColumns + ` FROM lineage_edges WHERE id = $1`

	edge, err := scanEdge(r.db.QueryRow(ctx, query, id))
	if err != nil {
		return nil, fmt.Errorf("select lineage edge: %w", err)
	}
	return edge, nil
}

func (r *pgRepository) ListEdges(ctx context.Context, q ListEdgesQuery) (platform.PageResult[*Edge], error) {
	var (
		clauses = []string{"TRUE"}
		args    []any
	)

	add := func(clause string, values ...any) {
		for _, value := range values {
			args = append(args, value)
		}
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}

	if q.FromEntityType != "" {
		add("from_entity_type = $%d", string(normaliseEntityType(q.FromEntityType)))
	}
	if q.FromEntityID != "" {
		id, err := uuid.Parse(q.FromEntityID)
		if err != nil {
			return platform.PageResult[*Edge]{}, platform.NewBadRequest(
				"from_entity_id must be a valid UUID")
		}
		add("from_entity_id = $%d", id)
	}
	if q.FromColumn != "" {
		add("from_column = $%d", q.FromColumn)
	}
	if q.ToEntityType != "" {
		add("to_entity_type = $%d", string(normaliseEntityType(q.ToEntityType)))
	}
	if q.ToEntityID != "" {
		id, err := uuid.Parse(q.ToEntityID)
		if err != nil {
			return platform.PageResult[*Edge]{}, platform.NewBadRequest(
				"to_entity_id must be a valid UUID")
		}
		add("to_entity_id = $%d", id)
	}
	if q.ToColumn != "" {
		add("to_column = $%d", q.ToColumn)
	}
	if q.TransformType != "" {
		add("transform_type = $%d", string(normaliseTransformType(q.TransformType)))
	}
	if q.ColumnLevelOnly {
		// Table-level lineage is the useful default; a column filter makes the
		// result explicitly about columns rather than merely about tables that
		// happen to have a column edge.
		add("(from_column <> '' OR to_column <> '')")
	}

	where := " WHERE " + strings.Join(clauses, " AND ")
	orderColumn := map[string]string{
		"observed_at": "observed_at", "created_at": "created_at",
		"confidence": "confidence",
	}[q.Sort.By]
	if orderColumn == "" {
		orderColumn = "observed_at"
	}

	var total int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM lineage_edges`+where, args...).Scan(&total); err != nil {
		return platform.PageResult[*Edge]{}, fmt.Errorf("count lineage edges: %w", err)
	}
	if total == 0 {
		return platform.NewPageResult([]*Edge{}, 0, q.Limit, q.Offset), nil
	}

	args = append(args, q.Limit, q.Offset)
	listQuery := fmt.Sprintf(
		`SELECT %s FROM lineage_edges%s ORDER BY %s %s, id LIMIT $%d OFFSET $%d`,
		edgeColumns, where, orderColumn, q.Sort.Direction(), len(args)-1, len(args))

	rows, err := r.db.Query(ctx, listQuery, args...)
	if err != nil {
		return platform.PageResult[*Edge]{}, fmt.Errorf("list lineage edges: %w", err)
	}
	defer rows.Close()

	items := make([]*Edge, 0, q.Limit)
	for rows.Next() {
		edge, err := scanEdge(rows)
		if err != nil {
			return platform.PageResult[*Edge]{}, fmt.Errorf("scan lineage edge: %w", err)
		}
		items = append(items, edge)
	}
	if err := rows.Err(); err != nil {
		return platform.PageResult[*Edge]{}, fmt.Errorf("iterate lineage edge rows: %w", err)
	}

	return platform.NewPageResult(items, total, q.Limit, q.Offset), nil
}

// DeleteEdge removes an edge. Unlike other domains, lineage edges are hard
// deleted: a wrong edge is worse than a missing one, and re-parsing restores it.
func (r *pgRepository) DeleteEdge(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM lineage_edges WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete lineage edge: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *pgRepository) Neighbours(ctx context.Context, from Endpoint, direction Direction, includeColumns bool) ([]Edge, error) {
	// The column predicate is applied to both endpoints. A lineage graph mixes
	// table-level and column-level edges, and traversing from a table through a
	// column edge would report a "dataset A -> column c of B" hop that is not
	// what a caller asking about A's consumers means.
	columnFilter := "(from_column = '' AND to_column = '')"
	if includeColumns {
		columnFilter = "TRUE"
	}

	var (
		clauses []string
		args    []any
		order   = "observed_at DESC"
	)

	// The endpoint side of the edge depends on the direction: walking upstream
	// finds edges whose destination is the endpoint, walking downstream finds
	// edges whose source is, and Both matches either side.
	switch direction {
	case Upstream:
		clauses = []string{
			"to_entity_type = $1",
			"to_entity_id = $2",
			fmt.Sprintf("(%s)", columnFilter),
		}
	case Downstream:
		clauses = []string{
			"from_entity_type = $1",
			"from_entity_id = $2",
			fmt.Sprintf("(%s)", columnFilter),
		}
	case Both:
		clauses = []string{
			"(from_entity_type = $1 AND from_entity_id = $2)",
			"OR (to_entity_type = $1 AND to_entity_id = $2)",
			fmt.Sprintf("(%s)", columnFilter),
		}
	default:
		return nil, fmt.Errorf("unsupported traversal direction %q", direction)
	}

	args = []any{string(from.EntityType), from.EntityID}

	// When the endpoint names a column, restrict to edges touching that column so
	// a column-scoped query does not return the whole entity's edges. A table-level
	// edge (empty column name) is always included, because a column consumer is
	// also affected by changes to the table as a whole.
	if from.Column != "" && includeColumns {
		// The column name is bound as a value rather than interpolated into the
		// statement, so a crafted column name cannot alter the query.
		columnIndex := len(args) + 1
		args = append(args, from.Column)

		switch direction {
		case Upstream:
			clauses = append(clauses,
				fmt.Sprintf("(to_column = $%d OR to_column = '')", columnIndex))
		case Downstream:
			clauses = append(clauses,
				fmt.Sprintf("(from_column = $%d OR from_column = '')", columnIndex))
		case Both:
			clauses = append(clauses, fmt.Sprintf(
				"(from_column = $%d OR from_column = '' OR to_column = $%d OR to_column = '')",
				columnIndex, columnIndex))
		}
	}

	// The limit placeholder is numbered before its argument is appended, so it has
	// to point at the position that argument will occupy.
	query := fmt.Sprintf(
		`SELECT %s FROM lineage_edges
		 WHERE %s
		 ORDER BY %s
		 LIMIT $%d`,
		edgeColumns, strings.Join(clauses, " AND "), order, len(args)+1)

	args = append(args, neighbourLimit)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("select lineage neighbours: %w", err)
	}
	defer rows.Close()

	out := []Edge{}
	for rows.Next() {
		edge, err := scanEdge(rows)
		if err != nil {
			return nil, fmt.Errorf("scan lineage edge: %w", err)
		}
		out = append(out, *edge)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate lineage edge rows: %w", err)
	}
	return out, nil
}

// EdgesWithin fetches every edge whose endpoints both appear in the given set.
// It is the second half of a traversal: after the node set is known, this pulls
// the edges to show alongside it.
func (r *pgRepository) EdgesWithin(ctx context.Context, endpoints []Endpoint) ([]Edge, error) {
	if len(endpoints) == 0 {
		return []Edge{}, nil
	}

	// Passing the endpoints as a composite array keeps this to one query rather
	// than one per node, which matters when the traversal reached hundreds.
	ids := make([]uuid.UUID, 0, len(endpoints))
	byID := make(map[uuid.UUID][]Endpoint, len(endpoints))
	for _, e := range endpoints {
		ids = append(ids, e.EntityID)
		byID[e.EntityID] = append(byID[e.EntityID], e)
	}

	const query = `
		SELECT ` + edgeColumns + `
		FROM lineage_edges
		WHERE from_entity_id = ANY($1) OR to_entity_id = ANY($1)
		ORDER BY observed_at DESC`

	rows, err := r.db.Query(ctx, query, ids)
	if err != nil {
		return nil, fmt.Errorf("select lineage edges within endpoints: %w", err)
	}
	defer rows.Close()

	out := []Edge{}
	for rows.Next() {
		edge, err := scanEdge(rows)
		if err != nil {
			return nil, fmt.Errorf("scan lineage edge: %w", err)
		}
		// The ANY filter is on id alone, so narrow to edges where both endpoints
		// were actually in the traversal set.
		if _, ok := byID[edge.From.EntityID]; !ok {
			continue
		}
		if _, ok := byID[edge.To.EntityID]; !ok {
			continue
		}
		out = append(out, *edge)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate lineage edge rows: %w", err)
	}
	return out, nil
}

// neighbourLimit bounds one hop. It is generous relative to MaxNodes because a
// single high-fanout entity legitimately has many consumers, and truncating
// here silently would report an under-count that looks authoritative.
const neighbourLimit = 5000

// edgeKey identifies an endpoint for de-duplication during traversal. Column is
// part of the key only when column-level traversal is in play, handled by the
// caller.
func edgeKey(e Endpoint) string {
	return string(e.EntityType) + ":" + e.EntityID.String() + ":" + e.Column
}

// observedAt returns the edge's observation time, defaulting to the epoch for an
// edge that somehow has none.
func observedAt(e Edge) time.Time {
	if e.ObservedAt.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return e.ObservedAt
}
