package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// EnvProvider resolves `env://NAME` references from process environment
// variables. It exists for local development and tests; production deployments
// should use a Vault or cloud KMS-backed provider.
//
// The environment is snapshotted once at construction so a later mutation
// cannot change resolution mid-process, and so tests remain deterministic.
type EnvProvider struct {
	mu       sync.RWMutex
	lookup   func(string) (string, bool)
	loadedAt time.Time
}

// NewEnvProvider builds a provider that reads the live process environment.
func NewEnvProvider() *EnvProvider {
	return &EnvProvider{lookup: os.LookupEnv, loadedAt: time.Now()}
}

// NewStaticProvider builds a provider over an explicit map, for tests.
func NewStaticProvider(values map[string]string) *EnvProvider {
	snapshot := make(map[string]string, len(values))
	for k, v := range values {
		snapshot[strings.ToUpper(k)] = v
	}
	return &EnvProvider{
		lookup: func(key string) (string, bool) {
			v, ok := snapshot[strings.ToUpper(key)]
			return v, ok
		},
		loadedAt: time.Now(),
	}
}

// Resolve implements Provider for `env://NAME` references.
func (p *EnvProvider) Resolve(_ context.Context, ref string) (Secret, error) {
	if err := ValidateRef(ref); err != nil {
		return Secret{}, err
	}

	scheme, name, _ := strings.Cut(ref, "://")
	if !strings.EqualFold(scheme, "env") {
		return Secret{}, fmt.Errorf("env provider cannot resolve %q: unsupported scheme %q", ref, scheme)
	}

	p.mu.RLock()
	lookup := p.lookup
	version := p.loadedAt.UTC().Format(time.RFC3339)
	p.mu.RUnlock()

	value, ok := lookup(name)
	if !ok {
		return Secret{}, fmt.Errorf("%w: environment variable %q is not set", ErrNotFound, name)
	}

	return Secret{Value: []byte(value), Version: version}, nil
}
