package dataset

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/edp/edp-control-plane/internal/infrastructure/postgres"
	"github.com/edp/edp-control-plane/internal/platform"
)

// Sentinel errors returned by the repository.
var (
	// ErrNotFound is returned when no live row matches.
	ErrNotFound = errors.New("dataset not found")
	// ErrSlugTaken is returned when a slug collides with an existing dataset.
	ErrSlugTaken = errors.New("dataset slug is already in use")
	// ErrSourceRequired is returned when a streaming dataset omits its source.
	ErrSourceRequired = errors.New("dataset requires a source")
	// ErrSchemaConflict is returned when a schema change is not permissible.
	ErrSchemaConflict = errors.New("dataset schema change is not permitted")
)

// Repository is the persistence contract for datasets.
type Repository interface {
	Create(ctx context.Context, d *Dataset, columns []Column) error
	GetByID(ctx context.Context, id uuid.UUID) (*Dataset, error)
	GetBySlug(ctx context.Context, slug string) (*Dataset, error)
	List(ctx context.Context, q ListDatasetsQuery) (platform.PageResult[*Dataset], error)
	Update(ctx context.Context, d *Dataset) error
	Delete(ctx context.Context, id uuid.UUID) error

	// ReplaceColumns swaps the column set atomically.
	ReplaceColumns(ctx context.Context, datasetID uuid.UUID, columns []Column) error
	// Columns reads the schema for a single dataset.
	Columns(ctx context.Context, datasetID uuid.UUID) ([]Column, error)

	// CountByClassification returns how many live datasets hold each
	// classification, for the governance dashboard.
	CountByClassification(ctx context.Context) (map[Classification]int, error)
}

// pgRepository is the PostgreSQL implementation of Repository.
type pgRepository struct {
	db *pgxpool.Pool
}

var _ Repository = (*pgRepository)(nil)

// NewRepository builds a PostgreSQL-backed dataset repository.
func NewRepository(db postgres.DB) Repository {
	return &pgRepository{db: db.Pool()}
}

// datasetColumns is the canonical SELECT list for datasets.
const datasetColumns = `
	id, name, slug, description,
	dataset_kind, classification, status,
	physical_location, source_id,
	domain, retention_days, schema_version,
	owner_id, steward_id, tags,
	row_count, size_bytes, last_refreshed_at,
	metadata,
	created_by, updated_by, created_at, updated_at`

func scanDataset(row pgx.Row) (*Dataset, error) {
	var (
		d            Dataset
		kindStr      string
		classStr     string
		statusStr    string
		locationJSON []byte
		metadataJSON []byte
	)

	err := row.Scan(
		&d.ID, &d.Name, &d.Slug, &d.Description,
		&kindStr, &classStr, &statusStr,
		&locationJSON, &d.SourceID,
		&d.Domain, &d.RetentionDays, &d.SchemaVersion,
		&d.OwnerID, &d.StewardID, &d.Tags,
		&d.RowCount, &d.SizeBytes, &d.LastRefreshedAt,
		&metadataJSON,
		&d.CreatedBy, &d.UpdatedBy, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	d.Kind = Kind(kindStr)
	d.Classification = Classification(classStr)
	d.Status = Status(statusStr)
	d.PhysicalLocation = jsonMap(locationJSON)
	d.Metadata = jsonMap(metadataJSON)
	if d.Tags == nil {
		d.Tags = []string{}
	}

	return &d, nil
}

// Create inserts the dataset and its schema in one transaction, so a dataset
// never exists without the columns it was declared with.
func (r *pgRepository) Create(ctx context.Context, d *Dataset, columns []Column) error {
	location, err := postgres.JSONB(d.PhysicalLocation)
	if err != nil {
		return fmt.Errorf("encode physical_location: %w", err)
	}
	metadata, err := postgres.JSONB(d.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	const insert = `
		INSERT INTO datasets (
			name, slug, description, dataset_kind, classification, status,
			physical_location, source_id, domain, retention_days, schema_version,
			owner_id, steward_id, tags, metadata, created_by, updated_by
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11,
			$12, $13, $14, $15, $16, $17
		)
		RETURNING id, created_at, updated_at`

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = tx.QueryRow(ctx, insert,
		d.Name, d.Slug, d.Description, string(d.Kind), string(d.Classification),
		string(d.Status), location, d.SourceID, d.Domain, d.RetentionDays,
		d.SchemaVersion, d.OwnerID, d.StewardID, d.Tags, metadata,
		d.CreatedBy, d.UpdatedBy,
	).Scan(&d.ID, &d.CreatedAt, &d.UpdatedAt)

	if postgres.IsUniqueViolation(err, "datasets_slug_unique") {
		return fmt.Errorf("%w: %q", ErrSlugTaken, d.Slug)
	}
	if postgres.IsForeignKeyViolation(err) {
		return fmt.Errorf("%w: the referenced source does not exist", ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("insert dataset: %w", err)
	}

	if err := insertColumns(ctx, tx, d.ID, columns); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit dataset insert: %w", err)
	}

	d.Columns = columns
	return nil
}

// insertColumns writes a column set within the caller's transaction.
func insertColumns(ctx context.Context, tx pgx.Tx, datasetID uuid.UUID, columns []Column) error {
	if len(columns) == 0 {
		return nil
	}

	const insert = `
		INSERT INTO dataset_columns (
			dataset_id, name, ordinal, data_type, is_nullable, is_primary_key,
			is_sensitive, pii_class, default_value, description, masking_strategy
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	// A single multi-row INSERT: one round trip instead of N.
	batch := &pgx.Batch{}
	for _, c := range columns {
		batch.Queue(insert,
			datasetID, c.Name, c.Ordinal, c.DataType, c.IsNullable,
			c.IsPrimaryKey, c.IsSensitive, string(c.PIIClass),
			c.DefaultValue, c.Description, string(c.Masking))
	}

	results := tx.SendBatch(ctx, batch)
	defer func() { _ = results.Close() }()

	for range columns {
		if _, err := results.Exec(); err != nil {
			if postgres.IsUniqueViolation(err, "") {
				return fmt.Errorf("%w: duplicate column name or ordinal", ErrSchemaConflict)
			}
			return fmt.Errorf("insert dataset column: %w", err)
		}
	}
	return nil
}

func (r *pgRepository) GetByID(ctx context.Context, id uuid.UUID) (*Dataset, error) {
	query := `SELECT ` + datasetColumns + ` FROM datasets WHERE id = $1 AND deleted_at IS NULL`

	d, err := scanDataset(r.db.QueryRow(ctx, query, id))
	switch {
	case postgres.IsNoRows(err):
		return nil, fmt.Errorf("%w: id %s", ErrNotFound, id)
	case err != nil:
		return nil, fmt.Errorf("select dataset by id: %w", err)
	}
	return d, nil
}

func (r *pgRepository) GetBySlug(ctx context.Context, slug string) (*Dataset, error) {
	query := `SELECT ` + datasetColumns + ` FROM datasets WHERE slug = $1 AND deleted_at IS NULL`

	d, err := scanDataset(r.db.QueryRow(ctx, query, slug))
	switch {
	case postgres.IsNoRows(err):
		return nil, fmt.Errorf("%w: slug %q", ErrNotFound, slug)
	case err != nil:
		return nil, fmt.Errorf("select dataset by slug: %w", err)
	}
	return d, nil
}

func (r *pgRepository) List(ctx context.Context, q ListDatasetsQuery) (platform.PageResult[*Dataset], error) {
	var (
		clauses = []string{"deleted_at IS NULL"}
		args    []any
	)

	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}

	if q.Kind != "" {
		add("dataset_kind = $%d", q.Kind)
	}
	if q.Classification != "" {
		add("classification = $%d", q.Classification)
	}
	if q.Status != "" {
		add("status = $%d", q.Status)
	}
	if q.SourceID != "" {
		add("source_id = $%d", q.SourceID)
	}
	if q.Domain != "" {
		add("domain = $%d", q.Domain)
	}
	if q.OwnerID != "" {
		add("owner_id = $%d", q.OwnerID)
	}
	if q.StewardID != "" {
		add("steward_id = $%d", q.StewardID)
	}
	if q.Search != "" {
		args = append(args, q.Search)
		clauses = append(clauses, fmt.Sprintf(
			"to_tsvector('simple', name || ' ' || description) @@ plainto_tsquery('simple', $%d)",
			len(args)))
	}
	for _, tag := range q.Tag {
		add("tags @> ARRAY[$%d]", tag)
	}

	where := " WHERE " + strings.Join(clauses, " AND ")

	orderColumn := strings.TrimSpace(q.Sort.By)
	if orderColumn == "" {
		orderColumn = "created_at"
	}
	orderBy := orderColumn + " " + q.Sort.Direction()

	var total int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM datasets`+where, args...).Scan(&total); err != nil {
		return platform.PageResult[*Dataset]{}, fmt.Errorf("count datasets: %w", err)
	}
	if total == 0 {
		return platform.NewPageResult([]*Dataset{}, 0, q.Limit, q.Offset), nil
	}

	args = append(args, q.Limit, q.Offset)
	listQuery := fmt.Sprintf(
		`SELECT %s FROM datasets%s ORDER BY %s LIMIT $%d OFFSET $%d`,
		datasetColumns, where, orderBy, len(args)-1, len(args))

	rows, err := r.db.Query(ctx, listQuery, args...)
	if err != nil {
		return platform.PageResult[*Dataset]{}, fmt.Errorf("list datasets: %w", err)
	}
	defer rows.Close()

	items := make([]*Dataset, 0, q.Limit)
	for rows.Next() {
		d, err := scanDataset(rows)
		if err != nil {
			return platform.PageResult[*Dataset]{}, fmt.Errorf("scan dataset row: %w", err)
		}
		items = append(items, d)
	}
	if err := rows.Err(); err != nil {
		return platform.PageResult[*Dataset]{}, fmt.Errorf("iterate dataset rows: %w", err)
	}

	result := platform.NewPageResult(items, total, q.Limit, q.Offset)

	if q.IncludeColumns && len(items) > 0 {
		if err := r.attachColumns(ctx, items); err != nil {
			return platform.PageResult[*Dataset]{}, err
		}
	}
	return result, nil
}

// attachColumns loads the schema for many datasets in one query, avoiding an
// N+1 pattern when the caller asked for columns.
func (r *pgRepository) attachColumns(ctx context.Context, items []*Dataset) error {
	byID := make(map[uuid.UUID][]Column, len(items))
	args := make([]any, 0, len(items))
	placeholders := make([]string, 0, len(items))

	for i, item := range items {
		args = append(args, item.ID)
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
	}

	// The placeholder list is generated from the caller's slice length, so this
	// cannot be a constant string. The values themselves remain parameters.
	query := `
		SELECT id, name, ordinal, data_type, is_nullable, is_primary_key,
		       is_sensitive, pii_class, default_value, description,
		       masking_strategy, created_at, updated_at
		FROM dataset_columns
		WHERE dataset_id IN (` + strings.Join(placeholders, ", ") + `)
		ORDER BY dataset_id, ordinal`

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("load dataset columns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			datasetID uuid.UUID
			c         Column
			piiStr    string
			maskStr   string
		)
		if err := rows.Scan(
			&datasetID, &c.Name, &c.Ordinal, &c.DataType, &c.IsNullable,
			&c.IsPrimaryKey, &c.IsSensitive, &piiStr, &c.DefaultValue,
			&c.Description, &maskStr, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return fmt.Errorf("scan dataset column: %w", err)
		}
		c.ID = datasetID
		c.PIIClass = PIIClass(piiStr)
		c.Masking = MaskingStrategy(maskStr)
		byID[datasetID] = append(byID[datasetID], c)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate dataset column rows: %w", err)
	}

	for _, item := range items {
		if cols, ok := byID[item.ID]; ok {
			item.Columns = cols
		} else {
			item.Columns = []Column{}
		}
	}
	return nil
}

func (r *pgRepository) Update(ctx context.Context, d *Dataset) error {
	location, err := postgres.JSONB(d.PhysicalLocation)
	if err != nil {
		return fmt.Errorf("encode physical_location: %w", err)
	}
	metadata, err := postgres.JSONB(d.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	const query = `
		UPDATE datasets SET
			name = $1, slug = $2, description = $3,
			dataset_kind = $4, classification = $5, status = $6,
			physical_location = $7, source_id = $8,
			domain = $9, retention_days = $10, schema_version = $11,
			owner_id = $12, steward_id = $13, tags = $14, metadata = $15,
			updated_by = $16
		WHERE id = $17 AND deleted_at IS NULL
		RETURNING updated_at`

	err = r.db.QueryRow(ctx, query,
		d.Name, d.Slug, d.Description, string(d.Kind), string(d.Classification),
		string(d.Status), location, d.SourceID, d.Domain, d.RetentionDays,
		d.SchemaVersion, d.OwnerID, d.StewardID, d.Tags, metadata,
		d.UpdatedBy, d.ID,
	).Scan(&d.UpdatedAt)

	switch {
	case postgres.IsUniqueViolation(err, "datasets_slug_unique"):
		return fmt.Errorf("%w: %q", ErrSlugTaken, d.Slug)
	case postgres.IsNoRows(err):
		return fmt.Errorf("%w: id %s", ErrNotFound, d.ID)
	case err != nil:
		return fmt.Errorf("update dataset: %w", err)
	}
	return nil
}

func (r *pgRepository) Delete(ctx context.Context, id uuid.UUID) error {
	const query = `
		UPDATE datasets SET deleted_at = now(), updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING id`

	var deleted uuid.UUID
	err := r.db.QueryRow(ctx, query, id).Scan(&deleted)

	switch {
	case postgres.IsNoRows(err):
		return fmt.Errorf("%w: id %s", ErrNotFound, id)
	case err != nil:
		return fmt.Errorf("delete dataset: %w", err)
	}
	return nil
}

// ReplaceColumns swaps the column set atomically, so readers never observe a
// partially rewritten schema.
func (r *pgRepository) ReplaceColumns(ctx context.Context, datasetID uuid.UUID, columns []Column) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const deleteAll = `DELETE FROM dataset_columns WHERE dataset_id = $1`
	if _, err := tx.Exec(ctx, deleteAll, datasetID); err != nil {
		return fmt.Errorf("clear dataset columns: %w", err)
	}

	if err := insertColumns(ctx, tx, datasetID, columns); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit column replacement: %w", err)
	}
	return nil
}

func (r *pgRepository) Columns(ctx context.Context, datasetID uuid.UUID) ([]Column, error) {
	const query = `
		SELECT id, name, ordinal, data_type, is_nullable, is_primary_key,
		       is_sensitive, pii_class, default_value, description,
		       masking_strategy, created_at, updated_at
		FROM dataset_columns
		WHERE dataset_id = $1
		ORDER BY ordinal`

	rows, err := r.db.Query(ctx, query, datasetID)
	if err != nil {
		return nil, fmt.Errorf("select dataset columns: %w", err)
	}
	defer rows.Close()

	out := make([]Column, 0, 16)
	for rows.Next() {
		var (
			c       Column
			piiStr  string
			maskStr string
		)
		if err := rows.Scan(
			&c.ID, &c.Name, &c.Ordinal, &c.DataType, &c.IsNullable,
			&c.IsPrimaryKey, &c.IsSensitive, &piiStr, &c.DefaultValue,
			&c.Description, &maskStr, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan dataset column: %w", err)
		}
		c.PIIClass = PIIClass(piiStr)
		c.Masking = MaskingStrategy(maskStr)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dataset column rows: %w", err)
	}
	return out, nil
}

func (r *pgRepository) CountByClassification(ctx context.Context) (map[Classification]int, error) {
	const query = `
		SELECT classification, count(*)
		FROM datasets
		WHERE deleted_at IS NULL
		GROUP BY classification`

	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("count datasets by classification: %w", err)
	}
	defer rows.Close()

	out := make(map[Classification]int, len(allClassifications))
	for rows.Next() {
		var (
			class string
			n     int
		)
		if err := rows.Scan(&class, &n); err != nil {
			return nil, fmt.Errorf("scan classification count: %w", err)
		}
		out[Classification(class)] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate classification counts: %w", err)
	}
	return out, nil
}
