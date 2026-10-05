package source

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/edp/edp-control-plane/internal/infrastructure/postgres"
	"github.com/edp/edp-control-plane/internal/platform"
)

// ErrNotFound is returned by Get* when no live row matches.
var ErrNotFound = errors.New("source not found")

// ErrSlugTaken is returned when a slug collides with an existing source.
var ErrSlugTaken = errors.New("source slug is already in use")

// Repository is the persistence contract for sources. Handlers and services
// depend on this interface, never on the SQL implementation, which keeps the
// domain testable without a database.
type Repository interface {
	Create(ctx context.Context, s *Source) error
	GetByID(ctx context.Context, id uuid.UUID) (*Source, error)
	GetBySlug(ctx context.Context, slug string) (*Source, error)
	List(ctx context.Context, q ListSourcesQuery) (platform.PageResult[*Source], error)
	Update(ctx context.Context, s *Source) error
	Delete(ctx context.Context, id uuid.UUID) error

	// RecordHealthCheck stores the outcome of a connectivity probe.
	RecordHealthCheck(ctx context.Context, id uuid.UUID, status HealthStatus, checkedAt time.Time) error

	// CountByOwner returns how many live sources the given owner holds.
	CountByOwner(ctx context.Context, ownerID string) (int, error)
}

// pgRepository is the PostgreSQL implementation of Repository.
type pgRepository struct {
	db *pgxpool.Pool
}

var _ Repository = (*pgRepository)(nil)

// NewRepository builds a PostgreSQL-backed source repository.
func NewRepository(db postgres.DB) Repository {
	return &pgRepository{db: db.Pool()}
}

// sourceColumns is the canonical SELECT list, kept in one place so the scan
// helper and every query stay in step.
const sourceColumns = `
	id, name, slug, description,
	source_type, ingestion_mode, environment, status,
	connection_secret_ref, connection_config,
	owner_id, tags,
	last_health_check_at, last_health_check_status,
	metadata,
	created_by, updated_by, created_at, updated_at`

// scanSource reads one row using the column order of sourceColumns.
func scanSource(row pgx.Row) (*Source, error) {
	var (
		s            Source
		typeStr      string
		modeStr      string
		envStr       string
		statusStr    string
		configJSON   []byte
		metadataJSON []byte
		healthStr    *string
	)

	err := row.Scan(
		&s.ID, &s.Name, &s.Slug, &s.Description,
		&typeStr, &modeStr, &envStr, &statusStr,
		&s.ConnectionSecretRef, &configJSON,
		&s.OwnerID, &s.Tags,
		&s.LastHealthCheckAt, &healthStr,
		&metadataJSON,
		&s.CreatedBy, &s.UpdatedBy, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	s.Type = Type(typeStr)
	s.Ingestion = IngestionMode(modeStr)
	s.Environment = Environment(envStr)
	s.Status = Status(statusStr)
	s.ConnectionConfig = jsonMap(configJSON)
	s.Metadata = jsonMap(metadataJSON)

	if healthStr != nil {
		h := HealthStatus(*healthStr)
		s.LastHealthCheckStatus = &h
	}
	if s.Tags == nil {
		s.Tags = []string{}
	}

	return &s, nil
}

func (r *pgRepository) Create(ctx context.Context, s *Source) error {
	const query = `
		INSERT INTO sources (
			name, slug, description, source_type, ingestion_mode, environment, status,
			connection_secret_ref, connection_config, owner_id, tags, metadata,
			created_by, updated_by
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12,
			$13, $14
		)
		RETURNING id, created_at, updated_at`

	config, err := postgres.JSONB(s.ConnectionConfig)
	if err != nil {
		return fmt.Errorf("encode connection_config: %w", err)
	}
	metadata, err := postgres.JSONB(s.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	err = r.db.QueryRow(ctx, query,
		s.Name, s.Slug, s.Description, string(s.Type), string(s.Ingestion),
		string(s.Environment), string(s.Status), s.ConnectionSecretRef,
		config, s.OwnerID, s.Tags, metadata, s.CreatedBy, s.UpdatedBy,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)

	if postgres.IsUniqueViolation(err, "sources_slug_unique") {
		return fmt.Errorf("%w: %q", ErrSlugTaken, s.Slug)
	}
	if err != nil {
		return fmt.Errorf("insert source: %w", err)
	}
	return nil
}

func (r *pgRepository) GetByID(ctx context.Context, id uuid.UUID) (*Source, error) {
	query := `SELECT ` + sourceColumns + ` FROM sources WHERE id = $1 AND deleted_at IS NULL`

	s, err := scanSource(r.db.QueryRow(ctx, query, id))
	switch {
	case postgres.IsNoRows(err):
		return nil, fmt.Errorf("%w: id %s", ErrNotFound, id)
	case err != nil:
		return nil, fmt.Errorf("select source by id: %w", err)
	}
	return s, nil
}

func (r *pgRepository) GetBySlug(ctx context.Context, slug string) (*Source, error) {
	query := `SELECT ` + sourceColumns + ` FROM sources WHERE slug = $1 AND deleted_at IS NULL`

	s, err := scanSource(r.db.QueryRow(ctx, query, slug))
	switch {
	case postgres.IsNoRows(err):
		return nil, fmt.Errorf("%w: slug %q", ErrNotFound, slug)
	case err != nil:
		return nil, fmt.Errorf("select source by slug: %w", err)
	}
	return s, nil
}

// List builds a filtered, sorted, paginated query. Filters are appended as
// positional parameters as they are recognised, so no caller-supplied value is
// ever concatenated into the SQL text; the sort column comes from the
// whitelisted map in platform.ParseSort.
func (r *pgRepository) List(ctx context.Context, q ListSourcesQuery) (platform.PageResult[*Source], error) {
	var (
		clauses = []string{"deleted_at IS NULL"}
		args    []any
	)

	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}

	if q.Type != "" {
		add("source_type = $%d", q.Type)
	}
	if q.Status != "" {
		add("status = $%d", q.Status)
	}
	if q.Environment != "" {
		add("environment = $%d", q.Environment)
	}
	if q.OwnerID != "" {
		add("owner_id = $%d", q.OwnerID)
	}
	if q.Search != "" {
		// to_tsvector over a text search expression, parameterised so the
		// pattern can never be interpreted as SQL.
		args = append(args, q.Search)
		clauses = append(clauses, fmt.Sprintf(
			"to_tsvector('simple', name || ' ' || description) @@ plainto_tsquery('simple', $%d)",
			len(args)))
	}
	for _, tag := range q.Tag {
		add("tags @> ARRAY[$%d]", tag)
	}

	where := " WHERE " + strings.Join(clauses, " AND ")

	// The sort column comes from the whitelisted map in platform.ParseSort, so
	// it cannot contain user-controlled SQL. An unset column falls back to the
	// creation order.
	orderColumn := strings.TrimSpace(q.Sort.By)
	if orderColumn == "" {
		orderColumn = "created_at"
	}
	orderBy := orderColumn + " " + q.Sort.Direction()

	countQuery := `SELECT count(*) FROM sources` + where

	var total int
	if err := r.db.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return platform.PageResult[*Source]{}, fmt.Errorf("count sources: %w", err)
	}
	if total == 0 {
		return platform.NewPageResult([]*Source{}, 0, q.Limit, q.Offset), nil
	}

	// Window the page.
	args = append(args, q.Limit, q.Offset)
	listQuery := fmt.Sprintf(
		`SELECT %s FROM sources%s ORDER BY %s LIMIT $%d OFFSET $%d`,
		sourceColumns, where, orderBy, len(args)-1, len(args),
	)

	rows, err := r.db.Query(ctx, listQuery, args...)
	if err != nil {
		return platform.PageResult[*Source]{}, fmt.Errorf("list sources: %w", err)
	}
	defer rows.Close()

	items := make([]*Source, 0, q.Limit)
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return platform.PageResult[*Source]{}, fmt.Errorf("scan source row: %w", err)
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return platform.PageResult[*Source]{}, fmt.Errorf("iterate source rows: %w", err)
	}

	return platform.NewPageResult(items, total, q.Limit, q.Offset), nil
}

// Update writes the mutable columns. The WHERE clause includes updated_at so a
// concurrent writer that started earlier cannot silently overwrite this one;
// the caller can detect the miss via ErrNotFound and retry.
func (r *pgRepository) Update(ctx context.Context, s *Source) error {
	config, err := postgres.JSONB(s.ConnectionConfig)
	if err != nil {
		return fmt.Errorf("encode connection_config: %w", err)
	}
	metadata, err := postgres.JSONB(s.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	const query = `
		UPDATE sources SET
			name = $1, slug = $2, description = $3,
			source_type = $4, ingestion_mode = $5, environment = $6, status = $7,
			connection_secret_ref = $8, connection_config = $9,
			owner_id = $10, tags = $11, metadata = $12,
			updated_by = $13
		WHERE id = $14 AND deleted_at IS NULL
		RETURNING updated_at`

	err = r.db.QueryRow(ctx, query,
		s.Name, s.Slug, s.Description, string(s.Type), string(s.Ingestion),
		string(s.Environment), string(s.Status), s.ConnectionSecretRef,
		config, s.OwnerID, s.Tags, metadata, s.UpdatedBy, s.ID,
	).Scan(&s.UpdatedAt)

	switch {
	case postgres.IsUniqueViolation(err, "sources_slug_unique"):
		return fmt.Errorf("%w: %q", ErrSlugTaken, s.Slug)
	case postgres.IsNoRows(err):
		return fmt.Errorf("%w: id %s", ErrNotFound, s.ID)
	case err != nil:
		return fmt.Errorf("update source: %w", err)
	}
	return nil
}

// Delete soft-deletes the row. A hard delete is deliberately not offered: the
// audit trail and lineage edges both reference sources, and retention
// obligations can outlast the operational need for the record.
func (r *pgRepository) Delete(ctx context.Context, id uuid.UUID) error {
	const query = `
		UPDATE sources
		SET deleted_at = now(), updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING id`

	var deleted uuid.UUID
	err := r.db.QueryRow(ctx, query, id).Scan(&deleted)

	switch {
	case postgres.IsNoRows(err):
		return fmt.Errorf("%w: id %s", ErrNotFound, id)
	case err != nil:
		return fmt.Errorf("delete source: %w", err)
	}
	return nil
}

func (r *pgRepository) RecordHealthCheck(ctx context.Context, id uuid.UUID, status HealthStatus, checkedAt time.Time) error {
	const query = `
		UPDATE sources
		SET last_health_check_status = $1, last_health_check_at = $2
		WHERE id = $3 AND deleted_at IS NULL`

	tag, err := r.db.Exec(ctx, query, string(status), checkedAt, id)
	if err != nil {
		return fmt.Errorf("record health check: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: id %s", ErrNotFound, id)
	}
	return nil
}

func (r *pgRepository) CountByOwner(ctx context.Context, ownerID string) (int, error) {
	const query = `
		SELECT count(*) FROM sources
		WHERE owner_id = $1 AND deleted_at IS NULL`

	var n int
	if err := r.db.QueryRow(ctx, query, ownerID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count sources by owner: %w", err)
	}
	return n, nil
}
