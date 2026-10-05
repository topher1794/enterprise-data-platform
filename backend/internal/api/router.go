// Package api wires the domains together and exposes them over HTTP.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/edp/edp-control-plane/internal/config"
	"github.com/edp/edp-control-plane/internal/middleware"
	"github.com/edp/edp-control-plane/internal/platform"
)

// Mountable is a domain handler that registers its own routes.
type Mountable interface {
	Mount(r chi.Router)
}

// Handlers bundles every domain handler, so the router's dependency list is one
// field rather than nine.
//
// Entries are typed as Mountable because the router only needs to call Mount;
// the concrete handler types live in the wiring code so this package does not
// depend on every domain.
type Handlers struct {
	Source     Mountable
	Dataset    Mountable
	Pipeline   Mountable
	Quality    Mountable
	Governance Mountable
	Lineage    Mountable
	Execution  Mountable
	Audit      Mountable
}

// all returns the mounted handlers in a stable order. Route registration order
// is only observable through conflict resolution, so this is for readability
// rather than behaviour.
func (h Handlers) all() []Mountable {
	return []Mountable{
		h.Source,
		h.Dataset,
		h.Pipeline,
		h.Quality,
		h.Governance,
		h.Lineage,
		h.Execution,
		h.Audit,
	}
}

// HealthChecker reports the state of one dependency.
type HealthChecker interface {
	// Name identifies the dependency in the health payload.
	Name() string
	// Check returns nil when the dependency is usable.
	Check(ctx context.Context) error
}

// RouterConfig is the subset of application configuration the router needs.
type RouterConfig struct {
	App     config.AppConfig
	HTTP    config.HTTPConfig
	Auth    config.AuthConfig
	Version string
	// BuildDate is reported by /version.
	BuildDate string
	// ReadyCheckers are consulted by /readyz. Empty means readiness reduces to
	// "the process is up".
	ReadyCheckers []HealthChecker
	// RequestTimeout bounds a single request. Zero disables the timeout
	// middleware, which is only appropriate in tests.
	RequestTimeout time.Duration
	// MaxBodyBytes rejects oversized bodies early. Zero disables the limit.
	MaxBodyBytes int64
	// Logger is handed to the logging middleware's package-level logger.
	Logger *slog.Logger
}

// NewRouter builds the fully wired HTTP handler.
//
// Middleware order is deliberate, outermost first:
//
//	RequestID     so every layer below logs the same id
//	Recovery      so a panic anywhere below becomes a 500 rather than a dropped
//	              connection
//	Timeout       so a slow handler cannot hold a connection open indefinitely
//	MaxBodyBytes  reject oversized bodies before any decoding work
//	CORS          answer preflight even for requests that later fail
//	Logging       record the outcome once the response is known
//	Authentication
//
// RequestID sits outside Recovery so the panic log carries the request id, and
// Timeout sits inside Recovery so a handler that overruns cannot take the
// process down.
func NewRouter(cfg RouterConfig, handlers Handlers) http.Handler {
	r := chi.NewRouter()

	// The catch-all handlers are registered before any routes so they cover the
	// whole tree, including the public and versioned subtrees.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		platform.Fail(w, req, platform.NewNotFound("route", req.Method+" "+req.URL.Path))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		platform.Fail(w, req, platform.NewMethodNotAllowed(req.Method, req.URL.Path))
	})

	r.Use(middleware.RequestID)
	r.Use(middleware.Recovery)
	if cfg.RequestTimeout > 0 {
		r.Use(middleware.Timeout(cfg.RequestTimeout))
	}
	if cfg.MaxBodyBytes > 0 {
		r.Use(middleware.MaxBodyBytes(cfg.MaxBodyBytes))
	}
	if len(cfg.HTTP.CORSAllowOrigin) > 0 {
		r.Use(middleware.CORS(cfg.HTTP.CORSAllowOrigin))
	}
	if cfg.Logger != nil {
		middleware.SetLogger(cfg.Logger)
	}
	r.Use(middleware.Logging)

	// Liveness and readiness are deliberately unauthenticated. A load balancer
	// probing the service has no credentials, and gating these would make a
	// missing token indistinguishable from a dead process.
	r.Get("/healthz", healthHandler(cfg, false))
	r.Get("/livez", healthHandler(cfg, false))
	r.Get("/readyz", healthHandler(cfg, true))

	if cfg.Version != "" {
		r.Get("/version", func(w http.ResponseWriter, req *http.Request) {
			platform.JSON(w, http.StatusOK, map[string]string{
				"version":    cfg.Version,
				"build_date": cfg.BuildDate,
			})
		})
	}

	// mountPath is the prefix the versioned API is served under.
	//
	// app.base_path defaults to /api/v1, so it already carries the version; the
	// router mounts there verbatim rather than appending its own suffix. A trailing
	// slash is trimmed so chi does not register "//api/v1" as a distinct route.
	mountPath := strings.TrimSuffix(cfg.App.BasePath, "/")
	if mountPath == "" {
		mountPath = "/"
	}

	r.Route(mountPath, func(r chi.Router) {
		// When authentication is enabled every API route needs a valid token.
		// When it is disabled the authenticator is not installed at all, so the
		// handlers run with a synthetic actor and the same authorization checks
		// still apply.
		if cfg.Auth.Enabled {
			r.Use(middleware.RequireAuthentication)
		}

		for _, h := range handlers.all() {
			if h != nil {
				h.Mount(r)
			}
		}
	})

	return r
}

// CheckResult is one dependency's health in the probe payload.
type CheckResult struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	LatencyMs int64  `json:"latency_ms"`
}

// HealthPayload is the body returned by /healthz and /readyz.
type HealthPayload struct {
	Status    string        `json:"status"`
	Version   string        `json:"version,omitempty"`
	Checks    []CheckResult `json:"checks"`
	Timestamp time.Time     `json:"timestamp"`
}

// healthHandler builds a probe handler.
//
// The two probes answer different questions and must not be merged. Liveness asks
// "is this process working", readiness asks "can it serve traffic". Reporting a
// database outage as unhealthy on the liveness probe would make an orchestrator
// restart a perfectly healthy process, and restart loops during a database
// outage are worse than the original problem.
func healthHandler(cfg RouterConfig, includeDependencies bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checks := make([]CheckResult, 0, len(cfg.ReadyCheckers))

		if includeDependencies {
			for _, checker := range cfg.ReadyCheckers {
				// One shared budget across all checkers: a probe that hangs must
				// not hold the connection open for the sum of every timeout.
				ctx, cancel := context.WithTimeout(r.Context(), probeTimeout(cfg))

				started := time.Now()
				err := checker.Check(ctx)
				elapsed := time.Since(started)
				cancel()

				result := CheckResult{
					Name:      checker.Name(),
					Status:    "ok",
					LatencyMs: elapsed.Milliseconds(),
				}
				if err != nil {
					result.Status = "unavailable"
					// A readiness failure without a reason is unactionable, so the
					// dependency's own message is included. It is the driver's
					// message, not a stack trace or a connection string.
					result.Error = err.Error()
				}
				checks = append(checks, result)
			}
		}

		healthy := true
		for _, check := range checks {
			if check.Status != "ok" {
				healthy = false
				break
			}
		}

		status := http.StatusOK
		state := "healthy"
		if !healthy {
			status = http.StatusServiceUnavailable
			state = "unavailable"
		}

		platform.JSON(w, status, HealthPayload{
			Status:    state,
			Version:   cfg.Version,
			Checks:    checks,
			Timestamp: time.Now().UTC(),
		})
	}
}

// defaultProbeTimeout bounds dependency checks when the caller supplied no
// request timeout to derive one from.
const defaultProbeTimeout = 3 * time.Second

// probeTimeout picks the probe budget: half the request timeout, so a probe
// still fits inside whatever deadline the caller imposed.
func probeTimeout(cfg RouterConfig) time.Duration {
	if cfg.RequestTimeout <= 0 {
		return defaultProbeTimeout
	}
	if half := cfg.RequestTimeout / 2; half < defaultProbeTimeout {
		return half
	}
	return defaultProbeTimeout
}
