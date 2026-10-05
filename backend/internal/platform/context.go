package platform

import (
	"context"
	"net/http"
	"regexp"
	"strings"
)

type contextKey string

const (
	ctxKeyRequestID contextKey = "edp.request_id"
	ctxKeyActor     contextKey = "edp.actor"
)

// requestIDPattern matches the UUID form emitted by RequestID middleware.
// Externally supplied IDs that do not conform are rejected to prevent log
// injection and unbounded header growth.
var requestIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsValidRequestID reports whether id is a well-formed UUID.
func IsValidRequestID(id string) bool { return requestIDPattern.MatchString(id) }

// WithRequestID stores a correlation ID on the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

// RequestIDFrom retrieves the correlation ID, or "" when absent.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

// Actor is the authenticated principal on whose behalf a request is served.
// It is populated by the authentication middleware and is the basis for
// authorization decisions and audit records.
type Actor struct {
	ID          string   `json:"id"`
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name,omitempty"`
	TenantID    string   `json:"tenant_id,omitempty"`
	Roles       []string `json:"roles"`
	Scopes      []string `json:"scopes"`
}

// HasRole reports whether the actor holds the named role (case-insensitive).
func (a Actor) HasRole(role string) bool {
	for _, r := range a.Roles {
		if strings.EqualFold(r, role) {
			return true
		}
	}
	return false
}

// HasScope reports whether the actor holds the named OAuth scope.
func (a Actor) HasScope(scope string) bool {
	for _, s := range a.Scopes {
		if strings.EqualFold(s, scope) {
			return true
		}
	}
	return false
}

// WithActor stores the authenticated principal on the context.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, ctxKeyActor, a)
}

// ActorFrom retrieves the authenticated principal. ok is false for anonymous
// requests, so callers can distinguish unauthenticated from unauthorized.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(ctxKeyActor).(Actor)
	return a, ok
}

// MustActor retrieves the authenticated principal and panics when absent. It is
// only for handlers already guarded by RequireAuthentication.
func MustActor(ctx context.Context) Actor {
	a, ok := ActorFrom(ctx)
	if !ok {
		panic("platform: no actor on context; route is missing authentication middleware")
	}
	return a
}

// RequestIDHeaderValue returns the inbound correlation header value, if any.
func RequestIDHeaderValue(r *http.Request) string {
	return r.Header.Get(RequestIDHeader)
}
