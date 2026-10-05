package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/edp/edp-control-plane/internal/config"
	"github.com/edp/edp-control-plane/internal/platform"
)

// stubHandler records that it was mounted and answers 200 on every route.
type stubHandler struct {
	mounted bool
}

func (s *stubHandler) Mount(r chi.Router) {
	s.mounted = true
	r.Get("/things", func(w http.ResponseWriter, _ *http.Request) {
		platform.JSON(w, http.StatusOK, map[string]string{"ok": "yes"})
	})
}

// stubChecker reports a fixed outcome for the readiness probe.
type stubChecker struct {
	name string
	err  error
	// delay lets a test prove the probe budget bounds the check.
	delay time.Duration
}

func (c stubChecker) Name() string { return c.name }

func (c stubChecker) Check(ctx context.Context) error {
	if c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.err
}

// testConfig is a minimal valid configuration for the router.
//
// The logger writes to io.Discard: the logging middleware logs every request,
// and the deliberate panic tests would otherwise print full stack traces that
// bury the actual test output.
func testConfig() RouterConfig {
	return RouterConfig{
		App:     config.AppConfig{Name: "test", Env: "dev", BasePath: "/api/v1"},
		HTTP:    config.HTTPConfig{CORSAllowOrigin: []string{"https://example.test"}},
		Auth:    config.AuthConfig{Enabled: false},
		Version: "test",
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// newTestRouter builds a router over the supplied handlers and config.
func newTestRouter(cfg RouterConfig, handlers Handlers) http.Handler {
	return NewRouter(cfg, handlers)
}

func decodeHealth(t *testing.T, rec *httptest.ResponseRecorder) HealthPayload {
	t.Helper()

	var payload HealthPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode health payload %q: %v", rec.Body.String(), err)
	}
	return payload
}

func TestHealthzIsHealthyWithoutDependencyChecks(t *testing.T) {
	t.Parallel()

	// Liveness deliberately does not consult dependencies, so a database outage
	// cannot cause an orchestrator to restart an otherwise healthy process.
	handler := newTestRouter(testConfig(), Handlers{})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := decodeHealth(t, rec).Status; got != "healthy" {
		t.Fatalf("expected status healthy, got %q", got)
	}
}

func TestReadyzFailsWhenDependencyIsDown(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.ReadyCheckers = []HealthChecker{stubChecker{name: "postgres", err: errors.New("connection refused")}}

	handler := newTestRouter(cfg, Handlers{})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	payload := decodeHealth(t, rec)
	if payload.Status != "unavailable" {
		t.Fatalf("expected status unavailable, got %q", payload.Status)
	}
	if len(payload.Checks) != 1 {
		t.Fatalf("expected one check, got %d", len(payload.Checks))
	}

	check := payload.Checks[0]
	if check.Name != "postgres" || check.Status != "ok" && check.Status != "unavailable" {
		t.Fatalf("unexpected check result: %+v", check)
	}
	if check.Status != "unavailable" {
		t.Errorf("expected the dependency to be reported unavailable, got %+v", check)
	}
	// A readiness failure without a reason is unactionable.
	if check.Error == "" {
		t.Error("expected the failure reason to be reported")
	}
}

func TestReadyzSucceedsWhenAllDependenciesPass(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.ReadyCheckers = []HealthChecker{
		stubChecker{name: "postgres"},
		stubChecker{name: "airflow"},
	}

	handler := newTestRouter(cfg, Handlers{})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if got := len(decodeHealth(t, rec).Checks); got != 2 {
		t.Fatalf("expected both checks to be reported, got %d", got)
	}
}

func TestLivenessStaysUpWhileDependenciesAreDown(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.ReadyCheckers = []HealthChecker{stubChecker{name: "postgres", err: errors.New("down")}}

	handler := newTestRouter(cfg, Handlers{})

	for _, path := range []string{"/healthz", "/livez"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if rec.Code != http.StatusOK {
			t.Errorf("%s: expected 200 while a dependency is down, got %d", path, rec.Code)
		}
	}
}

func TestReadyzBoundsSlowCheckerWithRequestTimeout(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.RequestTimeout = 60 * time.Millisecond
	cfg.ReadyCheckers = []HealthChecker{
		stubChecker{name: "slow", delay: 2 * time.Second},
	}

	handler := newTestRouter(cfg, Handlers{})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected the probe to fail fast with 503, got %d", rec.Code)
	}
}

func TestUnknownRouteReturnsNotFound(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(testConfig(), Handlers{})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestMountedHandlersAreRegisteredUnderBasePath(t *testing.T) {
	t.Parallel()

	stub := &stubHandler{}
	handler := newTestRouter(testConfig(), Handlers{Source: stub})

	if !stub.mounted {
		t.Fatal("expected the source handler to be mounted")
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/things", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected the mounted route to respond, got %d", rec.Code)
	}
}

func TestNilHandlersAreSkipped(t *testing.T) {
	t.Parallel()

	// Every domain may be absent; a nil entry must not panic the router.
	rec := httptest.NewRecorder()
	newTestRouter(testConfig(), Handlers{}).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with no handlers wired, got %d", rec.Code)
	}
}

func TestAPIRoutesRequireAuthenticationWhenEnabled(t *testing.T) {
	t.Parallel()

	stub := &stubHandler{}
	cfg := testConfig()
	cfg.Auth.Enabled = true

	handler := newTestRouter(cfg, Handlers{Source: stub})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/things", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a token, got %d", rec.Code)
	}
}

func TestProbesRemainPublicWhenAuthIsEnabled(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.Auth.Enabled = true

	handler := newTestRouter(cfg, Handlers{})

	// A load balancer probing the service holds no credentials. Gating the probes
	// would make a missing token look identical to a dead process.
	for _, path := range []string{"/healthz", "/livez", "/readyz"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if rec.Code != http.StatusOK {
			t.Errorf("%s: expected 200 with auth enabled, got %d", path, rec.Code)
		}
	}
}

func TestCORSHeadersAreSet(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(testConfig(), Handlers{})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://example.test")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got == "" {
		t.Error("expected Access-Control-Allow-Origin to be set for a configured origin")
	}
}

func TestUnknownMethodOnMountedRouteIsRejected(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(testConfig(), Handlers{Source: &stubHandler{}})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/things", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestVersionReportsBuildMetadata(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.BuildDate = "2026-01-01T00:00:00Z"

	handler := newTestRouter(cfg, Handlers{})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode version payload: %v", err)
	}
	if payload["version"] != "test" || payload["build_date"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("unexpected version payload: %v", payload)
	}
}

func TestCustomBasePathIsHonoured(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.App.BasePath = "/internal/api"

	handler := newTestRouter(cfg, Handlers{Source: &stubHandler{}})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/api/things", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 under the custom base path, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/things", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected the default base path to be absent, got %d", rec.Code)
	}
}

// panicHandler panics on every route, to exercise the recovery middleware.
type panicHandler struct{}

func (panicHandler) Mount(r chi.Router) {
	r.Get("/boom", func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
}

func TestRequestPanicBecomesServerError(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(testConfig(), Handlers{Source: panicHandler{}})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/boom", nil))

	// Recovery must turn the panic into a 500 rather than dropping the
	// connection, which an orchestrator would see as a network fault.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestRouterKeepsServingAfterAPanic(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(testConfig(), Handlers{Source: panicHandler{}})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}

	// The recovered process must still answer other requests.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the router to keep serving, got %d", rec.Code)
	}
}
