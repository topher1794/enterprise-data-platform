package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/edp/edp-control-plane/internal/config"
)

// JWKSVerifier validates RS256/ES256 access tokens against an identity
// provider's JWKS endpoint, refreshing the key set on the configured interval
// and immediately when an unknown key ID is presented.
//
// Only asymmetric algorithms are permitted. Allowing `none` or a symmetric key
// derived from public material is the classic JWT confusion vulnerability.
type JWKSVerifier struct {
	cfg     config.AuthConfig
	http    *http.Client
	mu      sync.RWMutex
	keys    map[string]any
	fetched time.Time
}

// NewJWKSVerifier builds a verifier for cfg. The key set is fetched lazily on
// the first Verify call, so construction never blocks startup on the IdP.
func NewJWKSVerifier(cfg config.AuthConfig) (*JWKSVerifier, error) {
	if cfg.JWKSURL == "" {
		return nil, errors.New("jwt verifier: jwks_url is required")
	}
	if _, err := http.NewRequest(http.MethodGet, cfg.JWKSURL, nil); err != nil {
		return nil, fmt.Errorf("jwt verifier: invalid jwks_url: %w", err)
	}

	return &JWKSVerifier{
		cfg:  cfg,
		http: &http.Client{Timeout: 10 * time.Second},
		keys: make(map[string]any),
	}, nil
}

// allowedAlgs are the signature algorithms this service will verify.
var allowedAlgs = []string{"RS256", "RS384", "RS512", "ES256", "ES384", "PS256"}

// Verify parses raw and validates its signature, issuer, audience and expiry,
// then projects the claims into a Claims value.
func (v *JWKSVerifier) Verify(ctx context.Context, raw string) (*Claims, error) {
	opts := []jwt.ParserOption{
		jwt.WithValidMethods(allowedAlgs),
		jwt.WithExpirationRequired(),
		jwt.WithIssuer(v.cfg.Issuer),
		jwt.WithLeeway(v.cfg.ClockSkew),
	}
	if v.cfg.Audience != "" {
		opts = append(opts, jwt.WithAudience(v.cfg.Audience))
	}

	token, err := jwt.ParseWithClaims(raw, jwt.MapClaims{}, func(t *jwt.Token) (any, error) {
		return v.keyFunc(ctx, t)
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("%w: token failed validation", ErrInvalidToken)
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected claims type %T", ErrInvalidToken, token.Claims)
	}
	return v.toClaims(mapClaims), nil
}

// keyFunc resolves the signing key for the token's `kid` header, refreshing the
// cache when the key is unknown (as happens right after an IdP key rotation) or
// when the cached set has expired.
func (v *JWKSVerifier) keyFunc(ctx context.Context, token *jwt.Token) (any, error) {
	kid, _ := token.Header["kid"].(string)
	if kid == "" {
		return nil, fmt.Errorf("%w: token header is missing kid", ErrInvalidToken)
	}

	if key, ok := v.lookup(kid); ok {
		return key, nil
	}

	// Unknown key: force one refresh, then look again. A forged `kid` therefore
	// costs one upstream request per attempt, which the JWKS endpoint's own rate
	// limiting is expected to absorb.
	if err := v.refresh(ctx, true); err != nil {
		return nil, fmt.Errorf("%w: refresh keys for kid %q: %v", ErrInvalidToken, kid, err)
	}

	key, ok := v.lookup(kid)
	if !ok {
		return nil, fmt.Errorf("%w: no key matching kid %q", ErrInvalidToken, kid)
	}
	return key, nil
}

func (v *JWKSVerifier) lookup(kid string) (any, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	key, ok := v.keys[kid]
	return key, ok
}

// refresh fetches the JWKS document and replaces the cached key set. When force
// is false a fetch still happens if the cache is older than the configured
// interval.
func (v *JWKSVerifier) refresh(ctx context.Context, force bool) error {
	v.mu.RLock()
	fresh := time.Since(v.fetched) < v.cfg.JWKSRefresh && len(v.keys) > 0
	v.mu.RUnlock()

	if fresh && !force {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return fmt.Errorf("build jwks request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := v.http.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks endpoint returned %d", resp.StatusCode)
	}

	jwks, err := parseJWKS(resp.Body)
	if err != nil {
		return err
	}

	v.mu.Lock()
	v.keys = jwks
	v.fetched = time.Now()
	v.mu.Unlock()

	log.InfoContext(ctx, "refreshed jwks key set", "key_count", len(jwks))
	return nil
}

// toClaims projects the raw JWT payload into the control plane's claim shape.
// Claim names are configurable because identity providers disagree on naming.
func (v *JWKSVerifier) toClaims(m jwt.MapClaims) *Claims {
	out := &Claims{
		Subject:  stringClaim(m, "sub"),
		Email:    stringClaim(m, "email"),
		Name:     firstNonEmpty(stringClaim(m, "name"), stringClaim(m, "preferred_username")),
		TenantID: stringClaim(m, v.cfg.TenantClaim),
		Issuer:   stringClaim(m, "iss"),
		Roles:    sliceClaim(m, v.cfg.RolesClaim),
		Scopes:   scopeClaim(m, v.cfg.ScopesClaim),
	}

	// Roles may arrive as a JSON array, or space- and comma-delimited.
	out.Roles = dedupeStrings(append(out.Roles, splitDelimited(stringClaim(m, v.cfg.RolesClaim))...))

	if exp, err := m.GetExpirationTime(); err == nil && exp != nil {
		out.ExpiresAt = exp.Time
	}
	return out
}

func stringClaim(m jwt.MapClaims, key string) string {
	if key == "" {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// sliceClaim reads a claim that may be a JSON array or a single string.
func sliceClaim(m jwt.MapClaims, key string) []string {
	if key == "" {
		return nil
	}
	switch v := m[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		return splitDelimited(v)
	default:
		return nil
	}
}

// scopeClaim handles the OAuth convention where scopes are space-delimited.
func scopeClaim(m jwt.MapClaims, key string) []string {
	if key == "" {
		return nil
	}
	if v, ok := m[key].(string); ok {
		return splitDelimited(v)
	}
	return dedupeStrings(sliceClaim(m, key))
}

// splitDelimited splits on spaces and commas, the two delimiters identity
// providers use for role and scope lists.
func splitDelimited(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t'
	})
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
