package quality

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/edp/edp-control-plane/internal/infrastructure/postgres"
	"github.com/edp/edp-control-plane/internal/platform"
)

// Auditor records state changes. The audit domain supplies the implementation;
// declaring it here avoids a dependency cycle between domains.
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

// AuditEntityRule is the audit entity type for quality rules.
const AuditEntityRule = "quality_rule"

// Service is the quality domain's business logic.
type Service struct {
	repo    Repository
	auditor Auditor
	log     *slog.Logger
}

// ServiceOption customises a Service.
type ServiceOption func(*Service)

// WithLogger overrides the logger.
func WithLogger(l *slog.Logger) ServiceOption {
	return func(s *Service) {
		if l != nil {
			s.log = l
		}
	}
}

// WithAuditor attaches an audit sink. Audit failures are logged, not fatal:
// losing an audit row must not roll back the business change.
func WithAuditor(a Auditor) ServiceOption {
	return func(s *Service) { s.auditor = a }
}

// NewService builds a quality service.
func NewService(repo Repository, opts ...ServiceOption) (*Service, error) {
	if repo == nil {
		return nil, errors.New("quality service: repository is required")
	}

	s := &Service{repo: repo, log: slog.Default()}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// CreateRule registers a new quality rule on a dataset.
func (s *Service) CreateRule(ctx context.Context, req CreateRuleRequest, actor platform.Actor) (*Rule, error) {
	if err := ValidateCreateRule(req); err != nil {
		return nil, err
	}

	rule := &Rule{
		DatasetID:         req.DatasetID,
		Name:              strings.TrimSpace(req.Name),
		Description:       strings.TrimSpace(req.Description),
		RuleType:          normaliseRuleType(string(req.RuleType)),
		Expectation:       strings.TrimSpace(req.Expectation),
		ExpectationParams: req.ExpectationParams,
		Severity:          normaliseSeverity(string(req.Severity)),
		FailureThreshold:  req.FailureThreshold,
		Dimension:         normaliseDimension(string(req.Dimension)),
		TargetColumn:      strings.TrimSpace(req.TargetColumn),
		// Default both flags to true: a rule that is registered but not blocking
		// would otherwise let data through by omission.
		Enabled:    true,
		Blocking:   true,
		PipelineID: req.PipelineID,
		OwnerID:    strings.TrimSpace(req.OwnerID),
		Tags:       normaliseTags(req.Tags),
		Metadata:   req.Metadata,
		CreatedBy:  actor.ID,
		UpdatedBy:  actor.ID,
	}
	if rule.ExpectationParams == nil {
		rule.ExpectationParams = map[string]any{}
	}
	if rule.Metadata == nil {
		rule.Metadata = map[string]any{}
	}
	if rule.OwnerID == "" {
		rule.OwnerID = actor.ID
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	if req.Blocking != nil {
		rule.Blocking = *req.Blocking
	}

	if err := s.repo.CreateRule(ctx, rule); err != nil {
		if postgres.IsUniqueViolation(err, "quality_rules_name_unique") {
			return nil, platform.NewConflict(
				"a quality rule named %q already exists on this dataset", rule.Name).
				WithCause(err)
		}
		return nil, platform.AsError(err)
	}

	s.record(ctx, AuditEntry{
		Action:     "created",
		EntityType: AuditEntityRule,
		EntityID:   rule.ID,
		EntityName: rule.Name,
		Changes:    map[string]any{"rule": ruleSnapshot(rule)},
	})

	return rule, nil
}

// GetRule returns a single rule.
func (s *Service) GetRule(ctx context.Context, id uuid.UUID) (*Rule, error) {
	rule, err := s.repo.GetRuleByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, platform.NewNotFound("quality rule", id)
		}
		return nil, platform.AsError(err)
	}
	return rule, nil
}

// ListRules returns a page of rules.
func (s *Service) ListRules(ctx context.Context, q ListRulesQuery) (platform.PageResult[*Rule], error) {
	page, err := s.repo.ListRules(ctx, q)
	if err != nil {
		return platform.PageResult[*Rule]{}, platform.AsError(err)
	}
	return page, nil
}

// UpdateRule applies a partial update.
//
// The current rule is read first so the audit trail can record what changed
// rather than only the new values, and so unset fields keep their existing
// values instead of being reset to zero values.
func (s *Service) UpdateRule(ctx context.Context, id uuid.UUID, req UpdateRuleRequest, actor platform.Actor) (*Rule, error) {
	if req.isEmpty() {
		return nil, platform.NewBadRequest(
			"the request body must contain at least one field to change")
	}
	if err := ValidateUpdateRule(req); err != nil {
		return nil, err
	}

	current, err := s.GetRule(ctx, id)
	if err != nil {
		return nil, err
	}

	before := ruleSnapshot(current)
	updated := *current
	updated.UpdatedBy = actor.ID

	if req.Name != nil {
		updated.Name = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		updated.Description = strings.TrimSpace(*req.Description)
	}
	if req.RuleType != nil {
		updated.RuleType = normaliseRuleType(string(*req.RuleType))
	}
	if req.Expectation != nil {
		updated.Expectation = strings.TrimSpace(*req.Expectation)
	}
	if req.ExpectationParams != nil {
		updated.ExpectationParams = req.ExpectationParams
	}
	if req.Severity != nil {
		updated.Severity = normaliseSeverity(string(*req.Severity))
	}
	if req.FailureThreshold != nil {
		updated.FailureThreshold = *req.FailureThreshold
	}
	if req.Dimension != nil {
		updated.Dimension = normaliseDimension(string(*req.Dimension))
	}
	if req.TargetColumn != nil {
		updated.TargetColumn = strings.TrimSpace(*req.TargetColumn)
	}
	if req.Enabled != nil {
		updated.Enabled = *req.Enabled
	}
	if req.Blocking != nil {
		updated.Blocking = *req.Blocking
	}
	if req.ClearPipeline {
		updated.PipelineID = nil
	} else if req.PipelineID != nil {
		updated.PipelineID = req.PipelineID
	}
	if req.OwnerID != nil {
		updated.OwnerID = strings.TrimSpace(*req.OwnerID)
	}
	if req.ReplaceTags && req.Tags != nil {
		updated.Tags = normaliseTags(req.Tags)
	} else if req.Tags != nil {
		updated.Tags = mergeTags(updated.Tags, req.Tags)
	}
	if req.ReplaceMetadata && req.Metadata != nil {
		updated.Metadata = req.Metadata
	} else if req.Metadata != nil {
		updated.Metadata = mergeMaps(updated.Metadata, req.Metadata)
	}

	// Re-check the column requirement after merging, because changing rule_type
	// or target_column can invalidate the pair.
	if err := validateColumnCoherence(&updated); err != nil {
		return nil, err
	}

	if err := s.repo.UpdateRule(ctx, &updated); err != nil {
		if postgres.IsUniqueViolation(err, "quality_rules_name_unique") {
			return nil, platform.NewConflict(
				"a quality rule named %q already exists on this dataset", updated.Name).
				WithCause(err)
		}
		return nil, platform.AsError(err)
	}

	s.record(ctx, AuditEntry{
		Action:     "updated",
		EntityType: AuditEntityRule,
		EntityID:   updated.ID,
		EntityName: updated.Name,
		Changes:    map[string]any{"before": before, "after": ruleSnapshot(&updated)},
	})

	return &updated, nil
}

// DeleteRule archives a rule.
func (s *Service) DeleteRule(ctx context.Context, id uuid.UUID, actor platform.Actor) error {
	rule, err := s.GetRule(ctx, id)
	if err != nil {
		return err
	}

	if err := s.repo.SoftDeleteRule(ctx, id, time.Now().UTC()); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return platform.NewNotFound("quality rule", id)
		}
		return platform.AsError(err)
	}

	s.record(ctx, AuditEntry{
		Action:     "deleted",
		EntityType: AuditEntityRule,
		EntityID:   id,
		EntityName: rule.Name,
		Changes:    map[string]any{"before": ruleSnapshot(rule)},
	})

	return nil
}

// RecordCheck stores the result of evaluating a rule. It is called by the
// execution engine, not exposed directly over HTTP.
func (s *Service) RecordCheck(ctx context.Context, req RecordCheckRequest, pipelineRunID *uuid.UUID) (*CheckRun, error) {
	if err := shared.Struct(req); err != nil {
		return nil, platform.NewValidation("the quality check payload failed validation").
			WithFields(fieldErrors(err)...)
	}

	status := normaliseCheckStatus(string(req.Status))
	if !status.Valid() {
		return nil, unsupportedEnum("status", req.Status,
			[]string{"passed", "warning", "failed", "error", "skipped"})
	}

	rule, err := s.repo.GetRuleByID(ctx, req.RuleID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, platform.NewNotFound("quality rule", req.RuleID)
		}
		return nil, platform.AsError(err)
	}

	check := &CheckRun{
		RuleID:        rule.ID,
		DatasetID:     rule.DatasetID,
		PipelineRunID: pipelineRunID,
		Status:        status,
		ObservedValue: req.ObservedValue,
		ExpectedValue: strings.TrimSpace(req.ExpectedValue),
		RowsEvaluated: req.RowsEvaluated,
		ViolationRate: req.ViolationRate,
		PassedCount:   req.PassedCount,
		FailedCount:   req.FailedCount,
		Message:       strings.TrimSpace(req.Message),
		Details:       req.Details,
		DurationMs:    req.DurationMs,
		EvaluatedAt:   time.Now().UTC(),
	}
	if check.Details == nil {
		check.Details = map[string]any{}
	}

	if err := s.repo.RecordCheck(ctx, check); err != nil {
		return nil, platform.AsError(err)
	}

	// Only a failing check on a blocking rule changes the pipeline outcome, so
	// only that case is worth an audit entry.
	if status == CheckFailed && rule.Blocking {
		s.record(ctx, AuditEntry{
			Action:     "violated",
			EntityType: AuditEntityRule,
			EntityID:   rule.ID,
			EntityName: rule.Name,
			Changes: map[string]any{
				"check":    checkRunSnapshot(check),
				"severity": rule.Severity,
			},
		})
	}

	return check, nil
}

// ListChecks returns a page of evaluations.
func (s *Service) ListChecks(ctx context.Context, q ListCheckRunsQuery) (platform.PageResult[*CheckRun], error) {
	if q.Since != nil && q.Until != nil && q.Until.Before(*q.Since) {
		return platform.PageResult[*CheckRun]{}, platform.NewBadRequest(
			"until must not be earlier than since")
	}

	page, err := s.repo.ListChecks(ctx, q)
	if err != nil {
		return platform.PageResult[*CheckRun]{}, platform.AsError(err)
	}
	return page, nil
}

// GetCheck returns a single evaluation.
func (s *Service) GetCheck(ctx context.Context, id uuid.UUID) (*CheckRun, error) {
	check, err := s.repo.GetCheckByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, platform.NewNotFound("quality check run", id)
		}
		return nil, platform.AsError(err)
	}
	return check, nil
}

// Health summarises rule performance for a dataset.
func (s *Service) Health(ctx context.Context, datasetID uuid.UUID) (*RuleHealth, error) {
	health, err := s.repo.RuleHealth(ctx, datasetID)
	if err != nil {
		return nil, platform.AsError(err)
	}
	return health, nil
}

// GateResult is the outcome of evaluating a dataset's blocking rules. The
// execution domain uses it to decide whether a run may proceed.
type GateResult struct {
	DatasetID uuid.UUID `json:"dataset_id"`
	Passed    bool      `json:"passed"`

	TotalBlocking int         `json:"total_blocking"`
	FailedCount   int         `json:"failed_count"`
	FailedRuleIDs []uuid.UUID `json:"failed_rule_ids"`
	WorstSeverity Severity    `json:"worst_severity,omitempty"`
	Reason        string      `json:"reason,omitempty"`
}

// EvaluateGate checks whether a dataset's blocking rules are currently
// satisfied, so a pipeline can be blocked before it writes bad data.
//
// A rule with no recorded evaluation does not fail the gate: refusing to run
// because a check has not run yet would deadlock a freshly created dataset.
// An errored check does fail the gate, because an unevaluated-by-error rule
// cannot be treated as evidence of quality.
func (s *Service) EvaluateGate(ctx context.Context, datasetID uuid.UUID) (*GateResult, error) {
	rules, err := s.repo.EnabledRules(ctx, datasetID)
	if err != nil {
		return nil, platform.AsError(err)
	}

	result := &GateResult{
		DatasetID:     datasetID,
		Passed:        true,
		TotalBlocking: len(rules),
		FailedRuleIDs: []uuid.UUID{},
	}

	for _, rule := range rules {
		if !rule.Blocking {
			continue
		}

		latest, err := s.repo.LatestCheckForRule(ctx, rule.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Never evaluated: no evidence either way.
				continue
			}
			return nil, platform.AsError(err)
		}

		switch latest.Status {
		case CheckFailed, CheckError:
			result.Passed = false
			result.FailedCount++
			result.FailedRuleIDs = append(result.FailedRuleIDs, rule.ID)
			if rule.Severity.rank() > result.WorstSeverity.rank() {
				result.WorstSeverity = rule.Severity
			}
		}
	}

	if !result.Passed {
		result.Reason = fmt.Sprintf(
			"%d blocking quality rule(s) are failing; worst severity is %s",
			result.FailedCount, result.WorstSeverity)
	}

	return result, nil
}

// validateColumnCoherence re-checks the rule-type/target-column pairing after
// a merge.
func validateColumnCoherence(r *Rule) error {
	if r.RuleType.requiresColumn() && strings.TrimSpace(r.TargetColumn) == "" {
		return missingField("target_column",
			fmt.Sprintf("a %s rule must name the column it validates", r.RuleType))
	}
	if !r.RuleType.requiresColumn() && strings.TrimSpace(r.TargetColumn) != "" {
		return conflictingField("target_column",
			fmt.Sprintf("a %s rule applies to the whole dataset and must not name a column", r.RuleType))
	}
	return nil
}

// mergeTags adds tags to an existing set without duplicates, preserving order.
func mergeTags(existing, incoming []string) []string {
	seen := make(map[string]bool, len(existing))
	out := make([]string, 0, len(existing)+len(incoming))

	for _, v := range append(append([]string{}, existing...), incoming...) {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

// mergeMaps merges incoming keys into a copy of existing.
func mergeMaps(existing, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(existing)+len(incoming))
	for k, v := range existing {
		out[k] = v
	}
	for k, v := range incoming {
		out[k] = v
	}
	return out
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
