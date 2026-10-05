package governance

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// validationError converts validator output into a 422 with per-field detail.
func validationError(err error) error {
	var invalid *validator.InvalidValidationError
	if errors.As(err, &invalid) {
		return platform.NewInternal("governance validation could not be performed").WithCause(err)
	}

	errs, ok := err.(validator.ValidationErrors)
	if !ok {
		return platform.NewBadRequest("governance payload is invalid: %s", err.Error())
	}

	out := platform.NewValidation("the governance payload failed validation")
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
		return fmt.Sprintf("must not exceed %s", field)
	case "gt":
		return fmt.Sprintf("%s must be greater than zero", field)
	case "gte":
		return fmt.Sprintf("%s must not be negative", field)
	case "lte":
		return fmt.Sprintf("%s must not exceed %s", field, fe.Param())
	case "dive":
		return "contains an invalid entry"
	default:
		return fmt.Sprintf("failed the %q rule", fe.Tag())
	}
}

// invalidEnum reports an invalid enum value, listing the accepted values.
//
// The accepted values are passed as a variadic rather than a pre-joined string
// so call sites can hand over a vocabulary slice directly.
func invalidEnum(field string, value any, allowed ...string) error {
	hint := "must be one of: " + strings.Join(allowed, ", ")
	return platform.NewValidation("%s %v is not valid: %s", field, value, hint).
		WithFields(fieldError(field, "oneof", hint))
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

// fieldError builds a single field error.
func fieldError(field, rule, message string) platform.FieldError {
	return platform.FieldError{Field: field, Rule: rule, Message: message}
}

// policyUpdateFields lists the fields present in a partial policy update, so
// StructPartial does not validate absent pointers as zero values.
func policyUpdateFields(req UpdatePolicyRequest) []string {
	names := []string{}
	for _, pair := range []struct {
		set  bool
		name string
	}{
		{req.Name != nil, "Name"},
		{req.Description != nil, "Description"},
		{req.RuleExpression != nil, "RuleExpression"},
		{req.RetentionDays != nil, "RetentionDays"},
		{req.Priority != nil, "Priority"},
		{req.OwnerID != nil, "OwnerID"},
	} {
		if pair.set {
			names = append(names, pair.name)
		}
	}
	return names
}

// contractUpdateFields lists the fields present in a partial contract update.
func contractUpdateFields(req UpdateContractRequest) []string {
	names := []string{}
	for _, pair := range []struct {
		set  bool
		name string
	}{
		{req.Name != nil, "Name"},
		{req.FreshnessSLAMinutes != nil, "FreshnessSLAMinutes"},
		{req.MinRowCount != nil, "MinRowCount"},
		{req.MaxNullRate != nil, "MaxNullRate"},
		{req.ConsumerTeams != nil, "ConsumerTeams"},
		{req.OwnerID != nil, "OwnerID"},
	} {
		if pair.set {
			names = append(names, pair.name)
		}
	}
	return names
}

// resolveSlug picks the slug for a new policy, deriving one from the name when
// none is supplied.
//
// A caller-supplied slug that is already taken is rejected rather than
// silently suffixed: an explicit slug is usually chosen because something
// external refers to it by that name, and quietly rewriting it would break
// those references. A derived slug that collides is fine to suffix, because
// nobody is depending on it yet.
func (s *Service) resolveSlug(ctx context.Context, requested, name string) (string, error) {
	trimmed := strings.TrimSpace(requested)

	if trimmed == "" {
		trimmed = slugify(name)
		if trimmed == "" {
			return "", platform.NewValidation(
				"the policy name does not contain any characters usable in a slug; " +
					"supply an explicit slug")
		}

		candidate := trimmed
		for attempt := 2; attempt <= 10; attempt++ {
			taken, err := s.repo.SlugExists(ctx, candidate, uuid.Nil)
			if err != nil {
				return "", platform.AsError(err)
			}
			if !taken {
				return candidate, nil
			}
			candidate = fmt.Sprintf("%s-%d", trimmed, attempt)
		}
		return "", platform.NewConflict(
			"could not derive a free slug from the policy name; supply an explicit slug")
	}

	taken, err := s.repo.SlugExists(ctx, trimmed, uuid.Nil)
	if err != nil {
		return "", platform.AsError(err)
	}
	if taken {
		return "", platform.NewConflict("a policy with slug %q already exists", trimmed)
	}
	return trimmed, nil
}

// slugify derives a slug from a display name.
func slugify(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))

	var b strings.Builder
	b.Grow(len(lower))

	prevHyphen := false
	for _, r := range lower {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}

	slug := strings.Trim(b.String(), "-")
	if len(slug) > maxSlugLength {
		slug = strings.Trim(slug[:maxSlugLength], "-")
	}
	return slug
}

// policySnapshot reduces a policy to its audit-relevant fields. The rule
// expression is included because changing it is the single most consequential
// edit to a policy.
func policySnapshot(p *Policy) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"name":                      p.Name,
		"slug":                      p.Slug,
		"policy_type":               p.PolicyType,
		"effect":                    p.Effect,
		"rule_expression":           p.RuleExpression,
		"applies_to_classification": p.AppliesToClassification,
		"resource_type":             p.ResourceType,
		"resource_id":               p.ResourceID,
		"remediation":               p.Remediation,
		"retention_days":            p.RetentionDays,
		"priority":                  p.Priority,
		"enabled":                   p.Enabled,
		"owner_id":                  p.OwnerID,
	}
}

// contractSnapshot reduces a contract to its audit-relevant fields. The full
// schema definition is reduced to a column count, since it can be large and
// changes on every schema tweak.
func contractSnapshot(c *Contract) map[string]any {
	if c == nil {
		return nil
	}

	columnCount := 0
	if raw, ok := c.SchemaDefinition["columns"].([]any); ok {
		columnCount = len(raw)
	}

	return map[string]any{
		"name":                   c.Name,
		"version":                c.Version,
		"status":                 c.Status,
		"dataset_id":             c.DatasetID,
		"consumer_teams":         c.ConsumerTeams,
		"breaking_change_policy": c.BreakingChangePolicy,
		"freshness_sla_minutes":  c.FreshnessSLAMinutes,
		"min_row_count":          c.MinRowCount,
		"max_null_rate":          c.MaxNullRate,
		"column_count":           columnCount,
		"owner_id":               c.OwnerID,
	}
}

// normaliseList trims, drops empties and de-duplicates a list of strings.
func normaliseList(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			continue
		}
		if _, dup := seen[trimmed]; dup {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// mergeLists appends incoming entries not already present.
func mergeLists(existing, incoming []string) []string {
	return normaliseList(append(append([]string{}, existing...), incoming...))
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

// firstNonEmpty returns the first non-empty value, or "".
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// policySnapshotForAudit is used by the handler's read paths, where the full
// policy is safe to expose.
func policySnapshotForAudit(p Policy) map[string]any { return policySnapshot(&p) }

func listPolicyTypes() []string {
	out := make([]string, 0, len(policyTypes))
	for t := range policyTypes {
		out = append(out, string(t))
	}
	sortStrings(out)
	return out
}

func listClassifications() []string {
	out := make([]string, 0, len(classifications))
	for c := range classifications {
		out = append(out, string(c))
	}
	sortStrings(out)
	return out
}

func listResourceTypes() []string {
	out := make([]string, 0, len(resourceTypes))
	for t := range resourceTypes {
		out = append(out, string(t))
	}
	sortStrings(out)
	return out
}

func listRemediations() []string {
	out := make([]string, 0, len(remediations))
	for r := range remediations {
		out = append(out, string(r))
	}
	sortStrings(out)
	return out
}

func listContractStatuses() []string {
	out := make([]string, 0, len(contractStatuses))
	for s := range contractStatuses {
		out = append(out, string(s))
	}
	sortStrings(out)
	return out
}

// sortStrings sorts in place, keeping the package free of a sort import for
// what are short, fixed vocabularies.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
