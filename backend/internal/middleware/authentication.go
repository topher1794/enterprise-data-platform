// Package middleware provides the HTTP middleware chain: correlation, logging,
// panic recovery, authentication and authorization.
package middleware

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/edp/edp-control-plane/internal/config"
	"github.com/edp/edp-control-plane/internal/platform"
)

// TokenVerifier turns a raw bearer token into a validated principal. It is an
// interface so tests can substitute a stub without standing up a JWKS endpoint.
type TokenVerifier interface {
	// Verify parses and validates the token, returning its claims. An invalid
	// or expired token must return ErrInvalidToken.
	Verify(ctx context.Context, token string) (*Claims, error)
}

// ErrInvalidToken is the sentinel returned for any token that fails
// verification. The underlying reason is logged, never returned to the client,
// so it cannot be used as an oracle for distinguishing expiry from forgery.
var ErrInvalidToken = errors.New("invalid token")

// Claims is the validated token payload in the shape the control plane needs.
type Claims struct {
	Subject   string
	Email     string
	Name      string
	TenantID  string
	Roles     []string
	Scopes    []string
	ExpiresAt time.Time
	Issuer    string
}

// Actor converts claims into the request-scoped principal.
func (c *Claims) Actor() platform.Actor {
	return platform.Actor{
		ID:          c.Subject,
		Email:       c.Email,
		DisplayName: c.Name,
		TenantID:    c.TenantID,
		Roles:       c.Roles,
		Scopes:      c.Scopes,
	}
}

// Authenticator verifies bearer tokens and populates the request context with
// the resulting principal.
type Authenticator struct {
	verifier TokenVerifier
	cfg      config.AuthConfig
	log      *slog.Logger
}

// NewAuthenticator builds authentication middleware around verifier.
func NewAuthenticator(verifier TokenVerifier, cfg config.AuthConfig, logger *slog.Logger) *Authenticator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Authenticator{verifier: verifier, cfg: cfg, log: logger}
}

// Middleware returns a handler that authenticates the request.
//
// When authentication is disabled the request proceeds unauthenticated, which
// keeps local development and the test suite free of token plumbing. Routes
// that must always carry a principal should additionally be wrapped in
// RequireAuthentication.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.cfg.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		raw, err := bearerToken(r)
		if err != nil {
			a.reject(w, r, err)
			return
		}

		claims, err := a.verifier.Verify(r.Context(), raw)
		if err != nil {
			a.log.WarnContext(r.Context(), "token verification failed",
				"error", err,
				"request_id", platform.RequestIDFrom(r.Context()),
				"remote_addr", clientIP(r),
			)
			a.reject(w, r, ErrInvalidToken)
			return
		}
		if claims == nil || claims.Subject == "" {
			a.reject(w, r, errors.New("token is missing a subject claim"))
			return
		}

		ctx := platform.WithActor(r.Context(), claims.Actor())
		a.log.DebugContext(ctx, "request authenticated",
			"subject", claims.Subject,
			"roles", claims.Roles,
			"expires_at", claims.ExpiresAt,
		)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAuthentication rejects anonymous requests. Place it on a route group
// that must carry a principal regardless of whether auth is globally enabled.
func RequireAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := platform.ActorFrom(r.Context()); !ok {
			platform.Fail(w, r, platform.NewUnauthorized("authentication is required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// reject writes a 401 with a WWW-Authenticate challenge. The underlying reason
// is logged rather than returned, so it cannot serve as an oracle for
// distinguishing an expired token from a forged one.
func (a *Authenticator) reject(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="edp-control-plane", error="invalid_token"`)
	platform.Fail(w, r, platform.NewUnauthorized("a valid bearer token is required").WithCause(err))
}

// bearerToken extracts the token from the Authorization header, or from the
// `access_token` query parameter used only by WebSocket and EventSource clients
// that cannot set headers.
func bearerToken(r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if header != "" {
		scheme, token, found := strings.Cut(header, " ")
		if !found || !strings.EqualFold(scheme, "Bearer") {
			return "", fmt.Errorf("authorization header must use the Bearer scheme")
		}
		token = strings.TrimSpace(token)
		if token == "" {
			return "", errors.New("bearer token is empty")
		}
		return token, nil
	}

	// Query-parameter tokens are only honoured on GET, so a crafted link cannot
	// make a browser leak a token via a state-changing request.
	if r.Method == http.MethodGet {
		if token := strings.TrimSpace(r.URL.Query().Get("access_token")); token != "" {
			return token, nil
		}
	}

	return "", errors.New("authorization header is missing")
}

// clientIP returns the peer address, ignoring spoofable forwarding headers.
func clientIP(r *http.Request) string {
	host, _, err := splitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func splitHostPort(addr string) (host, port string, err error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, "", errors.New("no port")
	}
	host = strings.Trim(addr[:i], "[]")
	return host, addr[i+1:], nil
}
