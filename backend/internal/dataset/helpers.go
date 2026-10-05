package dataset

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"

	"github.com/edp/edp-control-plane/internal/platform"
)

// validationError converts validator output into a 422 with per-field detail
// named by the JSON field the client actually sent.
func validationError(err error) error {
	var invalid *validator.InvalidValidationError
	if errors.As(err, &invalid) {
		return platform.NewInternal("dataset validation could not be performed").WithCause(err)
	}

	errs, ok := err.(validator.ValidationErrors)
	if !ok {
		return platform.NewBadRequest("dataset payload is invalid: %s", err.Error())
	}

	out := platform.NewValidation("the dataset payload failed validation")
	for _, fe := range errs {
		out = out.WithFields(platform.FieldError{
			Field:   jsonFieldName(fe),
			Rule:    fe.Tag(),
			Message: describeRule(fe),
		})
	}
	return out
}

func jsonFieldName(fe validator.FieldError) string {
	name := fe.Field()
	if name == "" {
		return strings.ToLower(fe.StructField())
	}
	return name
}

func describeRule(fe validator.FieldError) string {
	field := jsonFieldName(fe)

	switch fe.Tag() {
	case "required":
		return fmt.Sprintf("%s is required", field)
	case "min":
		return fmt.Sprintf("must be at least %s characters", fe.Param())
	case "max":
		return fmt.Sprintf("must not exceed %s", fe.Param())
	case "dive":
		return "contains an invalid value"
	default:
		return fmt.Sprintf("failed the %q rule", fe.Tag())
	}
}

// invalidEnum builds a validation error for an unsupported enum value or an
// out-of-range number.
func invalidEnum(field string, value any, hint string) error {
	return platform.NewValidation("%s %v is not valid: %s", field, value, hint).
		WithFields(platform.FieldError{
			Field:   field,
			Rule:    "oneof",
			Message: hint,
		})
}

// fieldError is a small constructor for a single validation failure.
func fieldError(field, rule, message string) platform.FieldError {
	return platform.FieldError{Field: field, Rule: rule, Message: message}
}

// extractID pulls the resource identifier out of a wrapped repository error.
func extractID(err error) string {
	msg := err.Error()
	idx := strings.LastIndex(msg, ": ")
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(msg[idx+2:])
}

// diffFields reports which top-level fields changed between two datasets.
func diffFields(before, after Dataset) []string {
	var changed []string

	if before.Name != after.Name {
		changed = append(changed, "name")
	}
	if before.Slug != after.Slug {
		changed = append(changed, "slug")
	}
	if before.Description != after.Description {
		changed = append(changed, "description")
	}
	if before.Kind != after.Kind {
		changed = append(changed, "kind")
	}
	if before.Classification != after.Classification {
		changed = append(changed, "classification")
	}
	if before.Status != after.Status {
		changed = append(changed, "status")
	}
	if !reflect.DeepEqual(before.SourceID, after.SourceID) {
		changed = append(changed, "source_id")
	}
	if !reflect.DeepEqual(before.PhysicalLocation, after.PhysicalLocation) {
		changed = append(changed, "physical_location")
	}
	if before.Domain != after.Domain {
		changed = append(changed, "domain")
	}
	if before.RetentionDays != after.RetentionDays {
		changed = append(changed, "retention_days")
	}
	if before.SchemaVersion != after.SchemaVersion {
		changed = append(changed, "schema_version")
	}
	if before.OwnerID != after.OwnerID {
		changed = append(changed, "owner_id")
	}
	if before.StewardID != after.StewardID {
		changed = append(changed, "steward_id")
	}
	if !reflect.DeepEqual(before.Tags, after.Tags) {
		changed = append(changed, "tags")
	}
	if !reflect.DeepEqual(before.Metadata, after.Metadata) {
		changed = append(changed, "metadata")
	}

	return changed
}

// auditSnapshot projects a dataset into the subset safe for the audit trail.
// Column-level detail is recorded as counts rather than full definitions, which
// keeps audit rows small.
func auditSnapshot(d Dataset) map[string]any {
	sensitive := 0
	for _, c := range d.Columns {
		if c.IsSensitive || c.PIIClass.Sensitive() {
			sensitive++
		}
	}

	snapshot := map[string]any{
		"name":           d.Name,
		"slug":           d.Slug,
		"kind":           d.Kind,
		"classification": d.Classification,
		"status":         d.Status,
		"owner_id":       d.OwnerID,
		"steward_id":     d.StewardID,
		"domain":         d.Domain,
		"tags":           d.Tags,
		"schema_version": d.SchemaVersion,
	}

	if len(d.Columns) > 0 {
		snapshot["column_count"] = len(d.Columns)
		snapshot["sensitive_column_count"] = sensitive
	}
	return snapshot
}
