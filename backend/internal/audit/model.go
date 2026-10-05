// Package audit records an append-only trail of every state change made through
// the control plane. It satisfies the Auditor interface declared by the source,
// dataset and pipeline domains.
package audit

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// Outcome is the result of an audited operation.
type Outcome string

// Audit outcomes.
const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
	OutcomeDenied  Outcome = "denied"
)

var allOutcomes = map[Outcome]bool{
	OutcomeSuccess: true, OutcomeFailure: true, OutcomeDenied: true,
}

// Valid reports whether o is a supported outcome.
func (o Outcome) Valid() bool { return allOutcomes[o] }

// Action names are `<entity>.<verb>`; the entity prefix lets a client filter by
// domain without enumerating verbs.
const (
	ActionCreated   = "created"
	ActionUpdated   = "updated"
	ActionDeleted   = "deleted"
	ActionTriggered = "triggered"
	ActionCancelled = "cancelled"
	ActionApproved  = "approved"
	ActionRejected  = "rejected"
)

// Entity types recorded in the trail.
const (
	EntitySource   = "source"
	EntityDataset  = "dataset"
	EntityPipeline = "pipeline"
	EntityTask     = "task"
	EntityQuality  = "quality_rule"
	EntityPolicy   = "policy"
	EntityContract = "data_contract"
	EntityLineage  = "lineage_edge"
	EntityRun      = "pipeline_run"
	EntityAuth     = "authentication"
)

// Event is a single audited state change.
type Event struct {
	ID         uuid.UUID `json:"id"`
	SequenceNo int64     `json:"sequence_no"`
	OccurredAt time.Time `json:"occurred_at"`

	ActorID    string   `json:"actor_id"`
	ActorEmail string   `json:"actor_email"`
	ActorRoles []string `json:"actor_roles"`
	TenantID   string   `json:"tenant_id"`

	Action     string     `json:"action"`
	EntityType string     `json:"entity_type"`
	EntityID   *uuid.UUID `json:"entity_id,omitempty"`
	EntityName string     `json:"entity_name,omitempty"`

	// ChangeSet holds before/after snapshots for updates. Secrets are redacted
	// by the calling domain before they reach here.
	ChangeSet map[string]any `json:"change_set,omitempty"`

	Outcome Outcome `json:"outcome"`

	IPAddress string `json:"ip_address,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`

	DurationMs *int `json:"duration_ms,omitempty"`
}

// RecordRequest is the input to Record. The actor and request metadata are
// resolved from the context rather than supplied by the caller, so a domain
// service cannot attribute an action to the wrong principal.
type RecordRequest struct {
	Action     string
	EntityType string
	EntityID   *uuid.UUID
	EntityName string
	ChangeSet  map[string]any
	Outcome    Outcome
	DurationMs *int
	IPAddress  string
	UserAgent  string
	RequestID  string
	TraceID    string
}

// ListEventsQuery are the filters accepted by the audit query endpoint. Audit
// data is high volume and append-only, so every query must be bounded.
type ListEventsQuery struct {
	platform.Page
	platform.Sort

	ActorID    string `json:"actor_id"`
	TenantID   string `json:"tenant_id"`
	Action     string `json:"action"`
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Outcome    string `json:"outcome"`
	// Since and Until bound occurred_at. Both are inclusive.
	Since *time.Time `json:"since"`
	Until *time.Time `json:"until"`
}

// SortableColumns maps the API's `sort_by` values onto real columns. Only
// append-friendly columns are sortable.
func SortableColumns() map[string]string {
	return map[string]string{
		"occurred_at": "occurred_at",
		"sequence_no": "sequence_no",
		"action":      "action",
	}
}

// DefaultSort returns the newest-first ordering operators expect from an audit
// trail.
func DefaultSort() platform.Sort {
	return platform.Sort{By: "occurred_at", Order: "desc"}
}

// redactKeys are field names whose values must never be written to the audit
// trail, regardless of what a calling domain passes in. This is a backstop: the
// domains already redact their own secrets, and defence in depth matters for a
// table that is meant to be immutable.
var redactKeys = map[string]bool{
	"password":          true,
	"secret":            true,
	"secret_value":      true,
	"connection_string": true,
	"token":             true,
	"access_token":      true,
	"refresh_token":     true,
	"api_key":           true,
	"private_key":       true,
	"client_secret":     true,
	"authorization":     true,
	"credential":        true,
	"credentials":       true,
	"session_token":     true,
	"cookie":            true,
}

// Redact returns a deep copy of changes with sensitive values replaced. The
// input is not modified.
//
// It is deliberately conservative: an unrecognised map key is preserved, since
// domain-specific fields are legitimate audit content, but any key that looks
// like a credential is masked. Keys matching redactKeys are matched after
// lowercasing and stripping underscores, so "clientSecret" and "client_secret"
// are both caught.
func Redact(changes map[string]any) map[string]any {
	if changes == nil {
		return nil
	}
	return redactMap(changes, 0)
}

// maxRedactDepth bounds recursion so a self-referential structure cannot loop.
const maxRedactDepth = 12

func redactMap(in map[string]any, depth int) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		if redactKeys[normaliseKey(key)] {
			out[key] = "[REDACTED]"
			continue
		}
		out[key] = redactValue(value, depth+1)
	}
	return out
}

func redactValue(value any, depth int) any {
	if depth > maxRedactDepth {
		return "[TRUNCATED]"
	}

	switch typed := value.(type) {
	case map[string]any:
		return redactMap(typed, depth)
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, redactMap(item, depth+1))
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, redactValue(item, depth+1))
		}
		return out
	default:
		return value
	}
}

// normaliseKey lowercases a key and removes separators so "clientSecret",
// "client_secret" and "client-secret" all normalise to the same token.
func normaliseKey(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range key {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r == '_' || r == '-' || r == ' ' || r == '.':
			// separator: drop
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// jsonMap decodes a nullable JSONB column, tolerating SQL NULL.
func jsonMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}
