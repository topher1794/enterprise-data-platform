package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/edp/edp-control-plane/internal/config"
	"github.com/edp/edp-control-plane/internal/platform"
)

// Role names recognised by the control plane. Domain code and database rows
// use the same vocabulary, so changing one without the others will silently
// change behaviour; they are declared here once to make that explicit.
const (
	RolePlatformAdmin = "platform-admin"
	RoleDataEngineer  = "data-engineer"
	RoleDataSteward   = "data-steward"
	RoleDataConsumer  = "data-consumer"
	RoleAuditor       = "auditor"
	RoleViewer        = "viewer"
)

// Scopes recognised by the control plane, following the read/write/admin
// convention.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
	ScopeAdmin = "admin"
)

// Authorizer applies role, scope and ownership requirements to a route.
type Authorizer struct {
	cfg config.AuthConfig
}

// NewAuthorizer builds authorization middleware from configuration.
func NewAuthorizer(cfg config.AuthConfig) *Authorizer {
	return &Authorizer{cfg: cfg}
}

// Requirement describes who may invoke a route.
type Requirement struct {
	// AnyRole grants access when the actor holds at least one of these roles.
	// Empty means no role constraint.
	AnyRole []string
	// AllRoles grants access only when the actor holds every listed role.
	AllRoles []string
	// AnyScope grants access when the actor holds at least one scope.
	AnyScope []string
	// AdminOnly requires one of the configured administrative roles.
	AdminOnly bool
}

// Requirements returns a middleware enforcing req.
//
// Authentication-disabled deployments bypass every check, which is what makes
// local development and the test suite usable without a token issuer. The
// bypass is logged loudly at startup so it cannot be mistaken for a
// misconfiguration in production.
func (a *Authorizer) Requirements(req Requirement) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !a.cfg.Enabled {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor, authenticated := platform.ActorFrom(r.Context())
			if !authenticated {
				platform.Fail(w, r, platform.NewUnauthorized("authentication is required"))
				return
			}

			if reason := a.evaluate(actor, req); reason != "" {
				log.WarnContext(r.Context(), "authorization denied",
					"reason", reason,
					"subject", actor.ID,
					"roles", actor.Roles,
					"scopes", actor.Scopes,
					"method", r.Method,
					"path", r.URL.Path,
					"request_id", platform.RequestIDFrom(r.Context()),
				)
				platform.Fail(w, r, platform.NewForbidden(
					"you do not have permission to perform this action"))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// evaluate returns an empty string when the actor satisfies req, or a
// human-readable reason describing the first failed check.
func (a *Authorizer) evaluate(actor platform.Actor, req Requirement) string {
	if req.AdminOnly {
		if !a.holdsAdminRole(actor) {
			return "an administrative role is required"
		}
		// An administrative role implies full access; short-circuit the rest.
		return ""
	}

	if len(req.AllRoles) > 0 {
		for _, role := range req.AllRoles {
			if !actor.HasRole(role) {
				return "role " + role + " is required"
			}
		}
	}

	if len(req.AnyRole) > 0 {
		if !matchesAny(actor.Roles, req.AnyRole) {
			return "one of these roles is required: " + strings.Join(req.AnyRole, ", ")
		}
	}

	if len(req.AnyScope) > 0 {
		if !matchesAny(actor.Scopes, req.AnyScope) {
			return "one of these scopes is required: " + strings.Join(req.AnyScope, ", ")
		}
	}

	return ""
}

func (a *Authorizer) holdsAdminRole(actor platform.Actor) bool {
	if len(a.cfg.AdminRoles) == 0 {
		return actor.HasRole(RolePlatformAdmin)
	}
	for _, role := range a.cfg.AdminRoles {
		if actor.HasRole(role) {
			return true
		}
	}
	return false
}

func matchesAny(held, required []string) bool {
	for _, want := range required {
		for _, have := range held {
			if strings.EqualFold(have, want) {
				return true
			}
		}
	}
	return false
}

// RequireOwnership builds middleware that only lets a caller act on a resource
// they own, unless they hold any of the override roles.
//
// It assumes ownerID has already been loaded into the request context under
// OwnerIDContextKey, typically by the handler after fetching the resource. When
// no owner is present the check is skipped, so a route that forgets to populate
// the key degrades to "allow" rather than to a confusing denial. Populate it
// deliberately; do not rely on the default.
func RequireOwnership(overrideRoles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor, ok := platform.ActorFrom(r.Context())
			if !ok {
				platform.Fail(w, r, platform.NewUnauthorized("authentication is required"))
				return
			}

			ownerID, hasOwner := r.Context().Value(OwnerIDContextKey{}).(string)
			if !hasOwner || ownerID == "" {
				next.ServeHTTP(w, r)
				return
			}

			if actor.ID == ownerID || matchesAny(actor.Roles, overrideRoles) {
				next.ServeHTTP(w, r)
				return
			}

			log.WarnContext(r.Context(), "ownership check failed",
				"subject", actor.ID,
				"owner", ownerID,
				"method", r.Method,
				"path", r.URL.Path,
			)
			platform.Fail(w, r, platform.NewForbidden(
				"only the owner of this resource may modify it"))
		})
	}
}

// OwnerIDContextKey carries the owner subject ID of the resource being acted
// on. Handlers set it with WithOwnerID before calling next.
type OwnerIDContextKey struct{}

// WithOwnerID attaches a resource owner to the request context.
func WithOwnerID(r *http.Request, ownerID string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), OwnerIDContextKey{}, ownerID))
}

// Convenience constructors for the common requirement shapes, so route
// definitions read declaratively.
func (z *Authorizer) Read() func(http.Handler) http.Handler {
	return z.Requirements(Requirement{AnyRole: []string{
		RolePlatformAdmin, RoleDataEngineer, RoleDataSteward, RoleDataConsumer, RoleAuditor, RoleViewer,
	}})
}

func (z *Authorizer) Write() func(http.Handler) http.Handler {
	return z.Requirements(Requirement{AnyRole: []string{
		RolePlatformAdmin, RoleDataEngineer, RoleDataSteward,
	}})
}

func (z *Authorizer) Admin() func(http.Handler) http.Handler {
	return z.Requirements(Requirement{AdminOnly: true})
}

func (z *Authorizer) Scope(scopes ...string) func(http.Handler) http.Handler {
	return z.Requirements(Requirement{AnyScope: scopes})
}
