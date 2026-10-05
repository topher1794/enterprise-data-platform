package quality

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// shared is the validator used for both create and partial-update payloads.
// WithRequiredStructEnabled makes `validate:"required"` fire on a zero-valued
// struct, which is what distinguishes an absent field from an explicit empty
// one.
var shared = validator.New(validator.WithRequiredStructEnabled())

// ValidateCreateRule checks a rule creation request. Validation is split
// between struct tags and these semantic checks, because tags cannot express
// rules that depend on more than one field.
func ValidateCreateRule(req CreateRuleRequest) error {
	if err := shared.Struct(req); err != nil {
		return platform.NewValidation("the quality rule payload failed validation").
			WithFields(fieldErrors(err)...)
	}

	if !req.RuleType.Valid() {
		return unsupportedEnum("rule_type", req.RuleType, listRuleTypes())
	}
	if !req.Severity.Valid() {
		return unsupportedEnum("severity", req.Severity, listSeverities())
	}
	if !req.Dimension.Valid() {
		return unsupportedEnum("dimension", req.Dimension, listDimensions())
	}

	// A column-scoped rule without a column is always a mistake, and one that
	// only surfaces when the check silently passes everything.
	if req.RuleType.requiresColumn() && strings.TrimSpace(req.TargetColumn) == "" {
		return missingField("target_column",
			fmt.Sprintf("a %s rule must name the column it validates", req.RuleType))
	}
	// A table-level rule that names a column is contradictory.
	if !req.RuleType.requiresColumn() && strings.TrimSpace(req.TargetColumn) != "" {
		return conflictingField("target_column",
			fmt.Sprintf("a %s rule applies to the whole dataset and must not name a column", req.RuleType))
	}

	if err := validateExpectation(req.RuleType, req.Expectation, req.ExpectationParams); err != nil {
		return err
	}

	return nil
}

// ValidateUpdateRule checks a partial rule update.
func ValidateUpdateRule(req UpdateRuleRequest) error {
	if err := shared.StructPartial(req, validationFieldNames(req)...); err != nil {
		return platform.NewValidation("the quality rule payload failed validation").
			WithFields(fieldErrors(err)...)
	}

	if req.RuleType != nil && !req.RuleType.Valid() {
		return unsupportedEnum("rule_type", *req.RuleType, listRuleTypes())
	}
	if req.Severity != nil && !req.Severity.Valid() {
		return unsupportedEnum("severity", *req.Severity, listSeverities())
	}
	if req.Dimension != nil && !req.Dimension.Valid() {
		return unsupportedEnum("dimension", *req.Dimension, listDimensions())
	}
	if req.TargetColumn != nil && strings.TrimSpace(*req.TargetColumn) == "" && req.RuleType != nil {
		if req.RuleType.requiresColumn() {
			return missingField("target_column",
				fmt.Sprintf("a %s rule must name the column it validates", *req.RuleType))
		}
	}

	return nil
}

// validateExpectation checks the Great Expectations mapping. The expectation
// name is optional; when present it must be a plausible expectation name, and a
// custom_sql rule must not carry one.
func validateExpectation(ruleType RuleType, expectation string, params map[string]any) error {
	trimmed := strings.TrimSpace(expectation)

	if ruleType == RuleCustomSQL {
		// A custom SQL rule is expressed through params, and an expectation
		// name here would be silently ignored by the engine.
		if trimmed != "" {
			return conflictingField("expectation",
				"a custom_sql rule supplies its SQL in expectation_params.query, not in expectation")
		}
		query, _ := params["query"].(string)
		if strings.TrimSpace(query) == "" {
			return missingField("expectation_params.query",
				"a custom_sql rule must supply the SQL to execute")
		}
		return nil
	}

	if trimmed == "" {
		return nil
	}

	// Expectation names follow expect_<subject>_<assertion>; reject anything
	// that is clearly not one rather than passing it to the engine.
	if !strings.HasPrefix(trimmed, "expect_") {
		return platform.NewValidation("expectation %q is not a valid expectation name: "+
			"names follow expect_<subject>_<assertion>", trimmed).
			WithFields(fieldError("expectation", "prefix", "must start with expect_"))
	}
	return nil
}

// fieldErrors flattens validator output into platform field errors.
func fieldErrors(err error) []platform.FieldError {
	var invalid *validator.InvalidValidationError
	if errors.As(err, &invalid) {
		return []platform.FieldError{{
			Field:   "payload",
			Rule:    "invalid",
			Message: "the payload could not be validated",
		}}
	}

	errs, ok := err.(validator.ValidationErrors)
	if !ok {
		return []platform.FieldError{{
			Field: "payload", Rule: "invalid", Message: err.Error(),
		}}
	}

	out := make([]platform.FieldError, 0, len(errs))
	for _, fe := range errs {
		out = append(out, fieldError(fe.Field(), fe.Tag(), ruleMessage(fe)))
	}
	return out
}

// ruleMessage renders a validator failure as a readable sentence.
func ruleMessage(fe validator.FieldError) string {
	field := fe.Field()
	if field == "" {
		field = strings.ToLower(fe.StructField())
	}

	switch fe.Tag() {
	case "required":
		return field + " is required"
	case "min":
		return fmt.Sprintf("%s must be at least %s characters", field, fe.Param())
	case "max":
		return fmt.Sprintf("%s must not exceed %s", field, fe.Param())
	case "gte":
		return fmt.Sprintf("%s must be greater than or equal to %s", field, fe.Param())
	case "lte":
		return fmt.Sprintf("%s must be less than or equal to %s", field, fe.Param())
	case "omitempty":
		return ""
	case "dive":
		return field + " contains an invalid entry"
	default:
		return fmt.Sprintf("%s failed the %q rule", field, fe.Tag())
	}
}

// validationFieldNames lists the fields StructPartial should validate, so an
// absent pointer is not treated as a zero value that fails a tag.
func validationFieldNames(req UpdateRuleRequest) []string {
	names := []string{}
	if req.Name != nil {
		names = append(names, "Name")
	}
	if req.Description != nil {
		names = append(names, "Description")
	}
	if req.Expectation != nil {
		names = append(names, "Expectation")
	}
	if req.FailureThreshold != nil {
		names = append(names, "FailureThreshold")
	}
	if req.TargetColumn != nil {
		names = append(names, "TargetColumn")
	}
	if req.OwnerID != nil {
		names = append(names, "OwnerID")
	}
	if req.Tags != nil {
		names = append(names, "Tags")
	}
	return names
}

// isEmpty reports whether a partial update would change nothing.
func (r UpdateRuleRequest) isEmpty() bool {
	return r.Name == nil &&
		r.Description == nil &&
		r.RuleType == nil &&
		r.Expectation == nil &&
		r.ExpectationParams == nil &&
		r.Severity == nil &&
		r.FailureThreshold == nil &&
		r.Dimension == nil &&
		r.TargetColumn == nil &&
		r.Enabled == nil &&
		r.Blocking == nil &&
		r.PipelineID == nil &&
		!r.ClearPipeline &&
		r.OwnerID == nil &&
		r.Tags == nil &&
		r.Metadata == nil
}

// fieldError builds a single field error.
func fieldError(field, rule, message string) platform.FieldError {
	return platform.FieldError{Field: field, Rule: rule, Message: message}
}

// unsupportedEnum reports an invalid enum value along with the valid set.
func unsupportedEnum(field string, value any, allowed []string) error {
	return platform.NewValidation("%s %v is not valid: expected one of %s",
		field, value, strings.Join(allowed, ", ")).
		WithFields(fieldError(field, "oneof",
			"must be one of: "+strings.Join(allowed, ", ")))
}

// missingField reports a required field that was absent.
func missingField(field, detail string) error {
	return platform.NewValidation("%s is required: %s", field, detail).
		WithFields(fieldError(field, "required", detail))
}

// conflictingField reports two fields that contradict each other.
func conflictingField(field, detail string) error {
	return platform.NewValidation("%s conflicts with the other fields: %s", field, detail).
		WithFields(fieldError(field, "conflict", detail))
}

// listRuleTypes returns the supported rule types for error messages.
func listRuleTypes() []string {
	out := make([]string, 0, len(ruleTypes))
	for t := range ruleTypes {
		out = append(out, string(t))
	}
	sortStrings(out)
	return out
}

func listSeverities() []string {
	out := make([]string, 0, len(severities))
	for s := range severities {
		out = append(out, string(s))
	}
	sortStrings(out)
	return out
}

func listDimensions() []string {
	out := make([]string, 0, len(dimensions))
	for d := range dimensions {
		out = append(out, string(d))
	}
	sortStrings(out)
	return out
}

// sortStrings sorts in place; kept local to avoid pulling in sort for one use.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// ruleSnapshot reduces a rule to its audit-relevant fields. Expectation params
// may contain the raw SQL of a custom_sql rule, which is bulky and may embed
// values, so the snapshot records only its presence.
func ruleSnapshot(r *Rule) map[string]any {
	if r == nil {
		return nil
	}

	owner := ""
	if r.OwnerID != "" {
		owner = r.OwnerID
	}

	return map[string]any{
		"name":              r.Name,
		"rule_type":         r.RuleType,
		"severity":          r.Severity,
		"dimension":         r.Dimension,
		"target_column":     r.TargetColumn,
		"enabled":           r.Enabled,
		"blocking":          r.Blocking,
		"failure_threshold": r.FailureThreshold,
		"owner_id":          owner,
		"dataset_id":        r.DatasetID,
		"pipeline_id":       r.PipelineID,
		"tags":              r.Tags,
		"has_custom_sql":    ruleHasCustomSQL(r),
	}
}

// ruleHasCustomSQL reports whether the rule carries a SQL payload.
func ruleHasCustomSQL(r *Rule) bool {
	if r.RuleType != RuleCustomSQL {
		return false
	}
	query, _ := r.ExpectationParams["query"].(string)
	return strings.TrimSpace(query) != ""
}

// checkRunSnapshot reduces an evaluation to its verdict.
func checkRunSnapshot(c *CheckRun) map[string]any {
	if c == nil {
		return nil
	}
	return map[string]any{
		"rule_id":        c.RuleID,
		"status":         c.Status,
		"violation_rate": c.ViolationRate,
		"failed_count":   c.FailedCount,
		"passed_count":   c.PassedCount,
		"evaluated_at":   c.EvaluatedAt,
	}
}

// validRuleID parses a UUID from a query string.
func validRuleID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, platform.NewBadRequest("%s must be a valid UUID", raw)
	}
	return id, nil
}
