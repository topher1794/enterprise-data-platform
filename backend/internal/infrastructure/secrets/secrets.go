// Package secrets resolves credential references held in metadata columns into
// usable secret values. Secrets are never persisted in the control-plane
// database; only opaque references are.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned when a reference cannot be resolved.
var ErrNotFound = errors.New("secret not found")

// Secret is a resolved credential plus the metadata needed to rotate it.
type Secret struct {
	// Value is the secret payload. Treat as sensitive: never log it.
	Value []byte
	// Version identifies the current rotation generation.
	Version string
	// ExpiresAt is zero for secrets with no expiry.
	ExpiresAt time.Time
}

// IsZero reports whether the secret holds no data.
func (s Secret) IsZero() bool { return len(s.Value) == 0 }

// Expired reports whether the secret has passed its expiry.
func (s Secret) Expired(now time.Time) bool {
	return !s.ExpiresAt.IsZero() && now.After(s.ExpiresAt)
}

// Provider resolves a secret reference to its value.
type Provider interface {
	// Resolve returns the secret for ref. Implementations must be safe for
	// concurrent use.
	Resolve(ctx context.Context, ref string) (Secret, error)
}

// Store is a caching decorator around a Provider. It short-circuits repeated
// resolutions of the same reference, which matters because health checks and
// task previews resolve the same connection secrets many times per minute.
type Store struct {
	provider Provider

	mu      sync.RWMutex
	entries map[string]cacheEntry

	ttl     time.Duration
	maxSize int
	now     func() time.Time
}

type cacheEntry struct {
	secret    Secret
	expiresAt time.Time
}

// NewStore wraps provider with a TTL cache. A ttl of zero disables caching.
func NewStore(provider Provider, ttl time.Duration, maxEntries int) *Store {
	if maxEntries <= 0 {
		maxEntries = 1024
	}
	return &Store{
		provider: provider,
		entries:  make(map[string]cacheEntry, maxEntries),
		ttl:      ttl,
		maxSize:  maxEntries,
		now:      time.Now,
	}
}

// Resolve returns the secret for ref, serving a cached value when one is fresh.
// A resolved secret is copied before being returned so a caller mutating the
// slice cannot corrupt the cache.
func (s *Store) Resolve(ctx context.Context, ref string) (Secret, error) {
	if s == nil || s.provider == nil {
		return Secret{}, fmt.Errorf("resolve secret %q: %w", ref, ErrNotFound)
	}
	if err := ValidateRef(ref); err != nil {
		return Secret{}, err
	}

	if s.ttl > 0 {
		if cached, ok := s.load(ref); ok {
			return cloneSecret(cached), nil
		}
	}

	secret, err := s.provider.Resolve(ctx, ref)
	if err != nil {
		return Secret{}, err
	}

	if s.ttl > 0 {
		// An explicit expiry always wins; otherwise fall back to the cache TTL.
		expiresAt := s.now().Add(s.ttl)
		if !secret.ExpiresAt.IsZero() && secret.ExpiresAt.Before(expiresAt) {
			expiresAt = secret.ExpiresAt
		}
		s.store(ref, cacheEntry{secret: cloneSecret(secret), expiresAt: expiresAt})
	}

	return cloneSecret(secret), nil
}

func (s *Store) load(ref string) (Secret, bool) {
	s.mu.RLock()
	entry, ok := s.entries[ref]
	s.mu.RUnlock()

	if !ok || s.now().After(entry.expiresAt) {
		return Secret{}, false
	}
	return entry.secret, true
}

func (s *Store) store(ref string, entry cacheEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Simple bounded eviction: drop the whole cache rather than tracking ages.
	// A credential fan-out this wide is rare, and a cold cache is cheap.
	if len(s.entries) >= s.maxSize {
		s.entries = make(map[string]cacheEntry, s.maxSize)
	}
	s.entries[ref] = entry
}

// Invalidate drops the cached value for ref, forcing the next Resolve to hit
// the provider. Call this after rotating a credential.
func (s *Store) Invalidate(ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, ref)
}

// RefPattern constrains references to a `scheme://path` shape. Scheme
// identifies the backend (vault, awssm, gcp, azurekv, env).
var refSchemes = map[string]bool{
	"vault":   true,
	"awssm":   true,
	"gcp":     true,
	"azurekv": true,
	"env":     true,
}

// ValidateRef checks that ref is a well-formed, supported secret reference.
func ValidateRef(ref string) error {
	if ref == "" {
		return errors.New("secret reference must not be empty")
	}
	if len(ref) > 512 {
		return errors.New("secret reference must not exceed 512 characters")
	}

	scheme, path, ok := strings.Cut(ref, "://")
	if !ok {
		return fmt.Errorf("secret reference %q must have the form scheme://path", ref)
	}
	if !refSchemes[strings.ToLower(scheme)] {
		return fmt.Errorf("secret reference %q uses unsupported scheme %q", ref, scheme)
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("secret reference %q has an empty path", ref)
	}
	return nil
}

func cloneSecret(s Secret) Secret {
	out := Secret{Version: s.Version, ExpiresAt: s.ExpiresAt}
	if s.Value != nil {
		out.Value = append([]byte(nil), s.Value...)
	}
	return out
}
