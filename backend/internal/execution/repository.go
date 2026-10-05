package execution

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

// Repository is the persistence contract for runs.
type Repository interface {
	CreateRun(ctx context.Context, r *Run) error
	GetRunByID(ctx context.Context, id uuid.UUID) (*Run, error)
	// GetRunByRunID looks a run up by its stable idempotency key.
	GetRunByRunID(ctx context.Context, runID uuid.UUID) (*Run, error)
	ListRuns(ctx context.Context, q ListRunsQuery) (platform.PageResult[*Run], error)
	UpdateRun(ctx context.Context, r *Run) error
	// CountActiveRuns counts runs still in flight, enforcing max_active_runs.
	CountActiveRuns(ctx context.Context, pipelineID uuid.UUID) (int, error)

	UpsertTaskRun(ctx context.Context, t *TaskRun) error
	GetTaskRunByID(ctx context.Context, id uuid.UUID) (*TaskRun, error)
	ListTaskRuns(ctx context.Context, q ListTaskRunsQuery) (platform.PageResult[*TaskRun], error)
	RecountRunTasks(ctx context.Context, pipelineRunID uuid.UUID) error
	// StaleRuns finds runs that still look active long after Airflow finished
	// with them, which happens when a callback is missed.
	StaleRuns(ctx context.Context, olderThan time.Time) ([]Run, error)
}

// pgRepository is the PostgreSQL implementation.
type pgRepository struct {
	db *pgxpool.Pool
}

var _ Repository = (*pgRepository)(nil)

// NewRepository builds a PostgreSQL-backed execution repository.
func NewRepository(db postgres.DB) Repository {
	return &pgRepository{db: db.Pool()}
}

const runColumns = `
	id, pipeline_id, run_id, dag_run_id,
	status, trigger_type, triggered_by,
	run_config, params,
	logical_date, queued_at, started_at, finished_at, duration_ms,
	task_count, succeeded_count, failed_count, skipped_count,
	quality_gate_passed, error_message, logs_url,
	metadata, created_at, updated_at`

func scanRun(row pgx.Row) (*Run, error) {
	var (
		r             Run
		status        string
		triggerType   string
		runConfigJSON []byte
		paramsJSON    []byte
		metadataJSON  []byte
	)

	err := row.Scan(
		&r.ID, &r.PipelineID, &r.RunID, &r.DAGRunID,
		&status, &triggerType, &r.TriggeredBy,
		&runConfigJSON, &paramsJSON,
		&r.LogicalDate, &r.QueuedAt, &r.StartedAt, &r.FinishedAt, &r.DurationMs,
		&r.TaskCount, &r.SucceededCount, &r.FailedCount, &r.SkippedCount,
		&r.QualityGatePassed, &r.ErrorMessage, &r.LogsURL,
		&metadataJSON, &r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	r.Status = RunStatus(status)
	r.TriggerType = TriggerType(triggerType)
	r.RunConfig = decodeJSONMap(runConfigJSON)
	r.Params = decodeJSONMap(paramsJSON)
	r.Metadata = decodeJSONMap(metadataJSON)
	return &r, nil
}

func (r *pgRepository) CreateRun(ctx context.Context, run *Run) error {
	runConfig, err := postgres.JSONB(run.RunConfig)
	if err != nil {
		return fmt.Errorf("encode run_config: %w", err)
	}
	params, err := postgres.JSONB(run.Params)
	if err != nil {
		return fmt.Errorf("encode params: %w", err)
	}
	metadata, err := postgres.JSONB(run.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	const query = `
		INSERT INTO pipeline_runs (
			pipeline_id, run_id, status, trigger_type, triggered_by,
			run_config, params, logical_date,
			task_count, metadata
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8,
			$9, $10
		)
		RETURNING id, queued_at, created_at, updated_at`

	err = r.db.QueryRow(ctx, query,
		run.PipelineID, run.RunID, string(run.Status), string(run.TriggerType), run.TriggeredBy,
		runConfig, params, run.LogicalDate,
		run.TaskCount, metadata,
	).Scan(&run.ID, &run.QueuedAt, &run.CreatedAt, &run.UpdatedAt)

	if err != nil {
		return fmt.Errorf("insert pipeline run: %w", err)
	}
	return nil
}

func (r *pgRepository) GetRunByID(ctx context.Context, id uuid.UUID) (*Run, error) {
	query := `SELECT ` + runColumns + ` FROM pipeline_runs WHERE id = $1`

	run, err := scanRun(r.db.QueryRow(ctx, query, id))
	if err != nil {
		return nil, fmt.Errorf("select pipeline run: %w", err)
	}
	return run, nil
}

func (r *pgRepository) GetRunByRunID(ctx context.Context, runID uuid.UUID) (*Run, error) {
	query := `SELECT ` + runColumns + ` FROM pipeline_runs WHERE run_id = $1`

	run, err := scanRun(r.db.QueryRow(ctx, query, runID))
	if err != nil {
		return nil, fmt.Errorf("select pipeline run by run_id: %w", err)
	}
	return run, nil
}

func (r *pgRepository) ListRuns(ctx context.Context, q ListRunsQuery) (platform.PageResult[*Run], error) {
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

	if q.PipelineID != "" {
		id, err := uuid.Parse(q.PipelineID)
		if err != nil {
			return platform.PageResult[*Run]{}, platform.NewBadRequest(
				"pipeline_id must be a valid UUID")
		}
		add("pipeline_id = $%d", id)
	}
	if q.RunID != "" {
		id, err := uuid.Parse(q.RunID)
		if err != nil {
			return platform.PageResult[*Run]{}, platform.NewBadRequest(
				"run_id must be a valid UUID")
		}
		add("run_id = $%d", id)
	}
	if len(q.Status) > 0 {
		values := make([]string, 0, len(q.Status))
		for _, v := range q.Status {
			values = append(values, string(normaliseRunStatus(v)))
		}
		args = append(args, values)
		clauses = append(clauses, fmt.Sprintf("status = ANY($%d)", len(args)))
	}
	if q.TriggerType != "" {
		add("trigger_type = $%d", string(normaliseTriggerType(q.TriggerType)))
	}
	if q.ActiveOnly {
		add("status IN ('queued', 'running')")
	}
	if q.Since != nil {
		add("created_at >= $%d", q.Since.UTC())
	}
	if q.Until != nil {
		add("created_at <= $%d", q.Until.UTC())
	}

	where := " WHERE " + strings.Join(clauses, " AND ")
	orderColumn := map[string]string{
		"created_at": "created_at", "queued_at": "queued_at",
		"started_at": "started_at", "finished_at": "finished_at",
		"status": "status", "duration_ms": "duration_ms",
	}[q.Sort.By]
	if orderColumn == "" {
		orderColumn = "created_at"
	}

	var total int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM pipeline_runs`+where, args...).Scan(&total); err != nil {
		return platform.PageResult[*Run]{}, fmt.Errorf("count pipeline runs: %w", err)
	}
	if total == 0 {
		return platform.NewPageResult([]*Run{}, 0, q.Limit, q.Offset), nil
	}

	args = append(args, q.Limit, q.Offset)
	listQuery := fmt.Sprintf(
		`SELECT %s FROM pipeline_runs%s ORDER BY %s %s, id LIMIT $%d OFFSET $%d`,
		runColumns, where, orderColumn, q.Sort.Direction(), len(args)-1, len(args))

	rows, err := r.db.Query(ctx, listQuery, args...)
	if err != nil {
		return platform.PageResult[*Run]{}, fmt.Errorf("list pipeline runs: %w", err)
	}
	defer rows.Close()

	items := make([]*Run, 0, q.Limit)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return platform.PageResult[*Run]{}, fmt.Errorf("scan pipeline run: %w", err)
		}
		items = append(items, run)
	}
	if err := rows.Err(); err != nil {
		return platform.PageResult[*Run]{}, fmt.Errorf("iterate pipeline run rows: %w", err)
	}

	return platform.NewPageResult(items, total, q.Limit, q.Offset), nil
}

func (r *pgRepository) UpdateRun(ctx context.Context, run *Run) error {
	runConfig, err := postgres.JSONB(run.RunConfig)
	if err != nil {
		return fmt.Errorf("encode run_config: %w", err)
	}
	params, err := postgres.JSONB(run.Params)
	if err != nil {
		return fmt.Errorf("encode params: %w", err)
	}
	metadata, err := postgres.JSONB(run.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	const query = `
		UPDATE pipeline_runs SET
			dag_run_id = $2, status = $3, triggered_by = $4,
			run_config = $5, params = $6,
			started_at = $7, finished_at = $8, duration_ms = $9,
			task_count = $10, succeeded_count = $11, failed_count = $12,
			skipped_count = $13, quality_gate_passed = $14,
			error_message = $15, logs_url = $16, metadata = $17,
			updated_at = now()
		WHERE id = $1
		RETURNING updated_at`

	err = r.db.QueryRow(ctx, query,
		run.ID, run.DAGRunID, string(run.Status), run.TriggeredBy,
		runConfig, params,
		run.StartedAt, run.FinishedAt, run.DurationMs,
		run.TaskCount, run.SucceededCount, run.FailedCount, run.SkippedCount,
		run.QualityGatePassed, run.ErrorMessage, run.LogsURL, metadata,
	).Scan(&run.UpdatedAt)

	if err != nil {
		return fmt.Errorf("update pipeline run: %w", err)
	}
	return nil
}

func (r *pgRepository) CountActiveRuns(ctx context.Context, pipelineID uuid.UUID) (int, error) {
	const query = `
		SELECT count(*) FROM pipeline_runs
		WHERE pipeline_id = $1 AND status IN ('queued', 'running')`

	var n int
	if err := r.db.QueryRow(ctx, query, pipelineID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active pipeline runs: %w", err)
	}
	return n, nil
}

const taskRunColumns = `
	id, pipeline_run_id, task_id, task_key, try_number,
	status, operator_type,
	attempt_started_at, finished_at, duration_ms, exit_code,
	operator_output, error_message, logs_url,
	created_at, updated_at`

func scanTaskRun(row pgx.Row) (*TaskRun, error) {
	var (
		t      TaskRun
		status string
	)

	err := row.Scan(
		&t.ID, &t.PipelineRunID, &t.TaskID, &t.TaskKey, &t.TryNumber,
		&status, &t.OperatorType,
		&t.AttemptStartedAt, &t.FinishedAt, &t.DurationMs, &t.ExitCode,
		&t.OperatorOutput, &t.ErrorMessage, &t.LogsURL,
		&t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	t.Status = TaskStatus(status)
	return &t, nil
}

// UpsertTaskRun records a task attempt.
//
// Airflow reports the same attempt more than once (state callbacks, retries
// after a lost callback), so the attempt is upserted on its natural key rather
// than inserted, which would otherwise fail on the second report.
func (r *pgRepository) UpsertTaskRun(ctx context.Context, t *TaskRun) error {
	const query = `
		INSERT INTO task_runs (
			pipeline_run_id, task_id, task_key, try_number,
			status, operator_type,
			attempt_started_at, finished_at, duration_ms, exit_code,
			operator_output, error_message, logs_url
		) VALUES (
			$1, $2, $3, $4,
			$5, $6,
			$7, $8, $9, $10,
			$11, $12, $13
		)
		ON CONFLICT (pipeline_run_id, task_key, try_number) DO UPDATE SET
			task_id = COALESCE(EXCLUDED.task_id, task_runs.task_id),
			status = EXCLUDED.status,
			operator_type = EXCLUDED.operator_type,
			attempt_started_at = EXCLUDED.attempt_started_at,
			finished_at = EXCLUDED.finished_at,
			duration_ms = EXCLUDED.duration_ms,
			exit_code = EXCLUDED.exit_code,
			operator_output = EXCLUDED.operator_output,
			error_message = EXCLUDED.error_message,
			logs_url = EXCLUDED.logs_url,
			updated_at = now()
		RETURNING id, created_at, updated_at`

	err := r.db.QueryRow(ctx, query,
		t.PipelineRunID, t.TaskID, t.TaskKey, t.TryNumber,
		string(t.Status), t.OperatorType,
		t.AttemptStartedAt, t.FinishedAt, t.DurationMs, t.ExitCode,
		t.OperatorOutput, t.ErrorMessage, t.LogsURL,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)

	if err != nil {
		return fmt.Errorf("upsert task run: %w", err)
	}
	return nil
}

func (r *pgRepository) GetTaskRunByID(ctx context.Context, id uuid.UUID) (*TaskRun, error) {
	query := `SELECT ` + taskRunColumns + ` FROM task_runs WHERE id = $1`

	task, err := scanTaskRun(r.db.QueryRow(ctx, query, id))
	if err != nil {
		return nil, fmt.Errorf("select task run: %w", err)
	}
	return task, nil
}

func (r *pgRepository) ListTaskRuns(ctx context.Context, q ListTaskRunsQuery) (platform.PageResult[*TaskRun], error) {
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

	if q.PipelineRunID != "" {
		id, err := uuid.Parse(q.PipelineRunID)
		if err != nil {
			return platform.PageResult[*TaskRun]{}, platform.NewBadRequest(
				"pipeline_run_id must be a valid UUID")
		}
		add("pipeline_run_id = $%d", id)
	}
	if q.TaskKey != "" {
		add("task_key = $%d", q.TaskKey)
	}
	if len(q.Status) > 0 {
		values := make([]string, 0, len(q.Status))
		for _, v := range q.Status {
			values = append(values, string(normaliseTaskStatus(v)))
		}
		args = append(args, values)
		clauses = append(clauses, fmt.Sprintf("status = ANY($%d)", len(args)))
	}

	where := " WHERE " + strings.Join(clauses, " AND ")
	orderColumn := map[string]string{
		"created_at": "created_at", "finished_at": "finished_at",
		"status": "status", "try_number": "try_number",
		"attempt_started_at": "attempt_started_at",
	}[q.Sort.By]
	if orderColumn == "" {
		orderColumn = "created_at"
	}
	direction := q.Sort.Direction()

	if q.LatestAttemptOnly {
		// DISTINCT ON collapses a retried task to its final attempt, ordered so
		// the highest try_number is picked.
		direction = "DESC"
		listArgs := append([]any{}, args...)
		args = append(listArgs, q.Limit, q.Offset)
		listQuery := fmt.Sprintf(
			`SELECT DISTINCT ON (task_key) %s FROM task_runs%s ORDER BY task_key, try_number %s
			 LIMIT $%d OFFSET $%d`,
			taskRunColumns, where, direction, len(args)-1, len(args))

		return r.pageTaskRuns(ctx, listQuery, args, q.Limit, q.Offset, where, listArgs)
	}

	args = append(args, q.Limit, q.Offset)
	listQuery := fmt.Sprintf(
		`SELECT %s FROM task_runs%s ORDER BY %s %s, try_number LIMIT $%d OFFSET $%d`,
		taskRunColumns, where, orderColumn, direction, len(args)-1, len(args))

	return r.pageTaskRuns(ctx, listQuery, args, q.Limit, q.Offset, where, args[:len(args)-2])
}

// pageTaskRuns runs the list query and its matching count, then wraps the rows.
func (r *pgRepository) pageTaskRuns(ctx context.Context, listQuery string, args []any, limit, offset int, where string, countArgs []any) (platform.PageResult[*TaskRun], error) {
	var total int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM task_runs`+where, countArgs...).Scan(&total); err != nil {
		return platform.PageResult[*TaskRun]{}, fmt.Errorf("count task runs: %w", err)
	}
	if total == 0 {
		return platform.NewPageResult([]*TaskRun{}, 0, limit, offset), nil
	}

	rows, err := r.db.Query(ctx, listQuery, args...)
	if err != nil {
		return platform.PageResult[*TaskRun]{}, fmt.Errorf("list task runs: %w", err)
	}
	defer rows.Close()

	items := make([]*TaskRun, 0, limit)
	for rows.Next() {
		task, err := scanTaskRun(rows)
		if err != nil {
			return platform.PageResult[*TaskRun]{}, fmt.Errorf("scan task run: %w", err)
		}
		items = append(items, task)
	}
	if err := rows.Err(); err != nil {
		return platform.PageResult[*TaskRun]{}, fmt.Errorf("iterate task run rows: %w", err)
	}

	return platform.NewPageResult(items, total, limit, offset), nil
}

// RecountRunTasks recomputes a run's task counters from its attempts, so the
// summary cannot drift from the per-task records.
func (r *pgRepository) RecountRunTasks(ctx context.Context, pipelineRunID uuid.UUID) error {
	const query = `
		WITH counts AS (
			SELECT
				count(*) FILTER (WHERE tr.status = 'success')::int AS succeeded,
				count(*) FILTER (WHERE tr.status IN ('failed', 'upstream_failed'))::int AS failed,
				count(*) FILTER (WHERE tr.status = 'skipped')::int AS skipped,
				count(DISTINCT tr.task_key)::int AS total
			FROM task_runs tr
			WHERE tr.pipeline_run_id = $1
			  AND tr.try_number = (
				SELECT max(inner_tr.try_number) FROM task_runs inner_tr
				WHERE inner_tr.pipeline_run_id = tr.pipeline_run_id
				  AND inner_tr.task_key = tr.task_key
			  )
		)
		UPDATE pipeline_runs pr
		SET succeeded_count = counts.succeeded,
			failed_count = counts.failed,
			skipped_count = counts.skipped,
			updated_at = now()
		FROM counts
		WHERE pr.id = $1`

	if _, err := r.db.Exec(ctx, query, pipelineRunID); err != nil {
		return fmt.Errorf("recount pipeline run tasks: %w", err)
	}
	return nil
}

// StaleRuns finds runs that have been active too long, which usually means a
// status callback was lost and the run needs reconciling.
func (r *pgRepository) StaleRuns(ctx context.Context, olderThan time.Time) ([]Run, error) {
	query := `SELECT ` + runColumns + `
		FROM pipeline_runs
		WHERE status IN ('queued', 'running')
		  AND COALESCE(started_at, queued_at) < $1
		ORDER BY COALESCE(started_at, queued_at) ASC
		LIMIT 1000`

	rows, err := r.db.Query(ctx, query, olderThan.UTC())
	if err != nil {
		return nil, fmt.Errorf("select stale pipeline runs: %w", err)
	}
	defer rows.Close()

	out := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pipeline run: %w", err)
		}
		out = append(out, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pipeline run rows: %w", err)
	}
	return out, nil
}
