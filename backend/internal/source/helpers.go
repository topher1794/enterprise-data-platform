package source

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"

	"github.com/edp/edp-control-plane/internal/platform"
)

// validationError converts validator output into a 422 with per-field detail,
// renaming fields to the JSON names the client actually sent.
func validationError(entity string, err error) error {
	var invalid *validator.InvalidValidationError
	if errors.As(err, &invalid) {
		return platform.NewInternal("%s validation could not be performed", entity).WithCause(err)
	}

	errs, ok := err.(validator.ValidationErrors)
	if !ok {
		return platform.NewBadRequest("%s payload is invalid: %s", entity, err.Error())
	}

	out := platform.NewValidation("the %s payload failed validation", entity)
	for _, fe := range errs {
		out = out.WithFields(platform.FieldError{
			Field:   jsonFieldName(fe),
			Rule:    fe.Tag(),
			Message: describeRule(entity, fe),
		})
	}
	return out
}

// jsonFieldName maps a struct field name onto its JSON name using the struct's
// own tag, falling back to a lower-cased name when the tag is absent.
func jsonFieldName(fe validator.FieldError) string {
	name := fe.Field()
	if name == "" {
		return strings.ToLower(fe.StructField())
	}
	return name
}

// describeRule renders a human-readable explanation of a failed rule.
func describeRule(entity string, fe validator.FieldError) string {
	field := jsonFieldName(fe)

	switch fe.Tag() {
	case "required":
		return fmt.Sprintf("%s is required", field)
	case "slug":
		return "must be lowercase alphanumeric words separated by single hyphens"
	case "min":
		return fmt.Sprintf("must be at least %s characters", fe.Param())
	case "max":
		return fmt.Sprintf("must not exceed %s", fe.Param())
	case "email":
		return "must be a valid email address"
	case "dive":
		return "contains an invalid value"
	default:
		return fmt.Sprintf("failed the %q rule", fe.Tag())
	}
}

// fieldError is a small constructor for a single validation failure.
func fieldError(field, rule, message string) platform.FieldError {
	return platform.FieldError{Field: field, Rule: rule, Message: message}
}

// extractID pulls the resource identifier out of a wrapped repository error, so
// the 404 body names the resource the caller asked for.
func extractID(err error) string {
	msg := err.Error()
	idx := strings.LastIndex(msg, ": ")
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(msg[idx+2:])
}

// diffFields reports which top-level fields changed between two records. It is
// used for audit logging and for the "changed" attribute on update events.
func diffFields(before, after Source) []string {
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
	if before.Type != after.Type {
		changed = append(changed, "type")
	}
	if before.Ingestion != after.Ingestion {
		changed = append(changed, "ingestion_mode")
	}
	if before.Environment != after.Environment {
		changed = append(changed, "environment")
	}
	if before.Status != after.Status {
		changed = append(changed, "status")
	}
	if before.ConnectionSecretRef != after.ConnectionSecretRef {
		changed = append(changed, "connection_secret_ref")
	}
	if before.OwnerID != after.OwnerID {
		changed = append(changed, "owner_id")
	}
	if !reflect.DeepEqual(before.Tags, after.Tags) {
		changed = append(changed, "tags")
	}
	if !reflect.DeepEqual(before.ConnectionConfig, after.ConnectionConfig) {
		changed = append(changed, "connection_config")
	}
	if !reflect.DeepEqual(before.Metadata, after.Metadata) {
		changed = append(changed, "metadata")
	}

	return changed
}

// auditSnapshot projects a source into the subset safe to persist in the audit
// trail. The secret reference is reduced to its scheme: an audit reader may be
// broadly privileged, but should not be able to enumerate credential paths.
func auditSnapshot(s Source) map[string]any {
	return map[string]any{
		"name":              s.Name,
		"slug":              s.Slug,
		"type":              s.Type,
		"status":            s.Status,
		"environment":       s.Environment,
		"owner_id":          s.OwnerID,
		"tags":              s.Tags,
		"secret_ref_scheme": secretScheme(s.ConnectionSecretRef),
	}
}

// secretScheme returns the scheme portion of a secret reference.
func secretScheme(ref string) string {
	scheme, _, _ := strings.Cut(ref, "://")
	return scheme
}
