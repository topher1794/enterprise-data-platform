package pipeline

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
	ErrNotFound = errors.New("pipeline not found")
	// ErrSlugTaken is returned when a slug collides with an existing pipeline.
	ErrSlugTaken = errors.New("pipeline slug is already in use")
	// ErrDAGIDTaken is returned when a DAG identifier is already deployed.
	ErrDAGIDTaken = errors.New("pipeline dag_id is already in use")
	// ErrArchiveConflict is returned when an archived pipeline is modified.
	ErrArchiveConflict = errors.New("pipeline is archived")
)

// Repository is the persistence contract for pipelines and their graphs.
type Repository interface {
	Create(ctx context.Context, p *Pipeline, tasks []Task, deps []Dependency) error
	GetByID(ctx context.Context, id uuid.UUID) (*Pipeline, error)
	GetBySlug(ctx context.Context, slug string) (*Pipeline, error)
	List(ctx context.Context, q ListPipelinesQuery) (platform.PageResult[*Pipeline], error)
	Update(ctx context.Context, p *Pipeline) error
	Delete(ctx context.Context, id uuid.UUID) error

	// Tasks reads the task set in ordinal-stable key order.
	Tasks(ctx context.Context, pipelineID uuid.UUID) ([]Task, error)
	// Dependencies reads the graph edges.
	Dependencies(ctx context.Context, pipelineID uuid.UUID) ([]Dependency, error)

	// ReplaceGraph swaps tasks and edges atomically and updates task_count and
	// graph_hash on the parent row.
	ReplaceGraph(ctx context.Context, pipelineID uuid.UUID, tasks []Task, deps []Dependency, graphHash string) error

	// AllDAGIDs returns every deployed DAG identifier, used by the validator to
	// detect collisions before a write.
	AllDAGIDs(ctx context.Context) (map[string]bool, error)
}

// pgRepository is the PostgreSQL implementation of Repository.
type pgRepository struct {
	db *pgxpool.Pool
}

var _ Repository = (*pgRepository)(nil)

// NewRepository builds a PostgreSQL-backed pipeline repository.
func NewRepository(db postgres.DB) Repository {
	return &pgRepository{db: db.Pool()}
}

// pipelineColumns is the canonical SELECT list for pipelines.
const pipelineColumns = `
	id, name, slug, description,
	dag_id, status, schedule, timezone,
	max_active_runs, catchup, default_dataset_id,
	owner_id, tags, task_count, graph_hash,
	metadata,
	created_by, updated_by, created_at, updated_at`

func scanPipeline(row pgx.Row) (*Pipeline, error) {
	var (
		p            Pipeline
		statusStr    string
		metadataJSON []byte
	)

	err := row.Scan(
		&p.ID, &p.Name, &p.Slug, &p.Description,
		&p.DAGID, &statusStr, &p.Schedule, &p.Timezone,
		&p.MaxActiveRuns, &p.Catchup, &p.DefaultDatasetID,
		&p.OwnerID, &p.Tags, &p.TaskCount, &p.GraphHash,
		&metadataJSON,
		&p.CreatedBy, &p.UpdatedBy, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	p.Status = Status(statusStr)
	p.Metadata = jsonMap(metadataJSON)
	if p.Tags == nil {
		p.Tags = []string{}
	}

	return &p, nil
}

// Create writes the pipeline and its graph in a single transaction, so a
// pipeline is never visible with a partial DAG.
func (r *pgRepository) Create(ctx context.Context, p *Pipeline, tasks []Task, deps []Dependency) error {
	metadata, err := postgres.JSONB(p.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	const insert = `
		INSERT INTO pipelines (
			name, slug, description, dag_id, status, schedule, timezone,
			max_active_runs, catchup, default_dataset_id,
			owner_id, tags, task_count, graph_hash, metadata,
			created_by, updated_by
		) VALUES (
			$1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), $7,
			$8, $9, $10,
			$11, $12, $13, $14, $15,
			$16, $17
		)
		RETURNING id, created_at, updated_at`

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = tx.QueryRow(ctx, insert,
		p.Name, p.Slug, p.Description, p.DAGID, string(p.Status), p.Schedule,
		p.Timezone, p.MaxActiveRuns, p.Catchup, p.DefaultDatasetID,
		p.OwnerID, p.Tags, p.TaskCount, p.GraphHash, metadata,
		p.CreatedBy, p.UpdatedBy,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)

	if postgres.IsUniqueViolation(err, "pipelines_slug_unique") {
		return fmt.Errorf("%w: %q", ErrSlugTaken, p.Slug)
	}
	if postgres.IsUniqueViolation(err, "pipelines_dag_id_unique") {
		return fmt.Errorf("%w: %q", ErrDAGIDTaken, p.DAGID)
	}
	if postgres.IsForeignKeyViolation(err) {
		return fmt.Errorf("%w: the referenced dataset does not exist", ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("insert pipeline: %w", err)
	}

	if err := insertGraph(ctx, tx, p.ID, tasks, deps); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit pipeline insert: %w", err)
	}

	p.Tasks = tasks
	p.Dependencies = deps
	return nil
}

// insertGraph writes tasks then edges within the caller's transaction. Tasks
// are inserted before edges because the edge foreign keys reference them.
func insertGraph(ctx context.Context, tx pgx.Tx, pipelineID uuid.UUID, tasks []Task, deps []Dependency) error {
	if len(tasks) > 0 {
		const insertTask = `
			INSERT INTO pipeline_tasks (
				pipeline_id, task_key, name, operator_type, operator_config,
				upstream_dataset_id, downstream_dataset_id,
				max_retries, retry_delay_seconds, timeout_seconds, sla_minutes,
				is_critical, enabled
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			RETURNING id`

		batch := &pgx.Batch{}
		for i := range tasks {
			t := &tasks[i]
			config, err := postgres.JSONB(t.OperatorConfig)
			if err != nil {
				return fmt.Errorf("encode operator_config for %s: %w", t.TaskKey, err)
			}

			batch.Queue(insertTask,
				pipelineID, t.TaskKey, t.Name, t.OperatorType, config,
				t.UpstreamDatasetID, t.DownstreamDatasetID,
				t.MaxRetries, t.RetryDelaySeconds, t.TimeoutSeconds, t.SLAMinutes,
				t.IsCritical, t.Enabled)
		}

		results := tx.SendBatch(ctx, batch)
		for i := range tasks {
			row := results.QueryRow()
			if err := row.Scan(&tasks[i].ID); err != nil {
				_ = results.Close()
				return fmt.Errorf("insert task %s: %w", tasks[i].TaskKey, err)
			}
		}
		if err := results.Close(); err != nil {
			return fmt.Errorf("flush task batch: %w", err)
		}
	}

	if len(deps) > 0 {
		const insertDep = `
			INSERT INTO pipeline_dependencies (pipeline_id, upstream_key, downstream_key)
			VALUES ($1, $2, $3)`

		batch := &pgx.Batch{}
		for _, d := range deps {
			batch.Queue(insertDep, pipelineID, d.UpstreamKey, d.DownstreamKey)
		}

		results := tx.SendBatch(ctx, batch)
		for i := range deps {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				if postgres.IsForeignKeyViolation(err) {
					return fmt.Errorf("%w: dependency %s -> %s references an unknown task",
						ErrNotFound, deps[i].UpstreamKey, deps[i].DownstreamKey)
				}
				return fmt.Errorf("insert dependency %s -> %s: %w",
					deps[i].UpstreamKey, deps[i].DownstreamKey, err)
			}
		}
		if err := results.Close(); err != nil {
			return fmt.Errorf("flush dependency batch: %w", err)
		}
	}

	return nil
}

func (r *pgRepository) GetByID(ctx context.Context, id uuid.UUID) (*Pipeline, error) {
	query := `SELECT ` + pipelineColumns + ` FROM pipelines WHERE id = $1 AND deleted_at IS NULL`

	p, err := scanPipeline(r.db.QueryRow(ctx, query, id))
	switch {
	case postgres.IsNoRows(err):
		return nil, fmt.Errorf("%w: id %s", ErrNotFound, id)
	case err != nil:
		return nil, fmt.Errorf("select pipeline by id: %w", err)
	}
	return p, nil
}

func (r *pgRepository) GetBySlug(ctx context.Context, slug string) (*Pipeline, error) {
	query := `SELECT ` + pipelineColumns + ` FROM pipelines WHERE slug = $1 AND deleted_at IS NULL`

	p, err := scanPipeline(r.db.QueryRow(ctx, query, slug))
	switch {
	case postgres.IsNoRows(err):
		return nil, fmt.Errorf("%w: slug %q", ErrNotFound, slug)
	case err != nil:
		return nil, fmt.Errorf("select pipeline by slug: %w", err)
	}
	return p, nil
}

func (r *pgRepository) List(ctx context.Context, q ListPipelinesQuery) (platform.PageResult[*Pipeline], error) {
	var (
		clauses = []string{"deleted_at IS NULL"}
		args    []any
	)

	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}

	if q.Status != "" {
		add("status = $%d", q.Status)
	}
	if q.OwnerID != "" {
		add("owner_id = $%d", q.OwnerID)
	}
	if q.DAGID != "" {
		add("dag_id = $%d", q.DAGID)
	}
	if q.Deployed != nil {
		add("dag_id IS NOT NULL = $%d", *q.Deployed)
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
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM pipelines`+where, args...).Scan(&total); err != nil {
		return platform.PageResult[*Pipeline]{}, fmt.Errorf("count pipelines: %w", err)
	}
	if total == 0 {
		return platform.NewPageResult([]*Pipeline{}, 0, q.Limit, q.Offset), nil
	}

	args = append(args, q.Limit, q.Offset)
	listQuery := fmt.Sprintf(
		`SELECT %s FROM pipelines%s ORDER BY %s LIMIT $%d OFFSET $%d`,
		pipelineColumns, where, orderBy, len(args)-1, len(args))

	rows, err := r.db.Query(ctx, listQuery, args...)
	if err != nil {
		return platform.PageResult[*Pipeline]{}, fmt.Errorf("list pipelines: %w", err)
	}
	defer rows.Close()

	items := make([]*Pipeline, 0, q.Limit)
	for rows.Next() {
		p, err := scanPipeline(rows)
		if err != nil {
			return platform.PageResult[*Pipeline]{}, fmt.Errorf("scan pipeline row: %w", err)
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return platform.PageResult[*Pipeline]{}, fmt.Errorf("iterate pipeline rows: %w", err)
	}

	return platform.NewPageResult(items, total, q.Limit, q.Offset), nil
}

func (r *pgRepository) Update(ctx context.Context, p *Pipeline) error {
	metadata, err := postgres.JSONB(p.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	// NULLIF keeps an empty dag_id or schedule as SQL NULL, which the unique
	// constraint needs: otherwise only one pipeline could have a NULL dag_id.
	const query = `
		UPDATE pipelines SET
			name = $1, slug = $2, description = $3,
			dag_id = NULLIF($4, ''), status = $5,
			schedule = NULLIF($6, ''), timezone = $7,
			max_active_runs = $8, catchup = $9, default_dataset_id = $10,
			owner_id = $11, tags = $12, task_count = $13, graph_hash = $14,
			metadata = $15, updated_by = $16
		WHERE id = $17 AND deleted_at IS NULL
		RETURNING updated_at`

	err = r.db.QueryRow(ctx, query,
		p.Name, p.Slug, p.Description, p.DAGID, string(p.Status),
		p.Schedule, p.Timezone, p.MaxActiveRuns, p.Catchup, p.DefaultDatasetID,
		p.OwnerID, p.Tags, p.TaskCount, p.GraphHash, metadata, p.UpdatedBy, p.ID,
	).Scan(&p.UpdatedAt)

	switch {
	case postgres.IsUniqueViolation(err, "pipelines_slug_unique"):
		return fmt.Errorf("%w: %q", ErrSlugTaken, p.Slug)
	case postgres.IsUniqueViolation(err, "pipelines_dag_id_unique"):
		return fmt.Errorf("%w: %q", ErrDAGIDTaken, p.DAGID)
	case postgres.IsNoRows(err):
		return fmt.Errorf("%w: id %s", ErrNotFound, p.ID)
	case err != nil:
		return fmt.Errorf("update pipeline: %w", err)
	}
	return nil
}

func (r *pgRepository) Delete(ctx context.Context, id uuid.UUID) error {
	const query = `
		UPDATE pipelines SET deleted_at = now(), updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING id`

	var deleted uuid.UUID
	err := r.db.QueryRow(ctx, query, id).Scan(&deleted)

	switch {
	case postgres.IsNoRows(err):
		return fmt.Errorf("%w: id %s", ErrNotFound, id)
	case err != nil:
		return fmt.Errorf("delete pipeline: %w", err)
	}
	return nil
}

func (r *pgRepository) Tasks(ctx context.Context, pipelineID uuid.UUID) ([]Task, error) {
	const query = `
		SELECT id, pipeline_id, task_key, name, operator_type, operator_config,
		       upstream_dataset_id, downstream_dataset_id,
		       max_retries, retry_delay_seconds, timeout_seconds, sla_minutes,
		       is_critical, enabled, created_at, updated_at
		FROM pipeline_tasks
		WHERE pipeline_id = $1
		ORDER BY task_key`

	rows, err := r.db.Query(ctx, query, pipelineID)
	if err != nil {
		return nil, fmt.Errorf("select pipeline tasks: %w", err)
	}
	defer rows.Close()

	out := make([]Task, 0, 16)
	for rows.Next() {
		var (
			t          Task
			configJSON []byte
		)
		if err := rows.Scan(
			&t.ID, &t.PipelineID, &t.TaskKey, &t.Name, &t.OperatorType, &configJSON,
			&t.UpstreamDatasetID, &t.DownstreamDatasetID,
			&t.MaxRetries, &t.RetryDelaySeconds, &t.TimeoutSeconds, &t.SLAMinutes,
			&t.IsCritical, &t.Enabled, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan pipeline task: %w", err)
		}
		t.OperatorConfig = jsonMap(configJSON)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pipeline task rows: %w", err)
	}
	return out, nil
}

func (r *pgRepository) Dependencies(ctx context.Context, pipelineID uuid.UUID) ([]Dependency, error) {
	const query = `
		SELECT upstream_key, downstream_key, created_at
		FROM pipeline_dependencies
		WHERE pipeline_id = $1
		ORDER BY upstream_key, downstream_key`

	rows, err := r.db.Query(ctx, query, pipelineID)
	if err != nil {
		return nil, fmt.Errorf("select pipeline dependencies: %w", err)
	}
	defer rows.Close()

	out := make([]Dependency, 0, 16)
	for rows.Next() {
		var d Dependency
		if err := rows.Scan(&d.UpstreamKey, &d.DownstreamKey, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan pipeline dependency: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pipeline dependency rows: %w", err)
	}
	return out, nil
}

// ReplaceGraph swaps tasks and edges in one transaction and updates the
// parent's denormalised task_count and graph_hash, keeping the two consistent.
func (r *pgRepository) ReplaceGraph(ctx context.Context, pipelineID uuid.UUID, tasks []Task, deps []Dependency, graphHash string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Edges reference tasks, so they must go first.
	const clearDeps = `DELETE FROM pipeline_dependencies WHERE pipeline_id = $1`
	if _, err := tx.Exec(ctx, clearDeps, pipelineID); err != nil {
		return fmt.Errorf("clear pipeline dependencies: %w", err)
	}

	const clearTasks = `DELETE FROM pipeline_tasks WHERE pipeline_id = $1`
	if _, err := tx.Exec(ctx, clearTasks, pipelineID); err != nil {
		return fmt.Errorf("clear pipeline tasks: %w", err)
	}

	if err := insertGraph(ctx, tx, pipelineID, tasks, deps); err != nil {
		return err
	}

	const updateParent = `
		UPDATE pipelines SET task_count = $1, graph_hash = $2
		WHERE id = $3 AND deleted_at IS NULL`

	if _, err := tx.Exec(ctx, updateParent, len(tasks), graphHash, pipelineID); err != nil {
		return fmt.Errorf("update pipeline graph hash: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit graph replacement: %w", err)
	}
	return nil
}

func (r *pgRepository) AllDAGIDs(ctx context.Context) (map[string]bool, error) {
	const query = `
		SELECT dag_id FROM pipelines
		WHERE dag_id IS NOT NULL AND deleted_at IS NULL`

	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("select pipeline dag ids: %w", err)
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan dag id: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dag id rows: %w", err)
	}
	return out, nil
}
