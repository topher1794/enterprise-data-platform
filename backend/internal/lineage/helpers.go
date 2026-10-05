package lineage

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// validateCreateEdge checks a lineage edge request.
//
// Edges are recorded by automated parsers rather than hand-authored forms, so
// this does the strict checks a parser is most likely to get wrong and stays
// quiet about the rest.
func validateCreateEdge(req CreateEdgeRequest) error {
	if req.From.EntityID == uuid.Nil {
		return missingField("from.entity_id", "the producing endpoint needs an id")
	}
	if req.To.EntityID == uuid.Nil {
		return missingField("to.entity_id", "the consuming endpoint needs an id")
	}
	if !req.From.EntityType.Valid() {
		return unsupportedEnum("from.entity_type", req.From.EntityType, listEntityTypes()...)
	}
	if !req.To.EntityType.Valid() {
		return unsupportedEnum("to.entity_type", req.To.EntityType, listEntityTypes()...)
	}
	if !req.TransformType.Valid() {
		return unsupportedEnum("transform_type", req.TransformType, listTransformTypes()...)
	}
	if req.Confidence != nil && (*req.Confidence < 0 || *req.Confidence > 1) {
		return platform.NewValidation("confidence must be between 0 and 1").
			WithFields(fieldError("confidence", "range", "must be between 0 and 1"))
	}

	fromType := normaliseEntityType(string(req.From.EntityType))
	toType := normaliseEntityType(string(req.To.EntityType))
	fromColumn := strings.TrimSpace(req.From.Column)
	toColumn := strings.TrimSpace(req.To.Column)

	// A self-edge is meaningless and the schema rejects it, so catching it here
	// turns a database error into a clear message.
	sameEndpoint := fromType == toType &&
		req.From.EntityID == req.To.EntityID &&
		fromColumn == toColumn
	if sameEndpoint {
		return platform.NewValidation(
			"a lineage edge cannot connect an entity to itself").
			WithFields(fieldError("to", "self_reference",
				"must differ from the from endpoint"))
	}

	// Column-level lineage between entities of different types is usually a
	// parser bug: columns belong to datasets and tables, not to pipelines.
	if fromColumn != "" || toColumn != "" {
		if fromColumn == "" || toColumn == "" {
			return conflictingField("from.column",
				"column-level lineage must name a column on both ends of the edge")
		}
		if fromType == EntityPipeline || fromType == EntityTask ||
			toType == EntityPipeline || toType == EntityTask {
			return conflictingField("column",
				"pipelines and tasks do not have columns; use table-level lineage "+
					"or route the columns through datasets")
		}
	}

	return nil
}

// unsupportedEnum reports an invalid enum value along with the valid set.
func unsupportedEnum(field string, value any, allowed ...string) error {
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

// edgeSnapshot reduces an edge to its audit-relevant fields. The metadata blob
// is included because parsers stash evidence there, which is the context needed
// to judge whether the edge is trustworthy.
func edgeSnapshot(e *Edge) map[string]any {
	if e == nil {
		return nil
	}
	return map[string]any{
		"from":                 endpointLabel(e.From),
		"to":                   endpointLabel(e.To),
		"transform_type":       e.TransformType,
		"transform_expression": e.TransformExpression,
		"confidence":           e.Confidence,
		"observed_at":          e.ObservedAt,
		"observed_by_run_id":   e.ObservedByRunID,
		"metadata":             e.Metadata,
	}
}

// endpointLabel renders an endpoint compactly for logs and audit rows.
func endpointLabel(e Endpoint) string {
	if e.Column == "" {
		return fmt.Sprintf("%s/%s", e.EntityType, e.EntityID)
	}
	return fmt.Sprintf("%s/%s.%s", e.EntityType, e.EntityID, e.Column)
}

func listEntityTypes() []string {
	out := make([]string, 0, len(entityTypes))
	for t := range entityTypes {
		out = append(out, string(t))
	}
	sortStrings(out)
	return out
}

func listTransformTypes() []string {
	out := make([]string, 0, len(transformTypes))
	for t := range transformTypes {
		out = append(out, string(t))
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
