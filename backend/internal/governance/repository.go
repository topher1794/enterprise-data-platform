package governance

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

// Repository is the persistence contract for governance.
type Repository interface {
	CreatePolicy(ctx context.Context, p *Policy) error
	GetPolicyByID(ctx context.Context, id uuid.UUID) (*Policy, error)
	GetPolicyBySlug(ctx context.Context, slug string) (*Policy, error)
	ListPolicies(ctx context.Context, q ListPoliciesQuery) (platform.PageResult[*Policy], error)
	UpdatePolicy(ctx context.Context, p *Policy) error
	SoftDeletePolicy(ctx context.Context, id uuid.UUID, deletedAt time.Time) error

	// ListEffectivePolicies returns the enabled policies that apply to a
	// resource, in ascending priority order, filtered by classification floor.
	ListEffectivePolicies(ctx context.Context, q EvaluateQuery) ([]Policy, error)
	// SlugExists reports whether a slug is taken, optionally ignoring one id so
	// an update that does not change the slug does not conflict with itself.
	SlugExists(ctx context.Context, slug string, exceptID uuid.UUID) (bool, error)

	CreateContract(ctx context.Context, c *Contract) error
	GetContractByID(ctx context.Context, id uuid.UUID) (*Contract, error)
	ListContracts(ctx context.Context, q ListContractsQuery) (platform.PageResult[*Contract], error)
	UpdateContract(ctx context.Context, c *Contract) error
	SoftDeleteContract(ctx context.Context, id uuid.UUID, deletedAt time.Time) error
}

// EvaluateQuery identifies the resource whose policies are being resolved.
type EvaluateQuery struct {
	ResourceType ResourceType `json:"resource_type"`
	ResourceID   uuid.UUID    `json:"resource_id"`
	// Classification is the sensitivity of the resource being accessed; it is
	// compared against each policy's applies_to_classification floor.
	Classification Classification `json:"classification"`
	// PolicyType narrows to one kind of policy. Empty means all types.
	PolicyType PolicyType `json:"policy_type"`
}

// pgRepository is the PostgreSQL implementation.
type pgRepository struct {
	db *pgxpool.Pool
}

var _ Repository = (*pgRepository)(nil)

// NewRepository builds a PostgreSQL-backed governance repository.
func NewRepository(db postgres.DB) Repository {
	return &pgRepository{db: db.Pool()}
}

const policyColumns = `
	id, name, slug, description,
	policy_type, effect, rule_expression,
	applies_to_classification, resource_type, resource_id,
	remediation, retention_days, priority, enabled,
	owner_id, metadata,
	created_by, updated_by, created_at, updated_at, deleted_at`

func scanPolicy(row pgx.Row) (*Policy, error) {
	var (
		p              Policy
		policyType     string
		effect         string
		classification string
		resourceType   string
		remediation    string
		metadataJSON   []byte
	)

	err := row.Scan(
		&p.ID, &p.Name, &p.Slug, &p.Description,
		&policyType, &effect, &p.RuleExpression,
		&classification, &resourceType, &p.ResourceID,
		&remediation, &p.RetentionDays, &p.Priority, &p.Enabled,
		&p.OwnerID, &metadataJSON,
		&p.CreatedBy, &p.UpdatedBy, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt,
	)
	if err != nil {
		return nil, err
	}

	p.PolicyType = PolicyType(policyType)
	p.Effect = Effect(effect)
	p.AppliesToClassification = Classification(classification)
	p.ResourceType = ResourceType(resourceType)
	p.Remediation = Remediation(remediation)
	p.Metadata = decodeJSONMap(metadataJSON)
	return &p, nil
}

func (r *pgRepository) CreatePolicy(ctx context.Context, p *Policy) error {
	metadata, err := postgres.JSONB(p.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	const query = `
		INSERT INTO governance_policies (
			name, slug, description,
			policy_type, effect, rule_expression,
			applies_to_classification, resource_type, resource_id,
			remediation, retention_days, priority, enabled,
			owner_id, metadata,
			created_by, updated_by
		) VALUES (
			$1, $2, $3,
			$4, $5, $6,
			$7, $8, $9,
			$10, $11, $12, $13,
			$14, $15,
			$16, $17
		)
		RETURNING id, created_at, updated_at`

	err = r.db.QueryRow(ctx, query,
		p.Name, p.Slug, p.Description,
		string(p.PolicyType), string(p.Effect), p.RuleExpression,
		string(p.AppliesToClassification), string(p.ResourceType), p.ResourceID,
		string(p.Remediation), p.RetentionDays, p.Priority, p.Enabled,
		p.OwnerID, metadata,
		p.CreatedBy, p.UpdatedBy,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)

	if err != nil {
		return fmt.Errorf("insert policy: %w", err)
	}
	return nil
}

func (r *pgRepository) GetPolicyByID(ctx context.Context, id uuid.UUID) (*Policy, error) {
	query := `SELECT ` + policyColumns + ` FROM governance_policies WHERE id = $1 AND deleted_at IS NULL`

	policy, err := scanPolicy(r.db.QueryRow(ctx, query, id))
	if err != nil {
		return nil, fmt.Errorf("select policy: %w", err)
	}
	return policy, nil
}

func (r *pgRepository) GetPolicyBySlug(ctx context.Context, slug string) (*Policy, error) {
	query := `SELECT ` + policyColumns + ` FROM governance_policies WHERE slug = $1 AND deleted_at IS NULL`

	policy, err := scanPolicy(r.db.QueryRow(ctx, query, slug))
	if err != nil {
		return nil, fmt.Errorf("select policy by slug: %w", err)
	}
	return policy, nil
}

func (r *pgRepository) ListPolicies(ctx context.Context, q ListPoliciesQuery) (platform.PageResult[*Policy], error) {
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

	if q.PolicyType != "" {
		add("policy_type = $%d", string(normalisePolicyType(q.PolicyType)))
	}
	if q.ResourceType != "" {
		add("resource_type = $%d", string(normaliseResourceType(q.ResourceType)))
	}
	if q.ResourceID != "" {
		id, err := uuid.Parse(q.ResourceID)
		if err != nil {
			return platform.PageResult[*Policy]{}, platform.NewBadRequest(
				"resource_id must be a valid UUID")
		}
		add("resource_id = $%d", id)
	}
	if q.Effect != "" {
		add("effect = $%d", string(normaliseEffect(q.Effect)))
	}
	if q.Enabled != nil {
		add("enabled = $%d", *q.Enabled)
	}
	if q.Search != "" {
		pattern := "%" + q.Search + "%"
		add("(name ILIKE $%d OR description ILIKE $%d)", pattern, pattern)
	}

	where := " WHERE " + strings.Join(clauses, " AND ")
	orderColumn := map[string]string{
		"name": "name", "slug": "slug", "priority": "priority",
		"created_at": "created_at", "updated_at": "updated_at",
	}[q.Sort.By]
	if orderColumn == "" {
		orderColumn = "priority"
	}

	var total int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM governance_policies`+where, args...).Scan(&total); err != nil {
		return platform.PageResult[*Policy]{}, fmt.Errorf("count policies: %w", err)
	}
	if total == 0 {
		return platform.NewPageResult([]*Policy{}, 0, q.Limit, q.Offset), nil
	}

	args = append(args, q.Limit, q.Offset)
	listQuery := fmt.Sprintf(
		`SELECT %s FROM governance_policies%s ORDER BY %s %s, name LIMIT $%d OFFSET $%d`,
		policyColumns, where, orderColumn, q.Sort.Direction(), len(args)-1, len(args))

	rows, err := r.db.Query(ctx, listQuery, args...)
	if err != nil {
		return platform.PageResult[*Policy]{}, fmt.Errorf("list policies: %w", err)
	}
	defer rows.Close()

	items := make([]*Policy, 0, q.Limit)
	for rows.Next() {
		policy, err := scanPolicy(rows)
		if err != nil {
			return platform.PageResult[*Policy]{}, fmt.Errorf("scan policy: %w", err)
		}
		items = append(items, policy)
	}
	if err := rows.Err(); err != nil {
		return platform.PageResult[*Policy]{}, fmt.Errorf("iterate policy rows: %w", err)
	}

	return platform.NewPageResult(items, total, q.Limit, q.Offset), nil
}

func (r *pgRepository) UpdatePolicy(ctx context.Context, p *Policy) error {
	metadata, err := postgres.JSONB(p.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	const query = `
		UPDATE governance_policies SET
			name = $2, description = $3,
			policy_type = $4, effect = $5, rule_expression = $6,
			applies_to_classification = $7, resource_type = $8, resource_id = $9,
			remediation = $10, retention_days = $11, priority = $12, enabled = $13,
			owner_id = $14, metadata = $15,
			updated_by = $16, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING updated_at`

	err = r.db.QueryRow(ctx, query,
		p.ID, p.Name, p.Description,
		string(p.PolicyType), string(p.Effect), p.RuleExpression,
		string(p.AppliesToClassification), string(p.ResourceType), p.ResourceID,
		string(p.Remediation), p.RetentionDays, p.Priority, p.Enabled,
		p.OwnerID, metadata, p.UpdatedBy,
	).Scan(&p.UpdatedAt)

	if err != nil {
		return fmt.Errorf("update policy: %w", err)
	}
	return nil
}

func (r *pgRepository) SoftDeletePolicy(ctx context.Context, id uuid.UUID, deletedAt time.Time) error {
	const query = `
		UPDATE governance_policies SET deleted_at = $2, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`

	tag, err := r.db.Exec(ctx, query, id, deletedAt.UTC())
	if err != nil {
		return fmt.Errorf("soft delete policy: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ListEffectivePolicies returns policies applying to a resource.
//
// Two policies match: those scoped to this exact resource, and those scoped to
// the whole resource type with no specific id. The classification floor is
// compared in Go rather than SQL because the ordering of the four levels is
// business logic, not a collation.
func (r *pgRepository) ListEffectivePolicies(ctx context.Context, q EvaluateQuery) ([]Policy, error) {
	var (
		clauses = []string{"deleted_at IS NULL", "enabled"}
		args    []any
	)

	add := func(clause string, values ...any) {
		start := len(args) + 1
		args = append(args, values...)

		indices := make([]any, len(values))
		for i := range values {
			indices[i] = start + i
		}
		clauses = append(clauses, fmt.Sprintf(clause, indices...))
	}

	if q.ResourceType != "" {
		add("resource_type = $%d", string(q.ResourceType))
	}
	if q.ResourceID != uuid.Nil {
		add("(resource_id = $%d OR resource_id IS NULL)", q.ResourceID)
	}
	if q.PolicyType != "" {
		add("policy_type = $%d", string(q.PolicyType))
	}

	query := `SELECT ` + policyColumns + `
		FROM governance_policies
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY priority ASC, name ASC`

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list effective policies: %w", err)
	}
	defer rows.Close()

	out := []Policy{}
	for rows.Next() {
		policy, err := scanPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("scan policy: %w", err)
		}
		// Apply the sensitivity floor here, where the ordering is known.
		if q.Classification != "" && !q.Classification.AtLeast(policy.AppliesToClassification) {
			continue
		}
		out = append(out, *policy)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate policy rows: %w", err)
	}
	return out, nil
}

func (r *pgRepository) SlugExists(ctx context.Context, slug string, exceptID uuid.UUID) (bool, error) {
	const query = `
		SELECT EXISTS (
			SELECT 1 FROM governance_policies
			WHERE slug = $1 AND deleted_at IS NULL AND ($2::uuid IS NULL OR id <> $2)
		)`

	var exists bool
	err := r.db.QueryRow(ctx, query, slug, nullIfNil(exceptID)).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check policy slug: %w", err)
	}
	return exists, nil
}

const contractColumns = `
	id, dataset_id, name, version, status,
	schema_definition, freshness_sla_minutes, min_row_count, max_null_rate,
	consumer_teams, breaking_change_policy,
	signed_at, signed_by,
	owner_id, metadata,
	created_by, updated_by, created_at, updated_at, deleted_at`

func scanContract(row pgx.Row) (*Contract, error) {
	var (
		c            Contract
		status       string
		schemaJSON   []byte
		metadataJSON []byte
		breaking     string
	)

	err := row.Scan(
		&c.ID, &c.DatasetID, &c.Name, &c.Version, &status,
		&schemaJSON, &c.FreshnessSLAMinutes, &c.MinRowCount, &c.MaxNullRate,
		&c.ConsumerTeams, &breaking,
		&c.SignedAt, &c.SignedBy,
		&c.OwnerID, &metadataJSON,
		&c.CreatedBy, &c.UpdatedBy, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt,
	)
	if err != nil {
		return nil, err
	}

	c.Status = ContractStatus(status)
	c.BreakingChangePolicy = BreakingChangePolicy(breaking)
	c.SchemaDefinition = decodeJSONMap(schemaJSON)
	c.Metadata = decodeJSONMap(metadataJSON)
	if c.ConsumerTeams == nil {
		c.ConsumerTeams = []string{}
	}
	return &c, nil
}

func (r *pgRepository) CreateContract(ctx context.Context, c *Contract) error {
	schema, err := postgres.JSONB(c.SchemaDefinition)
	if err != nil {
		return fmt.Errorf("encode schema_definition: %w", err)
	}
	metadata, err := postgres.JSONB(c.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	const query = `
		INSERT INTO data_contracts (
			dataset_id, name, version, status,
			schema_definition, freshness_sla_minutes, min_row_count, max_null_rate,
			consumer_teams, breaking_change_policy,
			signed_at, signed_by,
			owner_id, metadata,
			created_by, updated_by
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10,
			$11, $12,
			$13, $14,
			$15, $16
		)
		RETURNING id, created_at, updated_at`

	err = r.db.QueryRow(ctx, query,
		c.DatasetID, c.Name, c.Version, string(c.Status),
		schema, c.FreshnessSLAMinutes, c.MinRowCount, c.MaxNullRate,
		c.ConsumerTeams, string(c.BreakingChangePolicy),
		c.SignedAt, c.SignedBy,
		c.OwnerID, metadata,
		c.CreatedBy, c.UpdatedBy,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)

	if err != nil {
		return fmt.Errorf("insert data contract: %w", err)
	}
	return nil
}

func (r *pgRepository) GetContractByID(ctx context.Context, id uuid.UUID) (*Contract, error) {
	query := `SELECT ` + contractColumns + ` FROM data_contracts WHERE id = $1 AND deleted_at IS NULL`

	contract, err := scanContract(r.db.QueryRow(ctx, query, id))
	if err != nil {
		return nil, fmt.Errorf("select data contract: %w", err)
	}
	return contract, nil
}

func (r *pgRepository) ListContracts(ctx context.Context, q ListContractsQuery) (platform.PageResult[*Contract], error) {
	var (
		clauses = []string{"deleted_at IS NULL"}
		args    []any
	)

	add := func(clause string, values ...any) {
		start := len(args) + 1
		args = append(args, values...)

		indices := make([]any, len(values))
		for i := range values {
			indices[i] = start + i
		}
		clauses = append(clauses, fmt.Sprintf(clause, indices...))
	}

	if q.DatasetID != "" {
		id, err := uuid.Parse(q.DatasetID)
		if err != nil {
			return platform.PageResult[*Contract]{}, platform.NewBadRequest(
				"dataset_id must be a valid UUID")
		}
		add("dataset_id = $%d", id)
	}
	if q.Status != "" {
		add("status = $%d", string(normaliseContractStatus(q.Status)))
	}
	if q.Search != "" {
		pattern := "%" + q.Search + "%"
		add("(name ILIKE $%d OR version ILIKE $%d)", pattern, pattern)
	}

	where := " WHERE " + strings.Join(clauses, " AND ")
	orderColumn := map[string]string{
		"name": "name", "version": "version", "status": "status",
		"created_at": "created_at", "updated_at": "updated_at",
	}[q.Sort.By]
	if orderColumn == "" {
		orderColumn = "created_at"
	}

	var total int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM data_contracts`+where, args...).Scan(&total); err != nil {
		return platform.PageResult[*Contract]{}, fmt.Errorf("count data contracts: %w", err)
	}
	if total == 0 {
		return platform.NewPageResult([]*Contract{}, 0, q.Limit, q.Offset), nil
	}

	args = append(args, q.Limit, q.Offset)
	listQuery := fmt.Sprintf(
		`SELECT %s FROM data_contracts%s ORDER BY %s %s, id LIMIT $%d OFFSET $%d`,
		contractColumns, where, orderColumn, q.Sort.Direction(), len(args)-1, len(args))

	rows, err := r.db.Query(ctx, listQuery, args...)
	if err != nil {
		return platform.PageResult[*Contract]{}, fmt.Errorf("list data contracts: %w", err)
	}
	defer rows.Close()

	items := make([]*Contract, 0, q.Limit)
	for rows.Next() {
		contract, err := scanContract(rows)
		if err != nil {
			return platform.PageResult[*Contract]{}, fmt.Errorf("scan data contract: %w", err)
		}
		items = append(items, contract)
	}
	if err := rows.Err(); err != nil {
		return platform.PageResult[*Contract]{}, fmt.Errorf("iterate data contract rows: %w", err)
	}

	return platform.NewPageResult(items, total, q.Limit, q.Offset), nil
}

func (r *pgRepository) UpdateContract(ctx context.Context, c *Contract) error {
	schema, err := postgres.JSONB(c.SchemaDefinition)
	if err != nil {
		return fmt.Errorf("encode schema_definition: %w", err)
	}
	metadata, err := postgres.JSONB(c.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	const query = `
		UPDATE data_contracts SET
			name = $2, status = $3,
			schema_definition = $4, freshness_sla_minutes = $5,
			min_row_count = $6, max_null_rate = $7,
			consumer_teams = $8, breaking_change_policy = $9,
			signed_at = $10, signed_by = $11,
			owner_id = $12, metadata = $13,
			updated_by = $14, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING updated_at`

	err = r.db.QueryRow(ctx, query,
		c.ID, c.Name, string(c.Status),
		schema, c.FreshnessSLAMinutes, c.MinRowCount, c.MaxNullRate,
		c.ConsumerTeams, string(c.BreakingChangePolicy),
		c.SignedAt, c.SignedBy,
		c.OwnerID, metadata, c.UpdatedBy,
	).Scan(&c.UpdatedAt)

	if err != nil {
		return fmt.Errorf("update data contract: %w", err)
	}
	return nil
}

func (r *pgRepository) SoftDeleteContract(ctx context.Context, id uuid.UUID, deletedAt time.Time) error {
	const query = `
		UPDATE data_contracts SET deleted_at = $2, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`

	tag, err := r.db.Exec(ctx, query, id, deletedAt.UTC())
	if err != nil {
		return fmt.Errorf("soft delete data contract: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// nullIfNil converts a nil UUID to SQL NULL so a query can test $2::uuid IS NULL
// and compare ids in the same statement.
func nullIfNil(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}
