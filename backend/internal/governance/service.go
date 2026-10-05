package governance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/edp/edp-control-plane/internal/infrastructure/postgres"
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

// Audit entity types for this domain.
const (
	AuditEntityPolicy   = "policy"
	AuditEntityContract = "data_contract"
)

// Service holds the governance business rules. Most of its value is in the
// policy-composition logic: matching policies against a subject and resource,
// and deciding the effective retention period.
type Service struct {
	repo     Repository
	auditor  Auditor
	validate *validator.Validate
	log      *slog.Logger
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

// NewService builds a governance service.
func NewService(repo Repository, opts ...ServiceOption) (*Service, error) {
	if repo == nil {
		return nil, errors.New("governance service: repository is required")
	}

	s := &Service{
		repo:     repo,
		log:      slog.Default(),
		validate: validator.New(validator.WithRequiredStructEnabled()),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// CreatePolicy registers a governance policy.
func (s *Service) CreatePolicy(ctx context.Context, req CreatePolicyRequest, actor platform.Actor) (*Policy, error) {
	if err := s.validate.Struct(req); err != nil {
		return nil, validationError(err)
	}

	if !req.PolicyType.Valid() {
		return nil, invalidEnum("policy_type", req.PolicyType, listPolicyTypes()...)
	}
	if !req.Effect.Valid() {
		return nil, invalidEnum("effect", req.Effect, "allow", "deny")
	}
	if !req.AppliesToClassification.Valid() {
		return nil, invalidEnum("applies_to_classification", req.AppliesToClassification,
			listClassifications()...)
	}
	if !req.ResourceType.Valid() {
		return nil, invalidEnum("resource_type", req.ResourceType, listResourceTypes()...)
	}
	if !req.Remediation.Valid() {
		return nil, invalidEnum("remediation", req.Remediation, listRemediations()...)
	}

	// Retention days are the entire point of a retention policy, and a retention
	// window on a policy that does not retain is a configuration error that
	// would otherwise pass unnoticed.
	if req.PolicyType == PolicyRetention && req.RetentionDays == nil {
		return nil, missingField("retention_days",
			"a retention policy must state how long data is kept")
	}
	if req.PolicyType != PolicyRetention && req.RetentionDays != nil {
		return nil, conflictingField("retention_days",
			fmt.Sprintf("a %s policy does not use a retention window", req.PolicyType))
	}

	policy := &Policy{
		Name:                    strings.TrimSpace(req.Name),
		Description:             strings.TrimSpace(req.Description),
		PolicyType:              normalisePolicyType(string(req.PolicyType)),
		Effect:                  normaliseEffect(string(req.Effect)),
		RuleExpression:          strings.TrimSpace(req.RuleExpression),
		AppliesToClassification: normaliseClassification(string(req.AppliesToClassification)),
		ResourceType:            normaliseResourceType(string(req.ResourceType)),
		ResourceID:              req.ResourceID,
		Remediation:             normaliseRemediation(string(req.Remediation)),
		RetentionDays:           req.RetentionDays,
		Priority:                defaultPriority,
		Enabled:                 true,
		OwnerID:                 strings.TrimSpace(req.OwnerID),
		Metadata:                req.Metadata,
		CreatedBy:               actor.ID,
		UpdatedBy:               actor.ID,
	}
	if req.Priority != nil {
		policy.Priority = *req.Priority
	}
	if req.Enabled != nil {
		policy.Enabled = *req.Enabled
	}
	if policy.OwnerID == "" {
		policy.OwnerID = actor.ID
	}
	if policy.Metadata == nil {
		policy.Metadata = map[string]any{}
	}

	slug, err := s.resolveSlug(ctx, req.Slug, policy.Name)
	if err != nil {
		return nil, err
	}
	policy.Slug = slug

	if err := s.repo.CreatePolicy(ctx, policy); err != nil {
		if postgres.IsUniqueViolation(err, "governance_policies_slug_unique") {
			return nil, platform.NewConflict(
				"a policy with slug %q already exists", policy.Slug).WithCause(err)
		}
		return nil, platform.AsError(err)
	}

	s.record(ctx, AuditEntry{
		Action:     "created",
		EntityType: AuditEntityPolicy,
		EntityID:   policy.ID,
		EntityName: policy.Name,
		Changes:    map[string]any{"policy": policySnapshot(policy)},
	})

	return policy, nil
}

// GetPolicy returns a single policy.
func (s *Service) GetPolicy(ctx context.Context, id uuid.UUID) (*Policy, error) {
	policy, err := s.repo.GetPolicyByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, platform.NewNotFound("policy", id)
		}
		return nil, platform.AsError(err)
	}
	return policy, nil
}

// GetPolicyBySlug returns a single policy addressed by slug.
func (s *Service) GetPolicyBySlug(ctx context.Context, slug string) (*Policy, error) {
	trimmed := strings.TrimSpace(slug)
	if trimmed == "" {
		return nil, platform.NewBadRequest("slug is required")
	}

	policy, err := s.repo.GetPolicyBySlug(ctx, trimmed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, platform.NewNotFound("policy", trimmed)
		}
		return nil, platform.AsError(err)
	}
	return policy, nil
}

// ListPolicies returns a page of policies.
func (s *Service) ListPolicies(ctx context.Context, q ListPoliciesQuery) (platform.PageResult[*Policy], error) {
	page, err := s.repo.ListPolicies(ctx, q)
	if err != nil {
		return platform.PageResult[*Policy]{}, platform.AsError(err)
	}
	return page, nil
}

// UpdatePolicy applies a partial update.
func (s *Service) UpdatePolicy(ctx context.Context, id uuid.UUID, req UpdatePolicyRequest, actor platform.Actor) (*Policy, error) {
	if req.isEmpty() {
		return nil, platform.NewBadRequest(
			"the request body must contain at least one field to change")
	}
	if err := s.validate.StructPartial(req, policyUpdateFields(req)...); err != nil {
		return nil, validationError(err)
	}

	current, err := s.GetPolicy(ctx, id)
	if err != nil {
		return nil, err
	}

	before := policySnapshot(current)
	updated := *current
	updated.UpdatedBy = actor.ID

	if req.Name != nil {
		updated.Name = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		updated.Description = strings.TrimSpace(*req.Description)
	}
	if req.PolicyType != nil {
		updated.PolicyType = normalisePolicyType(string(*req.PolicyType))
	}
	if req.Effect != nil {
		updated.Effect = normaliseEffect(string(*req.Effect))
	}
	if req.RuleExpression != nil {
		updated.RuleExpression = strings.TrimSpace(*req.RuleExpression)
	}
	if req.AppliesToClassification != nil {
		updated.AppliesToClassification = normaliseClassification(string(*req.AppliesToClassification))
	}
	if req.ResourceType != nil {
		updated.ResourceType = normaliseResourceType(string(*req.ResourceType))
	}
	if req.ClearResource {
		updated.ResourceID = nil
	} else if req.ResourceID != nil {
		updated.ResourceID = req.ResourceID
	}
	if req.Remediation != nil {
		updated.Remediation = normaliseRemediation(string(*req.Remediation))
	}
	if req.ClearRetention {
		updated.RetentionDays = nil
	} else if req.RetentionDays != nil {
		updated.RetentionDays = req.RetentionDays
	}
	if req.Priority != nil {
		updated.Priority = *req.Priority
	}
	if req.Enabled != nil {
		updated.Enabled = *req.Enabled
	}
	if req.OwnerID != nil {
		updated.OwnerID = strings.TrimSpace(*req.OwnerID)
	}
	if req.ReplaceMetadata && req.Metadata != nil {
		updated.Metadata = req.Metadata
	} else if req.Metadata != nil {
		updated.Metadata = mergeMaps(updated.Metadata, req.Metadata)
	}

	// Re-check the retention pairing after merging.
	if err := checkRetentionCoherence(&updated); err != nil {
		return nil, err
	}

	if err := s.repo.UpdatePolicy(ctx, &updated); err != nil {
		return nil, platform.AsError(err)
	}

	s.record(ctx, AuditEntry{
		Action:     "updated",
		EntityType: AuditEntityPolicy,
		EntityID:   updated.ID,
		EntityName: updated.Name,
		Changes:    map[string]any{"before": before, "after": policySnapshot(&updated)},
	})

	return &updated, nil
}

// DeletePolicy archives a policy.
func (s *Service) DeletePolicy(ctx context.Context, id uuid.UUID, actor platform.Actor) error {
	policy, err := s.GetPolicy(ctx, id)
	if err != nil {
		return err
	}

	if err := s.repo.SoftDeletePolicy(ctx, id, time.Now().UTC()); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return platform.NewNotFound("policy", id)
		}
		return platform.AsError(err)
	}

	s.record(ctx, AuditEntry{
		Action:     "deleted",
		EntityType: AuditEntityPolicy,
		EntityID:   id,
		EntityName: policy.Name,
		Changes:    map[string]any{"before": policySnapshot(policy)},
	})

	return nil
}

// PolicyDecision is the outcome of evaluating policies against a request.
type PolicyDecision struct {
	// Allowed is false when any matching deny policy fired.
	Allowed bool `json:"allowed"`
	// Reason explains a denial, for the audit trail.
	Reason string `json:"reason,omitempty"`

	// MatchedPolicies lists the policies that applied, in priority order.
	MatchedPolicies []uuid.UUID `json:"matched_policies"`
	// Remediation is the strictest remediation among matching policies.
	Remediation Remediation `json:"remediation,omitempty"`
	// WinningPolicy is the policy that determined the outcome.
	WinningPolicy *uuid.UUID `json:"winning_policy,omitempty"`
}

// Evaluate resolves which policies apply to a subject accessing a resource and
// what the effective outcome is.
//
// The rule expression is intentionally NOT evaluated here. Deciding access is
// the authorization middleware's job, which already has an expression engine;
// duplicating it would give two answers to the same question. This method
// answers the governance question that middleware cannot: which policies apply
// to this resource and what is the strictest remediation among them.
func (s *Service) Evaluate(ctx context.Context, q EvaluateQuery) (*PolicyDecision, error) {
	policies, err := s.repo.ListEffectivePolicies(ctx, q)
	if err != nil {
		return nil, platform.AsError(err)
	}

	decision := &PolicyDecision{
		Allowed:         true,
		MatchedPolicies: []uuid.UUID{},
	}

	for _, p := range policies {
		decision.MatchedPolicies = append(decision.MatchedPolicies, p.ID)

		if p.Effect == EffectDeny {
			decision.Allowed = false
			decision.Reason = fmt.Sprintf(
				"policy %q (%s) denies access at classification %s or above",
				p.Name, p.PolicyType, p.AppliesToClassification)
			if decision.WinningPolicy == nil {
				decision.WinningPolicy = &p.ID
			}
		}

		// Strictest remediation wins, so a delete policy alongside a notify
		// policy results in deletion.
		if remediationRank(p.Remediation) > remediationRank(decision.Remediation) {
			decision.Remediation = p.Remediation
		}
	}

	return decision, nil
}

// EffectiveRetention returns the shortest retention window among matching
// retention policies, which is the window that must be honoured. Zero means no
// retention policy applies.
func (s *Service) EffectiveRetention(ctx context.Context, resourceType ResourceType, resourceID uuid.UUID) (int, error) {
	policies, err := s.repo.ListEffectivePolicies(ctx, EvaluateQuery{
		ResourceType: resourceType,
		ResourceID:   resourceID,
		PolicyType:   PolicyRetention,
	})
	if err != nil {
		return 0, platform.AsError(err)
	}

	shortest := 0
	for _, p := range policies {
		if p.RetentionDays == nil {
			continue
		}
		if shortest == 0 || *p.RetentionDays < shortest {
			shortest = *p.RetentionDays
		}
	}
	return shortest, nil
}

// CreateContract registers a data contract on a dataset.
func (s *Service) CreateContract(ctx context.Context, req CreateContractRequest, actor platform.Actor) (*Contract, error) {
	if err := s.validate.Struct(req); err != nil {
		return nil, validationError(err)
	}

	if !req.Status.Valid() {
		return nil, invalidEnum("status", req.Status, listContractStatuses()...)
	}
	if !req.BreakingChangePolicy.Valid() {
		return nil, invalidEnum("breaking_change_policy", req.BreakingChangePolicy,
			"notify", "major_version", "block")
	}

	contract := &Contract{
		DatasetID:            req.DatasetID,
		Name:                 strings.TrimSpace(req.Name),
		Version:              firstNonEmpty(strings.TrimSpace(req.Version), defaultContractVersion),
		Status:               normaliseContractStatus(string(req.Status)),
		SchemaDefinition:     req.SchemaDefinition,
		FreshnessSLAMinutes:  req.FreshnessSLAMinutes,
		MinRowCount:          req.MinRowCount,
		MaxNullRate:          req.MaxNullRate,
		ConsumerTeams:        normaliseList(req.ConsumerTeams),
		BreakingChangePolicy: normaliseBreakingChangePolicy(string(req.BreakingChangePolicy)),
		OwnerID:              strings.TrimSpace(req.OwnerID),
		Metadata:             req.Metadata,
		CreatedBy:            actor.ID,
		UpdatedBy:            actor.ID,
	}
	if contract.OwnerID == "" {
		contract.OwnerID = actor.ID
	}
	if contract.Metadata == nil {
		contract.Metadata = map[string]any{}
	}

	if err := s.repo.CreateContract(ctx, contract); err != nil {
		if postgres.IsUniqueViolation(err, "data_contracts_dataset_version_unique") {
			return nil, platform.NewConflict(
				"dataset already has contract version %q", contract.Version).WithCause(err)
		}
		return nil, platform.AsError(err)
	}

	s.record(ctx, AuditEntry{
		Action:     "created",
		EntityType: AuditEntityContract,
		EntityID:   contract.ID,
		EntityName: contract.Name,
		Changes:    map[string]any{"contract": contractSnapshot(contract)},
	})

	return contract, nil
}

// GetContract returns a single contract.
func (s *Service) GetContract(ctx context.Context, id uuid.UUID) (*Contract, error) {
	contract, err := s.repo.GetContractByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, platform.NewNotFound("data contract", id)
		}
		return nil, platform.AsError(err)
	}
	return contract, nil
}

// ListContracts returns a page of contracts.
func (s *Service) ListContracts(ctx context.Context, q ListContractsQuery) (platform.PageResult[*Contract], error) {
	page, err := s.repo.ListContracts(ctx, q)
	if err != nil {
		return platform.PageResult[*Contract]{}, platform.AsError(err)
	}
	return page, nil
}

// UpdateContract applies a partial update.
//
// Activating a contract requires a schema definition; a contract that promises
// nothing is worse than no contract, because consumers will rely on it.
func (s *Service) UpdateContract(ctx context.Context, id uuid.UUID, req UpdateContractRequest, actor platform.Actor) (*Contract, error) {
	if req.isEmpty() {
		return nil, platform.NewBadRequest(
			"the request body must contain at least one field to change")
	}
	if err := s.validate.StructPartial(req, contractUpdateFields(req)...); err != nil {
		return nil, validationError(err)
	}

	current, err := s.GetContract(ctx, id)
	if err != nil {
		return nil, err
	}

	before := contractSnapshot(current)
	updated := *current
	updated.UpdatedBy = actor.ID

	if req.Name != nil {
		updated.Name = strings.TrimSpace(*req.Name)
	}
	if req.Status != nil {
		updated.Status = normaliseContractStatus(string(*req.Status))
	}
	if req.SchemaDefinition != nil {
		updated.SchemaDefinition = req.SchemaDefinition
	}
	if req.ClearFreshness {
		updated.FreshnessSLAMinutes = nil
	} else if req.FreshnessSLAMinutes != nil {
		updated.FreshnessSLAMinutes = req.FreshnessSLAMinutes
	}
	if req.ClearMinRowCount {
		updated.MinRowCount = nil
	} else if req.MinRowCount != nil {
		updated.MinRowCount = req.MinRowCount
	}
	if req.ClearMaxNullRate {
		updated.MaxNullRate = nil
	} else if req.MaxNullRate != nil {
		updated.MaxNullRate = req.MaxNullRate
	}
	if req.ReplaceConsumerTeams && req.ConsumerTeams != nil {
		updated.ConsumerTeams = normaliseList(req.ConsumerTeams)
	} else if req.ConsumerTeams != nil {
		updated.ConsumerTeams = mergeLists(updated.ConsumerTeams, req.ConsumerTeams)
	}
	if req.BreakingChangePolicy != nil {
		updated.BreakingChangePolicy = normaliseBreakingChangePolicy(string(*req.BreakingChangePolicy))
	}
	if req.OwnerID != nil {
		updated.OwnerID = strings.TrimSpace(*req.OwnerID)
	}
	if req.ReplaceMetadata && req.Metadata != nil {
		updated.Metadata = req.Metadata
	} else if req.Metadata != nil {
		updated.Metadata = mergeMaps(updated.Metadata, req.Metadata)
	}

	if updated.Status == ContractActive && len(updated.SchemaDefinition) == 0 {
		return nil, conflictingField("schema_definition",
			"a contract cannot be activated without a schema definition")
	}

	if err := s.repo.UpdateContract(ctx, &updated); err != nil {
		return nil, platform.AsError(err)
	}

	s.record(ctx, AuditEntry{
		Action:     "updated",
		EntityType: AuditEntityContract,
		EntityID:   updated.ID,
		EntityName: updated.Name,
		Changes:    map[string]any{"before": before, "after": contractSnapshot(&updated)},
	})

	return &updated, nil
}

// DeleteContract archives a contract.
func (s *Service) DeleteContract(ctx context.Context, id uuid.UUID, actor platform.Actor) error {
	contract, err := s.GetContract(ctx, id)
	if err != nil {
		return err
	}

	if err := s.repo.SoftDeleteContract(ctx, id, time.Now().UTC()); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return platform.NewNotFound("data contract", id)
		}
		return platform.AsError(err)
	}

	s.record(ctx, AuditEntry{
		Action:     "deleted",
		EntityType: AuditEntityContract,
		EntityID:   id,
		EntityName: contract.Name,
		Changes:    map[string]any{"before": contractSnapshot(contract)},
	})

	return nil
}

// SignContract marks a contract as agreed by its producer.
func (s *Service) SignContract(ctx context.Context, id uuid.UUID, actor platform.Actor) (*Contract, error) {
	current, err := s.GetContract(ctx, id)
	if err != nil {
		return nil, err
	}

	if current.Status == ContractDeprecated {
		return nil, platform.NewConflict(
			"a deprecated contract cannot be signed; create a new version instead")
	}
	if current.SignedAt != nil && current.SignedBy == actor.ID {
		return current, nil
	}

	now := time.Now().UTC()
	current.SignedAt = &now
	current.SignedBy = actor.ID
	current.UpdatedBy = actor.ID

	if err := s.repo.UpdateContract(ctx, current); err != nil {
		return nil, platform.AsError(err)
	}

	s.record(ctx, AuditEntry{
		Action:     "approved",
		EntityType: AuditEntityContract,
		EntityID:   current.ID,
		EntityName: current.Name,
		Changes:    map[string]any{"signed_by": actor.ID, "signed_at": now},
	})

	return current, nil
}

// checkRetentionCoherence re-checks the retention window after a merge.
func checkRetentionCoherence(p *Policy) error {
	if p.PolicyType == PolicyRetention && p.RetentionDays == nil {
		return missingField("retention_days",
			"a retention policy must state how long data is kept")
	}
	if p.PolicyType != PolicyRetention && p.RetentionDays != nil {
		return conflictingField("retention_days",
			fmt.Sprintf("a %s policy does not use a retention window", p.PolicyType))
	}
	return nil
}

// remediationRank orders remediations by how aggressive they are.
func remediationRank(r Remediation) int {
	switch r {
	case RemediationNone:
		return 0
	case RemediationNotify:
		return 1
	case RemediationQuarantine:
		return 2
	case RemediationBlock:
		return 3
	case RemediationDelete:
		return 4
	default:
		return 0
	}
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
