// Package governance manages access and retention policies and the data
// contracts that producers and consumers agree to.
package governance

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// maxSlugLength bounds generated slugs.
const maxSlugLength = 100

// defaultPriority applies to a policy that does not state one. Lower numbers
// are evaluated first, so 100 sits mid-range rather than ahead of deliberate
// priorities.
const defaultPriority = 100

// defaultContractVersion is used when a contract is created without one.
const defaultContractVersion = "1.0.0"

// PolicyType is the category of control a policy expresses.
type PolicyType string

// Supported policy types.
const (
	PolicyAccessControl PolicyType = "access_control"
	PolicyRetention     PolicyType = "retention"
	PolicyResidency     PolicyType = "residency"
	PolicyConsent       PolicyType = "consent"
	PolicyDisclosure    PolicyType = "disclosure"
)

var policyTypes = map[PolicyType]bool{
	PolicyAccessControl: true, PolicyRetention: true, PolicyResidency: true,
	PolicyConsent: true, PolicyDisclosure: true,
}

// Valid reports whether t is a supported policy type.
func (t PolicyType) Valid() bool { return policyTypes[t] }

// Effect is the outcome when a policy's rule expression matches.
type Effect string

// Supported policy effects.
const (
	EffectAllow Effect = "allow"
	EffectDeny  Effect = "deny"
)

var effects = map[Effect]bool{EffectAllow: true, EffectDeny: true}

// Valid reports whether e is a supported effect.
func (e Effect) Valid() bool { return effects[e] }

// Classification orders how sensitive data is, from least to most restricted.
type Classification string

// Supported classifications.
const (
	ClassificationPublic       Classification = "public"
	ClassificationInternal     Classification = "internal"
	ClassificationConfidential Classification = "confidential"
	ClassificationRestricted   Classification = "restricted"
)

var classifications = map[Classification]bool{
	ClassificationPublic: true, ClassificationInternal: true,
	ClassificationConfidential: true, ClassificationRestricted: true,
}

// Valid reports whether c is a supported classification.
func (c Classification) Valid() bool { return classifications[c] }

// rank orders classifications for "at least this sensitive" comparisons.
func (c Classification) rank() int {
	switch c {
	case ClassificationPublic:
		return 1
	case ClassificationInternal:
		return 2
	case ClassificationConfidential:
		return 3
	case ClassificationRestricted:
		return 4
	default:
		return 0
	}
}

// AtLeast reports whether c is at least as sensitive as floor. An unrecognised
// classification is treated as maximally sensitive, so a bad value fails closed.
func (c Classification) AtLeast(floor Classification) bool {
	if c.rank() == 0 {
		return true
	}
	return c.rank() >= floor.rank()
}

// ResourceType is the kind of resource a policy guards.
type ResourceType string

// Supported resource types.
const (
	ResourceDataset  ResourceType = "dataset"
	ResourceSource   ResourceType = "source"
	ResourcePipeline ResourceType = "pipeline"
	ResourceColumn   ResourceType = "column"
)

var resourceTypes = map[ResourceType]bool{
	ResourceDataset: true, ResourceSource: true,
	ResourcePipeline: true, ResourceColumn: true,
}

// Valid reports whether t is a supported resource type.
func (t ResourceType) Valid() bool { return resourceTypes[t] }

// Remediation is the automatic action taken when a policy is violated.
type Remediation string

// Supported remediation actions.
const (
	RemediationNone       Remediation = "none"
	RemediationNotify     Remediation = "notify"
	RemediationQuarantine Remediation = "quarantine"
	RemediationBlock      Remediation = "block"
	RemediationDelete     Remediation = "delete"
)

var remediations = map[Remediation]bool{
	RemediationNone: true, RemediationNotify: true, RemediationQuarantine: true,
	RemediationBlock: true, RemediationDelete: true,
}

// Valid reports whether r is a supported remediation.
func (r Remediation) Valid() bool { return remediations[r] }

// Policy is a governance control bound to a resource or resource type.
type Policy struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`

	PolicyType PolicyType `json:"policy_type"`
	Effect     Effect     `json:"effect"`

	// RuleExpression is evaluated against the request subject and resource, for
	// example: subject.roles in ('data-steward')
	RuleExpression string `json:"rule_expression"`

	// AppliesToClassification is the sensitivity floor: the policy applies to
	// resources at or above this classification.
	AppliesToClassification Classification `json:"applies_to_classification"`

	ResourceType ResourceType `json:"resource_type"`
	// ResourceID scopes the policy to one resource; nil means the whole type.
	ResourceID *uuid.UUID `json:"resource_id,omitempty"`

	Remediation   Remediation `json:"remediation"`
	RetentionDays *int        `json:"retention_days,omitempty"`
	Priority      int         `json:"priority"`
	Enabled       bool        `json:"enabled"`

	OwnerID  string         `json:"owner_id"`
	Metadata map[string]any `json:"metadata"`

	CreatedBy string     `json:"created_by"`
	UpdatedBy string     `json:"updated_by"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// ContractStatus is the lifecycle state of a data contract.
type ContractStatus string

// Contract statuses.
const (
	ContractDraft      ContractStatus = "draft"
	ContractActive     ContractStatus = "active"
	ContractBreached   ContractStatus = "breached"
	ContractDeprecated ContractStatus = "deprecated"
)

var contractStatuses = map[ContractStatus]bool{
	ContractDraft: true, ContractActive: true,
	ContractBreached: true, ContractDeprecated: true,
}

// Valid reports whether s is a supported status.
func (s ContractStatus) Valid() bool { return contractStatuses[s] }

// BreakingChangePolicy is what happens when a producer breaks a contract.
type BreakingChangePolicy string

// Supported breaking-change policies.
const (
	BreakingNotify       BreakingChangePolicy = "notify"
	BreakingMajorVersion BreakingChangePolicy = "major_version"
	BreakingBlock        BreakingChangePolicy = "block"
)

var breakingChangePolicies = map[BreakingChangePolicy]bool{
	BreakingNotify: true, BreakingMajorVersion: true, BreakingBlock: true,
}

// Valid reports whether p is a supported breaking-change policy.
func (p BreakingChangePolicy) Valid() bool { return breakingChangePolicies[p] }

// Contract is a producer/consumer agreement about a dataset's shape and
// freshness.
type Contract struct {
	ID        uuid.UUID      `json:"id"`
	DatasetID uuid.UUID      `json:"dataset_id"`
	Name      string         `json:"name"`
	Version   string         `json:"version"`
	Status    ContractStatus `json:"status"`

	// SchemaDefinition declares required columns, nullability and types. It is
	// free-form so a contract can express more than the columns the control
	// plane models.
	SchemaDefinition map[string]any `json:"schema_definition"`

	FreshnessSLAMinutes *int     `json:"freshness_sla_minutes,omitempty"`
	MinRowCount         *int64   `json:"min_row_count,omitempty"`
	MaxNullRate         *float64 `json:"max_null_rate,omitempty"`

	ConsumerTeams []string `json:"consumer_teams"`

	BreakingChangePolicy BreakingChangePolicy `json:"breaking_change_policy"`

	SignedAt *time.Time `json:"signed_at,omitempty"`
	SignedBy string     `json:"signed_by"`

	OwnerID  string         `json:"owner_id"`
	Metadata map[string]any `json:"metadata"`

	CreatedBy string     `json:"created_by"`
	UpdatedBy string     `json:"updated_by"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// CreatePolicyRequest is the input to CreatePolicy.
type CreatePolicyRequest struct {
	Name        string `json:"name" validate:"required,min=1,max=255"`
	Slug        string `json:"slug" validate:"omitempty,max=255"`
	Description string `json:"description" validate:"max=2000"`

	PolicyType PolicyType `json:"policy_type" validate:"required"`
	Effect     Effect     `json:"effect" validate:"required"`

	RuleExpression string `json:"rule_expression" validate:"required,max=2000"`

	AppliesToClassification Classification `json:"applies_to_classification" validate:"required"`
	ResourceType            ResourceType   `json:"resource_type" validate:"required"`
	ResourceID              *uuid.UUID     `json:"resource_id"`

	Remediation Remediation `json:"remediation" validate:"required"`
	// RetentionDays is required for retention policies and rejected otherwise.
	RetentionDays *int `json:"retention_days" validate:"omitempty,gte=0"`

	Priority *int  `json:"priority" validate:"omitempty,gte=0,lte=10000"`
	Enabled  *bool `json:"enabled"`

	OwnerID  string         `json:"owner_id" validate:"max=255"`
	Metadata map[string]any `json:"metadata"`
}

// UpdatePolicyRequest is a partial policy update.
type UpdatePolicyRequest struct {
	Name           *string     `json:"name" validate:"omitempty,min=1,max=255"`
	Description    *string     `json:"description" validate:"omitempty,max=2000"`
	PolicyType     *PolicyType `json:"policy_type"`
	Effect         *Effect     `json:"effect"`
	RuleExpression *string     `json:"rule_expression" validate:"omitempty,max=2000"`

	AppliesToClassification *Classification `json:"applies_to_classification"`
	ResourceType            *ResourceType   `json:"resource_type"`
	ResourceID              *uuid.UUID      `json:"resource_id"`
	ClearResource           bool            `json:"clear_resource"`

	Remediation     *Remediation   `json:"remediation"`
	RetentionDays   *int           `json:"retention_days" validate:"omitempty,gte=0"`
	ClearRetention  bool           `json:"clear_retention"`
	Priority        *int           `json:"priority" validate:"omitempty,gte=0,lte=10000"`
	Enabled         *bool          `json:"enabled"`
	OwnerID         *string        `json:"owner_id" validate:"omitempty,max=255"`
	Metadata        map[string]any `json:"metadata"`
	ReplaceMetadata bool           `json:"replace_metadata"`
}

// ListPoliciesQuery filters the policy list.
type ListPoliciesQuery struct {
	platform.Page
	platform.Sort

	PolicyType   string `json:"policy_type"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Effect       string `json:"effect"`
	Enabled      *bool  `json:"enabled"`
	Search       string `json:"search"`
}

// SortableColumns maps API sort keys onto real columns.
func SortableColumns() map[string]string {
	return map[string]string{
		"name":       "name",
		"slug":       "slug",
		"priority":   "priority",
		"created_at": "created_at",
		"updated_at": "updated_at",
	}
}

// DefaultSort orders by priority, then name.
func DefaultSort() platform.Sort {
	return platform.Sort{By: "priority", Order: "asc"}
}

// CreateContractRequest is the input to CreateContract.
type CreateContractRequest struct {
	DatasetID uuid.UUID      `json:"dataset_id" validate:"required"`
	Name      string         `json:"name" validate:"required,min=1,max=255"`
	Version   string         `json:"version" validate:"omitempty,max=50"`
	Status    ContractStatus `json:"status" validate:"required"`

	SchemaDefinition map[string]any `json:"schema_definition" validate:"required"`

	FreshnessSLAMinutes *int     `json:"freshness_sla_minutes" validate:"omitempty,gt=0"`
	MinRowCount         *int64   `json:"min_row_count" validate:"omitempty,gte=0"`
	MaxNullRate         *float64 `json:"max_null_rate" validate:"omitempty,gte=0,lte=1"`

	ConsumerTeams []string `json:"consumer_teams" validate:"max=100,dive,max=100"`

	BreakingChangePolicy BreakingChangePolicy `json:"breaking_change_policy" validate:"required"`

	OwnerID  string         `json:"owner_id" validate:"max=255"`
	Metadata map[string]any `json:"metadata"`
}

// UpdateContractRequest is a partial contract update.
type UpdateContractRequest struct {
	Name             *string         `json:"name" validate:"omitempty,min=1,max=255"`
	Status           *ContractStatus `json:"status"`
	SchemaDefinition map[string]any  `json:"schema_definition"`

	FreshnessSLAMinutes *int     `json:"freshness_sla_minutes" validate:"omitempty,gt=0"`
	ClearFreshness      bool     `json:"clear_freshness"`
	MinRowCount         *int64   `json:"min_row_count" validate:"omitempty,gte=0"`
	ClearMinRowCount    bool     `json:"clear_min_row_count"`
	MaxNullRate         *float64 `json:"max_null_rate" validate:"omitempty,gte=0,lte=1"`
	ClearMaxNullRate    bool     `json:"clear_max_null_rate"`

	ConsumerTeams        []string              `json:"consumer_teams" validate:"max=100,dive,max=100"`
	ReplaceConsumerTeams bool                  `json:"replace_consumer_teams"`
	BreakingChangePolicy *BreakingChangePolicy `json:"breaking_change_policy"`

	OwnerID         *string        `json:"owner_id" validate:"omitempty,max=255"`
	Metadata        map[string]any `json:"metadata"`
	ReplaceMetadata bool           `json:"replace_metadata"`
}

// ListContractsQuery filters the contract list.
type ListContractsQuery struct {
	platform.Page
	platform.Sort

	DatasetID string `json:"dataset_id"`
	Status    string `json:"status"`
	Search    string `json:"search"`
}

// ContractSortableColumns maps API sort keys onto real columns.
func ContractSortableColumns() map[string]string {
	return map[string]string{
		"name":       "name",
		"version":    "version",
		"status":     "status",
		"created_at": "created_at",
		"updated_at": "updated_at",
	}
}

// ContractDefaultSort orders newest first.
func ContractDefaultSort() platform.Sort {
	return platform.Sort{By: "created_at", Order: "desc"}
}

// isEmpty reports whether a partial policy update would change nothing.
func (r UpdatePolicyRequest) isEmpty() bool {
	return r.Name == nil &&
		r.Description == nil &&
		r.PolicyType == nil &&
		r.Effect == nil &&
		r.RuleExpression == nil &&
		r.AppliesToClassification == nil &&
		r.ResourceType == nil &&
		r.ResourceID == nil &&
		!r.ClearResource &&
		r.Remediation == nil &&
		r.RetentionDays == nil &&
		!r.ClearRetention &&
		r.Priority == nil &&
		r.Enabled == nil &&
		r.OwnerID == nil &&
		r.Metadata == nil
}

// isEmpty reports whether a partial contract update would change nothing.
func (r UpdateContractRequest) isEmpty() bool {
	return r.Name == nil &&
		r.Status == nil &&
		r.SchemaDefinition == nil &&
		r.FreshnessSLAMinutes == nil &&
		!r.ClearFreshness &&
		r.MinRowCount == nil &&
		!r.ClearMinRowCount &&
		r.MaxNullRate == nil &&
		!r.ClearMaxNullRate &&
		r.ConsumerTeams == nil &&
		r.BreakingChangePolicy == nil &&
		r.OwnerID == nil &&
		r.Metadata == nil
}

func normalisePolicyType(v string) PolicyType {
	return PolicyType(strings.ToLower(strings.TrimSpace(v)))
}

func normaliseEffect(v string) Effect {
	return Effect(strings.ToLower(strings.TrimSpace(v)))
}

func normaliseClassification(v string) Classification {
	return Classification(strings.ToLower(strings.TrimSpace(v)))
}

func normaliseResourceType(v string) ResourceType {
	return ResourceType(strings.ToLower(strings.TrimSpace(v)))
}

func normaliseRemediation(v string) Remediation {
	return Remediation(strings.ToLower(strings.TrimSpace(v)))
}

func normaliseContractStatus(v string) ContractStatus {
	return ContractStatus(strings.ToLower(strings.TrimSpace(v)))
}

func normaliseBreakingChangePolicy(v string) BreakingChangePolicy {
	return BreakingChangePolicy(strings.ToLower(strings.TrimSpace(v)))
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
