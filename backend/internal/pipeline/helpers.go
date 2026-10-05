package pipeline

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"

	"github.com/edp/edp-control-plane/internal/platform"
)

// validationError converts validator output into a 422 with per-field detail.
func validationError(err error) error {
	var invalid *validator.InvalidValidationError
	if errors.As(err, &invalid) {
		return platform.NewInternal("pipeline validation could not be performed").WithCause(err)
	}

	errs, ok := err.(validator.ValidationErrors)
	if !ok {
		return platform.NewBadRequest("pipeline payload is invalid: %s", err.Error())
	}

	out := platform.NewValidation("the pipeline payload failed validation")
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

// invalidEnum builds a validation error for an unsupported enum value.
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

// diffFields reports which top-level fields changed between two pipelines.
func diffFields(before, after Pipeline) []string {
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
	if before.DAGID != after.DAGID {
		changed = append(changed, "dag_id")
	}
	if before.Status != after.Status {
		changed = append(changed, "status")
	}
	if before.Schedule != after.Schedule {
		changed = append(changed, "schedule")
	}
	if before.Timezone != after.Timezone {
		changed = append(changed, "timezone")
	}
	if before.MaxActiveRuns != after.MaxActiveRuns {
		changed = append(changed, "max_active_runs")
	}
	if before.Catchup != after.Catchup {
		changed = append(changed, "catchup")
	}
	if !reflect.DeepEqual(before.DefaultDatasetID, after.DefaultDatasetID) {
		changed = append(changed, "default_dataset_id")
	}
	if before.OwnerID != after.OwnerID {
		changed = append(changed, "owner_id")
	}
	if !reflect.DeepEqual(before.Tags, after.Tags) {
		changed = append(changed, "tags")
	}
	if before.GraphHash != after.GraphHash {
		changed = append(changed, "graph_hash")
	}
	if !reflect.DeepEqual(before.Metadata, after.Metadata) {
		changed = append(changed, "metadata")
	}

	return changed
}

// auditSnapshot projects a pipeline into the subset safe for the audit trail.
// Task definitions are reduced to counts: a full dump would bloat audit rows
// and can contain connection details from operator_config.
func auditSnapshot(p Pipeline) map[string]any {
	enabled := 0
	critical := 0
	for _, t := range p.Tasks {
		if t.Enabled {
			enabled++
		}
		if t.IsCritical {
			critical++
		}
	}

	return map[string]any{
		"name":           p.Name,
		"slug":           p.Slug,
		"status":         p.Status,
		"schedule":       p.Schedule,
		"timezone":       p.Timezone,
		"owner_id":       p.OwnerID,
		"tags":           p.Tags,
		"graph_hash":     p.GraphHash,
		"task_count":     len(p.Tasks),
		"edge_count":     len(p.Dependencies),
		"enabled_tasks":  enabled,
		"critical_tasks": critical,
		"has_dag_id":     p.DAGID != "",
	}
}
