package quality

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

// Repository is the persistence contract for quality rules and their results.
type Repository interface {
	CreateRule(ctx context.Context, r *Rule) error
	GetRuleByID(ctx context.Context, id uuid.UUID) (*Rule, error)
	ListRules(ctx context.Context, q ListRulesQuery) (platform.PageResult[*Rule], error)
	UpdateRule(ctx context.Context, r *Rule) error
	SoftDeleteRule(ctx context.Context, id uuid.UUID, deletedAt time.Time) error

	RecordCheck(ctx context.Context, c *CheckRun) error
	ListChecks(ctx context.Context, q ListCheckRunsQuery) (platform.PageResult[*CheckRun], error)
	GetCheckByID(ctx context.Context, id uuid.UUID) (*CheckRun, error)
	LatestCheckForRule(ctx context.Context, ruleID uuid.UUID) (*CheckRun, error)

	// RuleHealth summarises recent rule performance for a dataset.
	RuleHealth(ctx context.Context, datasetID uuid.UUID) (*RuleHealth, error)
	// RulesForDataset returns the rules that gate a dataset, for the
	// execution domain's quality gate.
	EnabledRules(ctx context.Context, datasetID uuid.UUID) ([]Rule, error)
}

// pgRepository is the PostgreSQL implementation.
type pgRepository struct {
	db *pgxpool.Pool
}

var _ Repository = (*pgRepository)(nil)

// NewRepository builds a PostgreSQL-backed quality repository.
func NewRepository(db postgres.DB) Repository {
	return &pgRepository{db: db.Pool()}
}

// ruleColumns is the canonical SELECT list for rules.
const ruleColumns = `
	id, dataset_id, name, description,
	rule_type, expectation, expectation_params,
	severity, failure_threshold, dimension, target_column,
	enabled, blocking, pipeline_id,
	owner_id, tags, metadata,
	created_by, updated_by, created_at, updated_at, deleted_at`

func scanRule(row pgx.Row) (*Rule, error) {
	var (
		r            Rule
		ruleType     string
		severity     string
		dimension    string
		paramsJSON   []byte
		metadataJSON []byte
	)

	err := row.Scan(
		&r.ID, &r.DatasetID, &r.Name, &r.Description,
		&ruleType, &r.Expectation, &paramsJSON,
		&severity, &r.FailureThreshold, &dimension, &r.TargetColumn,
		&r.Enabled, &r.Blocking, &r.PipelineID,
		&r.OwnerID, &r.Tags, &metadataJSON,
		&r.CreatedBy, &r.UpdatedBy, &r.CreatedAt, &r.UpdatedAt, &r.DeletedAt,
	)
	if err != nil {
		return nil, err
	}

	r.RuleType = RuleType(ruleType)
	r.Severity = Severity(severity)
	r.Dimension = Dimension(dimension)
	r.ExpectationParams = decodeJSONMap(paramsJSON)
	r.Metadata = decodeJSONMap(metadataJSON)
	if r.Tags == nil {
		r.Tags = []string{}
	}
	return &r, nil
}

func (r *pgRepository) CreateRule(ctx context.Context, rule *Rule) error {
	params, err := postgres.JSONB(rule.ExpectationParams)
	if err != nil {
		return fmt.Errorf("encode expectation_params: %w", err)
	}
	metadata, err := postgres.JSONB(rule.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	const query = `
		INSERT INTO quality_rules (
			dataset_id, name, description,
			rule_type, expectation, expectation_params,
			severity, failure_threshold, dimension, target_column,
			enabled, blocking, pipeline_id,
			owner_id, tags, metadata,
			created_by, updated_by
		) VALUES (
			$1, $2, $3,
			$4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13,
			$14, $15, $16,
			$17, $18
		)
		RETURNING id, created_at, updated_at`

	err = r.db.QueryRow(ctx, query,
		rule.DatasetID, rule.Name, rule.Description,
		string(rule.RuleType), rule.Expectation, params,
		string(rule.Severity), rule.FailureThreshold, string(rule.Dimension), rule.TargetColumn,
		rule.Enabled, rule.Blocking, rule.PipelineID,
		rule.OwnerID, rule.Tags, metadata,
		rule.CreatedBy, rule.UpdatedBy,
	).Scan(&rule.ID, &rule.CreatedAt, &rule.UpdatedAt)

	if err != nil {
		return fmt.Errorf("insert quality rule: %w", err)
	}
	return nil
}

func (r *pgRepository) GetRuleByID(ctx context.Context, id uuid.UUID) (*Rule, error) {
	query := `SELECT ` + ruleColumns + ` FROM quality_rules WHERE id = $1 AND deleted_at IS NULL`

	rule, err := scanRule(r.db.QueryRow(ctx, query, id))
	if err != nil {
		return nil, fmt.Errorf("select quality rule: %w", err)
	}
	return rule, nil
}

func (r *pgRepository) ListRules(ctx context.Context, q ListRulesQuery) (platform.PageResult[*Rule], error) {
	var (
		clauses = []string{"deleted_at IS NULL"}
		args    []any
	)

	add := func(clause string, values ...any) {
		// Each value consumes exactly one placeholder, so the clause receives a
		// run of consecutive indices. Formatting with a single len(args) would
		// repeat the final index and leave the earlier placeholders unbound,
		// which pgx reports as a bind-count mismatch at runtime.
		start := len(args) + 1
		args = append(args, values...)

		indices := make([]any, len(values))
		for i := range values {
			indices[i] = start + i
		}
		clauses = append(clauses, fmt.Sprintf(clause, indices...))
	}
	addArray := func(clause string, value []string) {
		if len(value) == 0 {
			return
		}
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}

	if q.DatasetID != "" {
		id, err := uuid.Parse(q.DatasetID)
		if err != nil {
			return platform.PageResult[*Rule]{}, platform.NewBadRequest(
				"dataset_id must be a valid UUID")
		}
		add("dataset_id = $%d", id)
	}
	if q.PipelineID != "" {
		id, err := uuid.Parse(q.PipelineID)
		if err != nil {
			return platform.PageResult[*Rule]{}, platform.NewBadRequest(
				"pipeline_id must be a valid UUID")
		}
		add("pipeline_id = $%d", id)
	}
	if len(q.RuleType) > 0 {
		values := make([]string, 0, len(q.RuleType))
		for _, v := range q.RuleType {
			values = append(values, string(normaliseRuleType(v)))
		}
		addArray("rule_type = ANY($%d)", values)
	}
	if len(q.Severity) > 0 {
		values := make([]string, 0, len(q.Severity))
		for _, v := range q.Severity {
			values = append(values, string(normaliseSeverity(v)))
		}
		addArray("severity = ANY($%d)", values)
	}
	if len(q.Dimension) > 0 {
		values := make([]string, 0, len(q.Dimension))
		for _, v := range q.Dimension {
			values = append(values, string(normaliseDimension(v)))
		}
		addArray("dimension = ANY($%d)", values)
	}
	if q.Enabled != nil {
		add("enabled = $%d", *q.Enabled)
	}
	if q.Blocking != nil {
		add("blocking = $%d", *q.Blocking)
	}
	if q.Search != "" {
		pattern := "%" + q.Search + "%"
		add("(name ILIKE $%d OR description ILIKE $%d)", pattern, pattern)
	}
	addArray("tags @> $%d", q.Tag)

	where := " WHERE " + strings.Join(clauses, " AND ")
	orderColumn := map[string]string{
		"name": "name", "created_at": "created_at", "updated_at": "updated_at",
		"severity": "severity", "rule_type": "rule_type", "dimension": "dimension",
	}[q.Sort.By]
	if orderColumn == "" {
		orderColumn = "created_at"
	}

	var total int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM quality_rules`+where, args...).Scan(&total); err != nil {
		return platform.PageResult[*Rule]{}, fmt.Errorf("count quality rules: %w", err)
	}
	if total == 0 {
		return platform.NewPageResult([]*Rule{}, 0, q.Limit, q.Offset), nil
	}

	args = append(args, q.Limit, q.Offset)
	listQuery := fmt.Sprintf(
		`SELECT %s FROM quality_rules%s ORDER BY %s %s, id LIMIT $%d OFFSET $%d`,
		ruleColumns, where, orderColumn, q.Sort.Direction(), len(args)-1, len(args))

	rows, err := r.db.Query(ctx, listQuery, args...)
	if err != nil {
		return platform.PageResult[*Rule]{}, fmt.Errorf("list quality rules: %w", err)
	}
	defer rows.Close()

	items := make([]*Rule, 0, q.Limit)
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return platform.PageResult[*Rule]{}, fmt.Errorf("scan quality rule: %w", err)
		}
		items = append(items, rule)
	}
	if err := rows.Err(); err != nil {
		return platform.PageResult[*Rule]{}, fmt.Errorf("iterate quality rule rows: %w", err)
	}

	return platform.NewPageResult(items, total, q.Limit, q.Offset), nil
}

func (r *pgRepository) UpdateRule(ctx context.Context, rule *Rule) error {
	params, err := postgres.JSONB(rule.ExpectationParams)
	if err != nil {
		return fmt.Errorf("encode expectation_params: %w", err)
	}
	metadata, err := postgres.JSONB(rule.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	const query = `
		UPDATE quality_rules SET
			name = $2, description = $3,
			rule_type = $4, expectation = $5, expectation_params = $6,
			severity = $7, failure_threshold = $8, dimension = $9, target_column = $10,
			enabled = $11, blocking = $12, pipeline_id = $13,
			owner_id = $14, tags = $15, metadata = $16,
			updated_by = $17, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING updated_at`

	err = r.db.QueryRow(ctx, query,
		rule.ID, rule.Name, rule.Description,
		string(rule.RuleType), rule.Expectation, params,
		string(rule.Severity), rule.FailureThreshold, string(rule.Dimension), rule.TargetColumn,
		rule.Enabled, rule.Blocking, rule.PipelineID,
		rule.OwnerID, rule.Tags, metadata,
		rule.UpdatedBy,
	).Scan(&rule.UpdatedAt)

	if err != nil {
		return fmt.Errorf("update quality rule: %w", err)
	}
	return nil
}

// SoftDeleteRule archives a rule. Rules are never hard deleted because their
// evaluation history must remain attributable.
func (r *pgRepository) SoftDeleteRule(ctx context.Context, id uuid.UUID, deletedAt time.Time) error {
	const query = `
		UPDATE quality_rules SET deleted_at = $2, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`

	tag, err := r.db.Exec(ctx, query, id, deletedAt.UTC())
	if err != nil {
		return fmt.Errorf("soft delete quality rule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

const checkColumns = `
	id, rule_id, dataset_id, pipeline_run_id,
	status, observed_value, expected_value,
	rows_evaluated, violation_rate, passed_count, failed_count,
	message, details, duration_ms,
	evaluated_at, created_at`

func scanCheck(row pgx.Row) (*CheckRun, error) {
	var (
		c           CheckRun
		status      string
		detailsJSON []byte
	)

	err := row.Scan(
		&c.ID, &c.RuleID, &c.DatasetID, &c.PipelineRunID,
		&status, &c.ObservedValue, &c.ExpectedValue,
		&c.RowsEvaluated, &c.ViolationRate, &c.PassedCount, &c.FailedCount,
		&c.Message, &detailsJSON, &c.DurationMs,
		&c.EvaluatedAt, &c.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	c.Status = CheckStatus(status)
	c.Details = decodeJSONMap(detailsJSON)
	return &c, nil
}

func (r *pgRepository) RecordCheck(ctx context.Context, c *CheckRun) error {
	details, err := postgres.JSONB(c.Details)
	if err != nil {
		return fmt.Errorf("encode details: %w", err)
	}

	const query = `
		INSERT INTO quality_check_runs (
			rule_id, dataset_id, pipeline_run_id,
			status, observed_value, expected_value,
			rows_evaluated, violation_rate, passed_count, failed_count,
			message, details, duration_ms,
			evaluated_at
		) VALUES (
			$1, $2, $3,
			$4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13,
			$14
		)
		RETURNING id, created_at`

	err = r.db.QueryRow(ctx, query,
		c.RuleID, c.DatasetID, c.PipelineRunID,
		string(c.Status), c.ObservedValue, c.ExpectedValue,
		c.RowsEvaluated, c.ViolationRate, c.PassedCount, c.FailedCount,
		c.Message, details, c.DurationMs,
		c.EvaluatedAt,
	).Scan(&c.ID, &c.CreatedAt)

	if err != nil {
		return fmt.Errorf("insert quality check run: %w", err)
	}
	return nil
}

func (r *pgRepository) ListChecks(ctx context.Context, q ListCheckRunsQuery) (platform.PageResult[*CheckRun], error) {
	var (
		clauses = []string{"TRUE"}
		args    []any
	)

	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}

	if q.RuleID != "" {
		id, err := uuid.Parse(q.RuleID)
		if err != nil {
			return platform.PageResult[*CheckRun]{}, platform.NewBadRequest(
				"rule_id must be a valid UUID")
		}
		add("rule_id = $%d", id)
	}
	if q.DatasetID != "" {
		id, err := uuid.Parse(q.DatasetID)
		if err != nil {
			return platform.PageResult[*CheckRun]{}, platform.NewBadRequest(
				"dataset_id must be a valid UUID")
		}
		add("dataset_id = $%d", id)
	}
	if q.PipelineRunID != "" {
		id, err := uuid.Parse(q.PipelineRunID)
		if err != nil {
			return platform.PageResult[*CheckRun]{}, platform.NewBadRequest(
				"pipeline_run_id must be a valid UUID")
		}
		add("pipeline_run_id = $%d", id)
	}
	if len(q.Status) > 0 {
		values := make([]string, 0, len(q.Status))
		for _, v := range q.Status {
			values = append(values, string(normaliseCheckStatus(v)))
		}
		args = append(args, values)
		clauses = append(clauses, fmt.Sprintf("status = ANY($%d)", len(args)))
	}
	if q.Since != nil {
		add("evaluated_at >= $%d", q.Since.UTC())
	}
	if q.Until != nil {
		add("evaluated_at <= $%d", q.Until.UTC())
	}

	where := " WHERE " + strings.Join(clauses, " AND ")
	orderColumn := map[string]string{
		"evaluated_at": "evaluated_at", "created_at": "created_at", "status": "status",
	}[q.Sort.By]
	if orderColumn == "" {
		orderColumn = "evaluated_at"
	}

	var total int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM quality_check_runs`+where, args...).Scan(&total); err != nil {
		return platform.PageResult[*CheckRun]{}, fmt.Errorf("count quality check runs: %w", err)
	}
	if total == 0 {
		return platform.NewPageResult([]*CheckRun{}, 0, q.Limit, q.Offset), nil
	}

	args = append(args, q.Limit, q.Offset)
	listQuery := fmt.Sprintf(
		`SELECT %s FROM quality_check_runs%s ORDER BY %s %s, id LIMIT $%d OFFSET $%d`,
		checkColumns, where, orderColumn, q.Sort.Direction(), len(args)-1, len(args))

	rows, err := r.db.Query(ctx, listQuery, args...)
	if err != nil {
		return platform.PageResult[*CheckRun]{}, fmt.Errorf("list quality check runs: %w", err)
	}
	defer rows.Close()

	items := make([]*CheckRun, 0, q.Limit)
	for rows.Next() {
		check, err := scanCheck(rows)
		if err != nil {
			return platform.PageResult[*CheckRun]{}, fmt.Errorf("scan quality check run: %w", err)
		}
		items = append(items, check)
	}
	if err := rows.Err(); err != nil {
		return platform.PageResult[*CheckRun]{}, fmt.Errorf("iterate quality check run rows: %w", err)
	}

	return platform.NewPageResult(items, total, q.Limit, q.Offset), nil
}

func (r *pgRepository) GetCheckByID(ctx context.Context, id uuid.UUID) (*CheckRun, error) {
	query := `SELECT ` + checkColumns + ` FROM quality_check_runs WHERE id = $1`

	check, err := scanCheck(r.db.QueryRow(ctx, query, id))
	if err != nil {
		return nil, fmt.Errorf("select quality check run: %w", err)
	}
	return check, nil
}

// LatestCheckForRule returns the most recent evaluation of a rule, which is
// what a dashboard shows as the rule's current state.
func (r *pgRepository) LatestCheckForRule(ctx context.Context, ruleID uuid.UUID) (*CheckRun, error) {
	query := `SELECT ` + checkColumns + `
		FROM quality_check_runs
		WHERE rule_id = $1
		ORDER BY evaluated_at DESC, created_at DESC
		LIMIT 1`

	check, err := scanCheck(r.db.QueryRow(ctx, query, ruleID))
	if err != nil {
		return nil, fmt.Errorf("select latest quality check run: %w", err)
	}
	return check, nil
}

func (r *pgRepository) RuleHealth(ctx context.Context, datasetID uuid.UUID) (*RuleHealth, error) {
	const query = `
		SELECT
			count(*) FILTER (WHERE deleted_at IS NULL)::int,
			count(*) FILTER (WHERE deleted_at IS NULL AND enabled)::int,
			count(*) FILTER (WHERE deleted_at IS NULL AND blocking)::int
		FROM quality_rules
		WHERE dataset_id = $1`

	health := &RuleHealth{DatasetID: datasetID}
	if err := r.db.QueryRow(ctx, query, datasetID).
		Scan(&health.TotalRules, &health.EnabledRules, &health.BlockingRules); err != nil {
		return nil, fmt.Errorf("count dataset quality rules: %w", err)
	}

	// Judge each rule by its most recent evaluation.
	const verdicts = `
		SELECT DISTINCT ON (r.id)
			r.severity,
			c.status
		FROM quality_rules r
		LEFT JOIN LATERAL (
			SELECT status
			FROM quality_check_runs
			WHERE rule_id = r.id
			ORDER BY evaluated_at DESC, created_at DESC
			LIMIT 1
		) c ON TRUE
		WHERE r.dataset_id = $1 AND r.deleted_at IS NULL
		ORDER BY r.id`

	rows, err := r.db.Query(ctx, verdicts, datasetID)
	if err != nil {
		return nil, fmt.Errorf("select dataset rule verdicts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			severity string
			status   *string
		)
		if err := rows.Scan(&severity, &status); err != nil {
			return nil, fmt.Errorf("scan rule verdict: %w", err)
		}

		if status == nil {
			health.NotEvaluated++
			continue
		}

		switch CheckStatus(*status) {
		case CheckPassed, CheckWarning:
			health.Passing++
		case CheckFailed, CheckError:
			health.Failing++
			// Keep the worst severity among failures.
			if Severity(severity).rank() > health.WorstSeverity.rank() {
				health.WorstSeverity = Severity(severity)
			}
		case CheckSkipped:
			// A skipped check neither passes nor fails; it is not evidence of
			// health, so it is excluded from the rate.
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rule verdicts: %w", err)
	}

	evaluated := health.Passing + health.Failing
	if evaluated > 0 {
		rate := float64(health.Passing) / float64(evaluated)
		health.PassedRate = &rate
	}

	return health, nil
}

func (r *pgRepository) EnabledRules(ctx context.Context, datasetID uuid.UUID) ([]Rule, error) {
	query := `SELECT ` + ruleColumns + `
		FROM quality_rules
		WHERE dataset_id = $1 AND enabled AND deleted_at IS NULL
		ORDER BY severity, name`

	rows, err := r.db.Query(ctx, query, datasetID)
	if err != nil {
		return nil, fmt.Errorf("list enabled quality rules: %w", err)
	}
	defer rows.Close()

	out := []Rule{}
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan quality rule: %w", err)
		}
		out = append(out, *rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate quality rule rows: %w", err)
	}
	return out, nil
}
