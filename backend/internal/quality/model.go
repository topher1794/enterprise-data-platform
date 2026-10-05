// Package quality manages data quality rules attached to datasets and the
// recorded results of evaluating them.
package quality

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// RuleType identifies the kind of check performed.
type RuleType string

// Supported rule types.
const (
	RuleNotNull              RuleType = "not_null"
	RuleUnique               RuleType = "unique"
	RuleRange                RuleType = "range"
	RuleRegex                RuleType = "regex"
	RuleReferentialIntegrity RuleType = "referential_integrity"
	RuleFreshness            RuleType = "freshness"
	RuleVolume               RuleType = "volume"
	RuleCustomSQL            RuleType = "custom_sql"
	RuleStatistical          RuleType = "statistical"
)

var ruleTypes = map[RuleType]bool{
	RuleNotNull: true, RuleUnique: true, RuleRange: true, RuleRegex: true,
	RuleReferentialIntegrity: true, RuleFreshness: true, RuleVolume: true,
	RuleCustomSQL: true, RuleStatistical: true,
}

// Valid reports whether t is a supported rule type.
func (t RuleType) Valid() bool { return ruleTypes[t] }

// requiresColumn reports whether a rule type only makes sense against a
// specific column. Enforcing this catches a whole class of misconfiguration
// where a column-scoped rule is registered with no column name.
func (t RuleType) requiresColumn() bool {
	switch t {
	case RuleNotNull, RuleUnique, RuleRange, RuleRegex, RuleStatistical:
		return true
	default:
		return false
	}
}

// Severity controls how loudly a failure is reported.
type Severity string

// Supported severities, ordered by escalation.
const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

var severities = map[Severity]bool{
	SeverityInfo: true, SeverityWarning: true,
	SeverityError: true, SeverityCritical: true,
}

// Valid reports whether s is a supported severity.
func (s Severity) Valid() bool { return severities[s] }

// rank gives a comparable weight for escalation decisions.
func (s Severity) rank() int {
	switch s {
	case SeverityInfo:
		return 1
	case SeverityWarning:
		return 2
	case SeverityError:
		return 3
	case SeverityCritical:
		return 4
	default:
		return 0
	}
}

// Dimension is the data quality facet a rule protects.
type Dimension string

// Supported quality dimensions.
const (
	DimensionAccuracy     Dimension = "accuracy"
	DimensionCompleteness Dimension = "completeness"
	DimensionConsistency  Dimension = "consistency"
	DimensionTimeliness   Dimension = "timeliness"
	DimensionUniqueness   Dimension = "uniqueness"
	DimensionValidity     Dimension = "validity"
	DimensionVolume       Dimension = "volume"
)

var dimensions = map[Dimension]bool{
	DimensionAccuracy: true, DimensionCompleteness: true,
	DimensionConsistency: true, DimensionTimeliness: true,
	DimensionUniqueness: true, DimensionValidity: true, DimensionVolume: true,
}

// Valid reports whether d is a supported dimension.
func (d Dimension) Valid() bool { return dimensions[d] }

// Rule is a data quality check bound to a dataset.
type Rule struct {
	ID          uuid.UUID `json:"id"`
	DatasetID   uuid.UUID `json:"dataset_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`

	RuleType RuleType `json:"rule_type"`

	// Expectation and ExpectationParams follow the Great Expectations
	// vocabulary so existing suites can be translated without rework.
	Expectation       string         `json:"expectation"`
	ExpectationParams map[string]any `json:"expectation_params"`

	Severity         Severity  `json:"severity"`
	FailureThreshold float64   `json:"failure_threshold"`
	Dimension        Dimension `json:"dimension"`

	TargetColumn string `json:"target_column"`

	Enabled  bool `json:"enabled"`
	Blocking bool `json:"blocking"`

	// PipelineID is set when the rule gates a pipeline run.
	PipelineID *uuid.UUID `json:"pipeline_id,omitempty"`

	OwnerID  string         `json:"owner_id"`
	Tags     []string       `json:"tags"`
	Metadata map[string]any `json:"metadata"`

	CreatedBy string     `json:"created_by"`
	UpdatedBy string     `json:"updated_by"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// CheckStatus is the outcome of one rule evaluation.
type CheckStatus string

// Check statuses.
const (
	CheckPassed  CheckStatus = "passed"
	CheckWarning CheckStatus = "warning"
	CheckFailed  CheckStatus = "failed"
	CheckError   CheckStatus = "error"
	CheckSkipped CheckStatus = "skipped"
)

var checkStatuses = map[CheckStatus]bool{
	CheckPassed: true, CheckWarning: true, CheckFailed: true,
	CheckError: true, CheckSkipped: true,
}

// Valid reports whether s is a supported check status.
func (s CheckStatus) Valid() bool { return checkStatuses[s] }

// terminal reports whether the evaluation has finished. An error is terminal
// but distinct from a failure: an error means the check could not run, which
// carries different meaning for a blocking gate.
func (s CheckStatus) terminal() bool { return s != CheckQueued }

// CheckQueued is the pre-evaluation state, used when building a pending run.
const CheckQueued CheckStatus = "queued"

// CheckRun is the recorded result of evaluating one rule once.
type CheckRun struct {
	ID            uuid.UUID  `json:"id"`
	RuleID        uuid.UUID  `json:"rule_id"`
	DatasetID     uuid.UUID  `json:"dataset_id"`
	PipelineRunID *uuid.UUID `json:"pipeline_run_id,omitempty"`

	Status CheckStatus `json:"status"`

	ObservedValue *float64 `json:"observed_value,omitempty"`
	ExpectedValue string   `json:"expected_value"`

	RowsEvaluated *int64 `json:"rows_evaluated,omitempty"`
	// ViolationRate is the fraction of evaluated rows that broke the rule.
	ViolationRate *float64 `json:"violation_rate,omitempty"`
	PassedCount   int64    `json:"passed_count"`
	FailedCount   int64    `json:"failed_count"`

	Message    string         `json:"message"`
	Details    map[string]any `json:"details"`
	DurationMs *int           `json:"duration_ms,omitempty"`

	EvaluatedAt time.Time `json:"evaluated_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// CreateRuleRequest is the input to CreateRule.
type CreateRuleRequest struct {
	DatasetID uuid.UUID `json:"dataset_id" validate:"required"`
	Name      string    `json:"name" validate:"required,min=1,max=255"`
	// Description is free text explaining the intent of the rule.
	Description string `json:"description" validate:"max=2000"`

	RuleType RuleType `json:"rule_type" validate:"required"`

	// Expectation and ExpectationParams carry the Great Expectations mapping.
	Expectation       string         `json:"expectation" validate:"max=200"`
	ExpectationParams map[string]any `json:"expectation_params"`

	Severity         Severity  `json:"severity" validate:"required"`
	FailureThreshold float64   `json:"failure_threshold" validate:"gte=0,lte=1"`
	Dimension        Dimension `json:"dimension" validate:"required"`

	TargetColumn string `json:"target_column" validate:"max=255"`

	// Enabled defaults to true; Blocking defaults to true. Both are pointers so
	// an omitted field is distinguishable from an explicit false.
	Enabled  *bool `json:"enabled"`
	Blocking *bool `json:"blocking"`

	PipelineID *uuid.UUID     `json:"pipeline_id"`
	OwnerID    string         `json:"owner_id" validate:"max=255"`
	Tags       []string       `json:"tags" validate:"max=50,dive,max=100"`
	Metadata   map[string]any `json:"metadata"`
}

// UpdateRuleRequest is a partial update. Every field is optional; the service
// rejects an empty body.
type UpdateRuleRequest struct {
	Name              *string        `json:"name" validate:"omitempty,min=1,max=255"`
	Description       *string        `json:"description" validate:"omitempty,max=2000"`
	RuleType          *RuleType      `json:"rule_type"`
	Expectation       *string        `json:"expectation" validate:"omitempty,max=200"`
	ExpectationParams map[string]any `json:"expectation_params"`
	Severity          *Severity      `json:"severity"`
	FailureThreshold  *float64       `json:"failure_threshold" validate:"omitempty,gte=0,lte=1"`
	Dimension         *Dimension     `json:"dimension"`
	TargetColumn      *string        `json:"target_column" validate:"omitempty,max=255"`
	Enabled           *bool          `json:"enabled"`
	Blocking          *bool          `json:"blocking"`
	PipelineID        *uuid.UUID     `json:"pipeline_id"`
	ClearPipeline     bool           `json:"clear_pipeline"`
	OwnerID           *string        `json:"owner_id" validate:"omitempty,max=255"`
	Tags              []string       `json:"tags" validate:"max=50,dive,max=100"`
	ReplaceTags       bool           `json:"replace_tags"`
	Metadata          map[string]any `json:"metadata"`
	ReplaceMetadata   bool           `json:"replace_metadata"`
}

// ListRulesQuery filters the rule list.
type ListRulesQuery struct {
	platform.Page
	platform.Sort

	DatasetID  string   `json:"dataset_id"`
	PipelineID string   `json:"pipeline_id"`
	RuleType   []string `json:"rule_type"`
	Severity   []string `json:"severity"`
	Dimension  []string `json:"dimension"`
	Enabled    *bool    `json:"enabled"`
	Blocking   *bool    `json:"blocking"`
	Search     string   `json:"search"`
	Tag        []string `json:"tag"`
}

// SortableColumns maps API sort keys onto real columns.
func SortableColumns() map[string]string {
	return map[string]string{
		"name":       "name",
		"created_at": "created_at",
		"updated_at": "updated_at",
		"severity":   "severity",
		"rule_type":  "rule_type",
		"dimension":  "dimension",
	}
}

// DefaultSort orders newest first.
func DefaultSort() platform.Sort {
	return platform.Sort{By: "created_at", Order: "desc"}
}

// ListCheckRunsQuery filters recorded evaluations.
type ListCheckRunsQuery struct {
	platform.Page
	platform.Sort

	RuleID        string   `json:"rule_id"`
	DatasetID     string   `json:"dataset_id"`
	PipelineRunID string   `json:"pipeline_run_id"`
	Status        []string `json:"status"`
	// Since and Until bound evaluated_at. Both inclusive.
	Since *time.Time `json:"since"`
	Until *time.Time `json:"until"`
}

// CheckRunSortableColumns maps API sort keys onto real columns.
func CheckRunSortableColumns() map[string]string {
	return map[string]string{
		"evaluated_at": "evaluated_at",
		"created_at":   "created_at",
		"status":       "status",
	}
}

// CheckRunDefaultSort orders newest first.
func CheckRunDefaultSort() platform.Sort {
	return platform.Sort{By: "evaluated_at", Order: "desc"}
}

// RecordCheckRequest is the input to RecordCheck, written by the engine that
// actually evaluates the rule.
type RecordCheckRequest struct {
	RuleID uuid.UUID   `json:"rule_id" validate:"required"`
	Status CheckStatus `json:"status" validate:"required"`

	ObservedValue *float64 `json:"observed_value"`
	ExpectedValue string   `json:"expected_value" validate:"max=1000"`

	RowsEvaluated *int64   `json:"rows_evaluated" validate:"omitempty,gte=0"`
	ViolationRate *float64 `json:"violation_rate" validate:"omitempty,gte=0,lte=1"`
	PassedCount   int64    `json:"passed_count" validate:"gte=0"`
	FailedCount   int64    `json:"failed_count" validate:"gte=0"`

	Message    string         `json:"message" validate:"max=4000"`
	Details    map[string]any `json:"details"`
	DurationMs *int           `json:"duration_ms" validate:"omitempty,gte=0"`
}

// RuleHealth summarises how a dataset's rules are currently performing.
type RuleHealth struct {
	DatasetID uuid.UUID `json:"dataset_id"`

	TotalRules    int `json:"total_rules"`
	EnabledRules  int `json:"enabled_rules"`
	BlockingRules int `json:"blocking_rules"`

	Passing int `json:"passing"`
	Failing int `json:"failing"`
	// NotEvaluated counts rules that have never been evaluated.
	NotEvaluated int `json:"not_evaluated"`

	// PassedRate is over evaluated rules only, so a dataset with no history
	// reports nil rather than a misleading 0%.
	PassedRate *float64 `json:"passed_rate,omitempty"`
	// WorstSeverity is the highest severity among failing rules, empty when
	// nothing is failing.
	WorstSeverity Severity `json:"worst_severity,omitempty"`
}

// normaliseRuleType lowercases and trims a rule type from untrusted input.
func normaliseRuleType(v string) RuleType {
	return RuleType(strings.ToLower(strings.TrimSpace(v)))
}

func normaliseSeverity(v string) Severity {
	return Severity(strings.ToLower(strings.TrimSpace(v)))
}

func normaliseDimension(v string) Dimension {
	return Dimension(strings.ToLower(strings.TrimSpace(v)))
}

func normaliseCheckStatus(v string) CheckStatus {
	return CheckStatus(strings.ToLower(strings.TrimSpace(v)))
}

func normaliseTags(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}

	out := make([]string, 0, len(values))
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// decodeJSONMap converts a nullable JSONB column into a map, tolerating NULL
// and malformed bytes rather than failing the whole read.
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

// appliesToColumn reports whether the rule's type needs a target column.
func (r Rule) appliesToColumn() bool { return r.RuleType.requiresColumn() }
