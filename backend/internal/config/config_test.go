package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeConfig writes a temporary config file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	// Defaults come from setDefaults rather than the file, so the smallest valid
	// document must produce a fully usable configuration.
	cfg, err := Load(writeConfig(t, "app:\n  name: test\n"), "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.App.Env != "dev" {
		t.Errorf("expected env dev, got %q", cfg.App.Env)
	}
	if cfg.HTTP.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.HTTP.Port)
	}
	if cfg.HTTP.ReadTimeout != 15*time.Second {
		t.Errorf("expected read timeout 15s, got %s", cfg.HTTP.ReadTimeout)
	}
	if cfg.Database.SSLMode != "disable" {
		t.Errorf("expected ssl mode disable, got %q", cfg.Database.SSLMode)
	}
	if cfg.Auth.Enabled {
		t.Error("expected authentication to be disabled by default")
	}
	if cfg.Log.Format != "json" {
		t.Errorf("expected json logs, got %q", cfg.Log.Format)
	}
}

func TestLoadReadsFileValues(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
app:
  name: overrides
  env: staging
http:
  port: 9090
auth:
  enabled: true
  issuer: https://issuer.test
  jwks_url: https://issuer.test/.well-known/jwks.json
`), "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.App.Name != "overrides" || cfg.App.Env != "staging" {
		t.Errorf("file values did not override defaults: %+v", cfg.App)
	}
	if cfg.HTTP.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.HTTP.Port)
	}
	if !cfg.Auth.Enabled {
		t.Error("expected auth enabled from the file")
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	// The whole point of the EDP__ scheme is that a deployment does not need a
	// bespoke config file per environment.
	t.Setenv("EDP_HTTP__PORT", "7000")
	t.Setenv("EDP_APP__NAME", "from-env")

	cfg, err := Load(writeConfig(t, "app:\n  name: from-file\n"), "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.App.Name != "from-env" {
		t.Errorf("expected the environment to win, got %q", cfg.App.Name)
	}
	if cfg.HTTP.Port != 7000 {
		t.Errorf("expected port 7000, got %d", cfg.HTTP.Port)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := map[string]string{
		"bad env":        "app:\n  env: production\n",
		"bad ssl mode":   "database:\n  ssl_mode: sometimes\n",
		"port too high":  "http:\n  port: 70000\n",
		"bad log level":  "log:\n  level: verbose\n",
		"bad log format": "log:\n  format: xml\n",
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body), ""); err == nil {
				t.Fatal("expected validation to fail")
			}
		})
	}
}

func TestValidateRejectsWriteTimeoutBelowReadTimeout(t *testing.T) {
	// Load runs Validate, so a response deadline shorter than the read deadline
	// is refused at startup rather than at first use.
	// A write timeout shorter than the read timeout would abort responses that
	// the client is still entitled to receive.
	if _, err := Load(writeConfig(t, `
http:
  read_timeout: 30s
  write_timeout: 10s
`), ""); err == nil {
		t.Fatal("expected a write timeout below the read timeout to be rejected")
	}
}

func TestValidateRejectsMinConnsAboveMaxConns(t *testing.T) {
	if _, err := Load(writeConfig(t, `
database:
  max_conns: 5
  min_conns: 10
`), ""); err == nil {
		t.Fatal("expected min_conns above max_conns to be rejected")
	}
}

func TestDSNBuildsExpectedConnectionString(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
database:
  host: db.test
  port: 5433
  user: edp
  password: s3cret
  name: edp_control_plane
  ssl_mode: require
`), "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	dsn := cfg.Database.DSN()
	for _, want := range []string{"db.test", "5433", "edp", "s3cret", "edp_control_plane", "require"} {
		if !contains(dsn, want) {
			t.Errorf("expected the DSN to contain %q, got %q", want, dsn)
		}
	}
}

func TestRedactedDSNHidesPassword(t *testing.T) {
	// This is what -print-config emits, so the password must never appear.
	cfg, err := Load(writeConfig(t, "database:\n  password: hunter2\n"), "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	redacted := cfg.Database.RedactedDSN()
	if contains(redacted, "hunter2") {
		t.Fatalf("the redacted DSN leaked the password: %q", redacted)
	}
	if !contains(redacted, "edp") {
		t.Errorf("expected the redacted DSN to keep the user, got %q", redacted)
	}
}

func TestAddrs(t *testing.T) {
	cfg, err := Load(writeConfig(t, "http:\n  host: 127.0.0.1\n  port: 8081\n"), "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if got := cfg.HTTP.Addr(); got != "127.0.0.1:8081" {
		t.Errorf("expected 127.0.0.1:8081, got %q", got)
	}
}

func TestIsProduction(t *testing.T) {
	if !(AppConfig{Env: "prod"}).IsProduction() {
		t.Error("expected prod to be production")
	}
	if (AppConfig{Env: "dev"}).IsProduction() {
		t.Error("expected dev not to be production")
	}
}

func TestIsAdminRole(t *testing.T) {
	cfg := AuthConfig{AdminRoles: []string{"platform-admin"}}

	if !cfg.IsAdminRole("platform-admin") {
		t.Error("expected a listed role to be an admin role")
	}
	if cfg.IsAdminRole("viewer") {
		t.Error("expected an unlisted role not to be an admin role")
	}
}

// contains is a small readability helper; strings.Contains would need the import
// in every assertion above.
func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) &&
		indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
