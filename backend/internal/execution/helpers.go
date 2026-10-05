package execution

import (
	"fmt"
	"strings"

	"github.com/edp/edp-control-plane/internal/platform"
)

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

// fieldError builds a single field error.
func fieldError(field, rule, message string) platform.FieldError {
	return platform.FieldError{Field: field, Rule: rule, Message: message}
}

func listRunStatuses() []string {
	out := make([]string, 0, len(runStatuses))
	for s := range runStatuses {
		out = append(out, string(s))
	}
	sortStrings(out)
	return out
}

func listTaskStatuses() []string {
	out := make([]string, 0, len(taskStatuses))
	for s := range taskStatuses {
		out = append(out, string(s))
	}
	sortStrings(out)
	return out
}

func listTriggerTypes() []string {
	out := make([]string, 0, len(triggerTypes))
	for t := range triggerTypes {
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

// runReport renders a one-line summary, used in log messages.
func runReport(r *Run) string {
	if r == nil {
		return "<nil run>"
	}
	progress := r.Progress()
	return fmt.Sprintf("run %s (%s): %s, %d/%d tasks",
		r.RunID, r.PipelineID, r.Status, progress.Finished, progress.Total)
}
