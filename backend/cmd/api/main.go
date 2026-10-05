// Command api runs the EDP control plane HTTP server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/api"
	"github.com/edp/edp-control-plane/internal/audit"
	"github.com/edp/edp-control-plane/internal/config"
	"github.com/edp/edp-control-plane/internal/dataset"
	"github.com/edp/edp-control-plane/internal/execution"
	"github.com/edp/edp-control-plane/internal/governance"
	"github.com/edp/edp-control-plane/internal/infrastructure/airflow"
	"github.com/edp/edp-control-plane/internal/infrastructure/postgres"
	"github.com/edp/edp-control-plane/internal/infrastructure/secrets"
	"github.com/edp/edp-control-plane/internal/lineage"
	"github.com/edp/edp-control-plane/internal/pipeline"
	"github.com/edp/edp-control-plane/internal/quality"
	"github.com/edp/edp-control-plane/internal/source"
	"github.com/edp/edp-control-plane/migrations"
)

// Build metadata, injected at link time:
//
//	go build -ldflags "-X main.version=1.2.3 -X main.buildDate=$(date -u +%FT%TZ)"
var (
	version   = "dev"
	buildDate = "unknown"
)

const (
	// defaultMaxBodyBytes caps request bodies. Catalog and schema payloads are
	// the largest legitimate inputs; 16 MiB leaves generous headroom for a wide
	// dataset schema while still stopping an unbounded upload.
	defaultMaxBodyBytes = 16 << 20

	// reconcilerInterval is how often stale runs are reconciled with Airflow.
	reconcilerInterval = 5 * time.Minute

	// secretCacheTTL bounds how long a resolved secret may be reused.
	secretCacheTTL = 5 * time.Minute
)

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet when configuration loading fails, so this
		// last-resort message goes straight to stderr.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath = flag.String("config", "", "path to the configuration file (default: configs/config.yaml)")
		migrate    = flag.Bool("migrate", false, "apply pending database migrations and exit")
		rollback   = flag.Bool("rollback", false, "roll back one migration and exit")
		showConfig = flag.Bool("print-config", false, "print resolved database settings with secrets redacted and exit")
		// The runtime image ships no shell or curl, so the health check has to be
		// the binary itself.
		healthcheck = flag.Bool("healthcheck", false, "probe the local liveness endpoint and exit")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath, os.Getenv("EDP_ENV"))
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	if *healthcheck {
		return probeLiveness(cfg)
	}

	logger, err := newLogger(cfg.Log)
	if err != nil {
		return fmt.Errorf("build logger: %w", err)
	}
	slog.SetDefault(logger)

	if *showConfig {
		// RedactedDSN masks the password, which is the only field a config dump
		// could leak. Printing the rest is genuinely useful when debugging.
		fmt.Println(cfg.Database.RedactedDSN())
		return nil
	}

	logger.Info("starting edp control plane",
		"name", cfg.App.Name,
		"env", cfg.App.Env,
		"version", cfg.App.Version,
		"build_version", version,
	)

	// Signals cancel rootCtx, which unwinds the startup path as well: an
	// interrupt during a slow database connect should not leave a
	// half-initialised process running.
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := postgres.NewPool(rootCtx, cfg.Database)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer db.Close()
	logger.Info("connected to database")

	// Run migrations as a separate process by default so that schema changes are
	// an explicit deployment step rather than a side effect of starting the API.
	if *migrate || *rollback {
		return runMigrations(rootCtx, cfg, logger, *rollback)
	}
	if cfg.Database.MigrateOnBoot {
		if err := applyMigrations(rootCtx, cfg, logger); err != nil {
			return err
		}
	}

	deps, err := buildDependencies(cfg, db, logger)
	if err != nil {
		return err
	}

	handler := api.NewRouter(routerConfig(cfg, deps), api.Handlers{
		Source:     source.NewHandler(deps.source),
		Dataset:    dataset.NewHandler(deps.dataset),
		Pipeline:   pipeline.NewHandler(deps.pipeline),
		Quality:    quality.NewHandler(deps.quality),
		Governance: governance.NewHandler(deps.governance),
		Lineage:    lineage.NewHandler(deps.lineage),
		Execution:  execution.NewHandler(deps.execution),
		Audit:      audit.NewHandler(deps.audit),
	})

	server := &http.Server{
		Addr:              cfg.HTTP.Addr(),
		Handler:           handler,
		ReadHeaderTimeout: cfg.HTTP.ReadTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", server.Addr, "base_path", cfg.App.BasePath)

		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	// The reconciler repairs runs whose Airflow callback was lost. It is
	// supporting infrastructure, so its failures are logged rather than
	// propagated.
	go startReconciler(rootCtx, deps.execution, logger)

	select {
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("http server failed: %w", err)
		}
		return nil
	case <-rootCtx.Done():
		logger.Info("shutdown signal received", "timeout", cfg.HTTP.ShutdownTimeout)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		// In-flight requests did not finish in time. Close anyway rather than
		// refusing to exit, and report it so the cause stays visible.
		logger.Error("graceful shutdown failed, closing connections", "error", err)
		_ = server.Close()
		return fmt.Errorf("shutdown: %w", err)
	}

	<-serverErr
	logger.Info("shutdown complete")
	return nil
}

// dependencies holds every constructed service plus the resources the health
// probes need, so main stays a wiring list rather than a chain of interacting
// locals.
type dependencies struct {
	source     *source.Service
	dataset    *dataset.Service
	pipeline   *pipeline.Service
	quality    *quality.Service
	governance *governance.Service
	lineage    *lineage.Service
	execution  *execution.Service
	audit      *audit.Service

	orchestrator execution.Orchestrator
	// db is retained so the readiness probe can check the database. The caller
	// keeps ownership: main closes the pool, not this struct.
	db postgres.DB
}

// buildDependencies constructs the object graph.
//
// The order matters in two places: audit is built first because every domain
// records through it, and pipeline is built before execution because execution
// resolves pipeline deployment details through a lookup.
func buildDependencies(cfg *config.Config, db postgres.DB, logger *slog.Logger) (*dependencies, error) {
	// Audit first. Building it later would mean either an import cycle or passing
	// a nil auditor to six services.
	auditSvc, err := audit.NewService(
		audit.NewRepository(db),
		audit.WithLogger(logger),
	)
	if err != nil {
		return nil, fmt.Errorf("build audit service: %w", err)
	}

	secretStore := secrets.NewStore(secrets.NewEnvProvider(), secretCacheTTL, 1024)

	sourceSvc, err := source.NewService(
		source.NewRepository(db),
		secretStore,
		source.WithLogger(logger),
		source.WithAuditor(sourceAuditor{svc: auditSvc}),
	)
	if err != nil {
		return nil, fmt.Errorf("build source service: %w", err)
	}

	datasetSvc, err := dataset.NewService(
		dataset.NewRepository(db),
		dataset.WithLogger(logger),
		dataset.WithAuditor(datasetAuditor{svc: auditSvc}),
	)
	if err != nil {
		return nil, fmt.Errorf("build dataset service: %w", err)
	}

	pipelineSvc, err := pipeline.NewService(
		pipeline.NewRepository(db),
		pipeline.WithLogger(logger),
		pipeline.WithAuditor(pipelineAuditor{svc: auditSvc}),
	)
	if err != nil {
		return nil, fmt.Errorf("build pipeline service: %w", err)
	}

	qualitySvc, err := quality.NewService(
		quality.NewRepository(db),
		quality.WithLogger(logger),
		quality.WithAuditor(qualityAuditor{svc: auditSvc}),
	)
	if err != nil {
		return nil, fmt.Errorf("build quality service: %w", err)
	}

	governanceSvc, err := governance.NewService(
		governance.NewRepository(db),
		governance.WithLogger(logger),
		governance.WithAuditor(governanceAuditor{svc: auditSvc}),
	)
	if err != nil {
		return nil, fmt.Errorf("build governance service: %w", err)
	}

	lineageSvc, err := lineage.NewService(
		lineage.NewRepository(db),
		lineage.WithLogger(logger),
		lineage.WithAuditor(lineageAuditor{svc: auditSvc}),
	)
	if err != nil {
		return nil, fmt.Errorf("build lineage service: %w", err)
	}

	// Execution needs an orchestrator only when one is configured. A deployment
	// without Airflow still serves run history, so a nil orchestrator is allowed
	// and only surfaces when a caller tries to trigger a run.
	var orchestrator execution.Orchestrator
	if cfg.Airflow.Enabled {
		client, err := airflow.NewClient(cfg.Airflow)
		if err != nil {
			return nil, fmt.Errorf("build airflow client: %w", err)
		}
		orchestrator = client
	}

	executionSvc, err := execution.NewService(
		execution.NewRepository(db),
		pipelineLookup{svc: pipelineSvc},
		execution.WithLogger(logger),
		execution.WithOrchestrator(orchestrator),
		execution.WithAuditor(executionAuditor{svc: auditSvc}),
	)
	if err != nil {
		return nil, fmt.Errorf("build execution service: %w", err)
	}

	return &dependencies{
		source:       sourceSvc,
		dataset:      datasetSvc,
		pipeline:     pipelineSvc,
		quality:      qualitySvc,
		governance:   governanceSvc,
		lineage:      lineageSvc,
		execution:    executionSvc,
		audit:        auditSvc,
		orchestrator: orchestrator,
		db:           db,
	}, nil
}

// routerConfig translates application configuration into router configuration.
func routerConfig(cfg *config.Config, deps *dependencies) api.RouterConfig {
	reportVersion := cfg.App.Version
	if reportVersion == "" {
		reportVersion = version
	}

	checkers := []api.HealthChecker{databaseChecker{db: deps.db}}
	if deps.orchestrator != nil {
		// Only probe the orchestrator when one is configured. Registering a
		// checker for a dependency that does not exist would make readiness
		// permanently red for deployments that run without Airflow.
		checkers = append(checkers, orchestratorChecker{orchestrator: deps.orchestrator})
	}

	return api.RouterConfig{
		App:            cfg.App,
		HTTP:           cfg.HTTP,
		Auth:           cfg.Auth,
		Version:        reportVersion,
		BuildDate:      buildDate,
		ReadyCheckers:  checkers,
		RequestTimeout: cfg.HTTP.WriteTimeout,
		MaxBodyBytes:   defaultMaxBodyBytes,
	}
}

// startReconciler periodically corrects runs whose status was never reported.
func startReconciler(ctx context.Context, svc *execution.Service, logger *slog.Logger) {
	ticker := time.NewTicker(reconcilerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			corrected, err := svc.Reconcile(ctx)
			if err != nil {
				logger.Warn("run reconciliation failed", "error", err)
				continue
			}
			if corrected > 0 {
				logger.Info("reconciled stale runs", "count", corrected)
			}
		}
	}
}

// applyMigrations runs pending migrations, logging the version before and after.
func applyMigrations(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	runner, err := newMigrationRunner(cfg)
	if err != nil {
		return err
	}
	defer closeRunner(runner, logger)

	current, dirty, err := runner.Version()
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if dirty {
		// A dirty version means a previous migration failed midway. Continuing
		// would build a service on a half-applied schema, so this has to be
		// resolved deliberately rather than automatically.
		return fmt.Errorf(
			"database schema is dirty at version %d; inspect and repair it before restarting (migrations: %s)",
			current, cfg.Database.MigrationsPath)
	}
	logger.Info("schema version before migrating", "version", current)

	if err := runner.Migrate(); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	final, _, err := runner.Version()
	if err != nil {
		return fmt.Errorf("read schema version after migrating: %w", err)
	}
	logger.Info("schema version after migrating", "version", final)

	return nil
}

// runMigrations serves the -migrate and -rollback flags, then exits.
func runMigrations(_ context.Context, cfg *config.Config, logger *slog.Logger, rollback bool) error {
	runner, err := newMigrationRunner(cfg)
	if err != nil {
		return err
	}
	defer closeRunner(runner, logger)

	if rollback {
		if err := runner.Rollback(); err != nil {
			return fmt.Errorf("roll back migration: %w", err)
		}
		logger.Info("rolled back one migration")
		return nil
	}

	return applyMigrations(context.Background(), cfg, logger)
}

// newMigrationRunner builds a runner over the embedded migrations.
func newMigrationRunner(cfg *config.Config) (*postgres.Runner, error) {
	runner, err := postgres.NewRunner(migrationFS(), cfg.Database.DSN())
	if err != nil {
		return nil, fmt.Errorf("build migration runner: %w", err)
	}
	return runner, nil
}

// migrationFS returns the embedded migration files as a filesystem.
//
// The SQL is compiled into the binary, so migrations work regardless of the
// working directory the process was started from.
func migrationFS() fs.FS {
	return migrations.FS
}

// closeRunner releases the migration connection, logging rather than returning
// the error because there is nothing useful a caller could do about it.
func closeRunner(runner *postgres.Runner, logger *slog.Logger) {
	if err := runner.Close(); err != nil {
		logger.Warn("failed to close migration runner", "error", err)
	}
}

// newLogger builds the process logger from configuration.
func newLogger(cfg config.LogConfig) (*slog.Logger, error) {
	var level slog.Level

	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("unsupported log level %q", cfg.Level)
	}

	opts := &slog.HandlerOptions{Level: level, AddSource: cfg.AddSource}

	var handler slog.Handler
	switch cfg.Format {
	case "text":
		// Text output is for local development. Production emits JSON so logs
		// can be parsed without a grok pattern per message.
		handler = slog.NewTextHandler(os.Stdout, opts)
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		return nil, fmt.Errorf("unsupported log format %q", cfg.Format)
	}

	return slog.New(handler), nil
}

// healthProbeTimeout bounds the self-probe. It is short because an orchestrator
// treats a slow health check as a failed one.
const healthProbeTimeout = 3 * time.Second

// probeLiveness checks this process's own liveness endpoint and reports the
// result through the exit code, for use as a container HEALTHCHECK.
//
// Liveness rather than readiness is deliberate. A container that reports unhealthy
// because its database is unreachable gets restarted, which does not fix the
// database and turns a dependency outage into a crash loop across the fleet.
func probeLiveness(cfg *config.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
	defer cancel()

	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", cfg.HTTP.Port)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: healthProbeTimeout}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("liveness probe failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("liveness probe returned %d", resp.StatusCode)
	}
	return nil
}

// databaseChecker reports database health for the readiness probe.
type databaseChecker struct {
	db postgres.DB
}

func (c databaseChecker) Name() string { return "postgres" }

func (c databaseChecker) Check(ctx context.Context) error {
	// Ping alone would succeed against a database that is reachable but out of
	// connection slots, which is exactly the condition readiness is meant to
	// catch. Issuing a trivial query exercises a real round trip.
	if err := c.db.Ping(ctx); err != nil {
		return err
	}

	var one int
	if err := c.db.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		return err
	}
	if one != 1 {
		return errors.New("unexpected response from database")
	}
	return nil
}

// orchestratorChecker reports Airflow health for the readiness probe.
type orchestratorChecker struct {
	orchestrator execution.Orchestrator
}

func (c orchestratorChecker) Name() string { return "airflow" }

func (c orchestratorChecker) Check(ctx context.Context) error {
	return c.orchestrator.Ping(ctx)
}

// The audit adapters below each implement one domain's locally declared
// Auditor interface.
//
// The domains deliberately do not import the audit package. Audit must be able
// to record events about any domain without knowing those domains exist, so the
// dependency runs the other way and each domain declares a minimal
// Auditor interface over its own AuditEntry type. Go does not treat named
// struct types as interchangeable, so one adapter per domain is required rather
// than a single generic one. Every adapter shares the same field mapping; they
// are separate only because the parameter types differ.
type sourceAuditor struct{ svc *audit.Service }

func (a sourceAuditor) Record(ctx context.Context, entry source.AuditEntry) error {
	return recordAudit(ctx, a.svc, entry.Action, entry.EntityType, entry.EntityID, entry.EntityName, entry.Changes)
}

type datasetAuditor struct{ svc *audit.Service }

func (a datasetAuditor) Record(ctx context.Context, entry dataset.AuditEntry) error {
	return recordAudit(ctx, a.svc, entry.Action, entry.EntityType, entry.EntityID, entry.EntityName, entry.Changes)
}

type pipelineAuditor struct{ svc *audit.Service }

func (a pipelineAuditor) Record(ctx context.Context, entry pipeline.AuditEntry) error {
	return recordAudit(ctx, a.svc, entry.Action, entry.EntityType, entry.EntityID, entry.EntityName, entry.Changes)
}

type qualityAuditor struct{ svc *audit.Service }

func (a qualityAuditor) Record(ctx context.Context, entry quality.AuditEntry) error {
	return recordAudit(ctx, a.svc, entry.Action, entry.EntityType, entry.EntityID, entry.EntityName, entry.Changes)
}

type governanceAuditor struct{ svc *audit.Service }

func (a governanceAuditor) Record(ctx context.Context, entry governance.AuditEntry) error {
	return recordAudit(ctx, a.svc, entry.Action, entry.EntityType, entry.EntityID, entry.EntityName, entry.Changes)
}

type lineageAuditor struct{ svc *audit.Service }

func (a lineageAuditor) Record(ctx context.Context, entry lineage.AuditEntry) error {
	return recordAudit(ctx, a.svc, entry.Action, entry.EntityType, entry.EntityID, entry.EntityName, entry.Changes)
}

type executionAuditor struct{ svc *audit.Service }

func (a executionAuditor) Record(ctx context.Context, entry execution.AuditEntry) error {
	return recordAudit(ctx, a.svc, entry.Action, entry.EntityType, entry.EntityID, entry.EntityName, entry.Changes)
}

// recordAudit maps a domain audit entry onto the audit service's request.
//
// The entity id is copied into a new pointer because the audit record outlives
// this call: taking the address of the caller's parameter would keep a pointer
// into a stack frame that has no meaning to the repository.
func recordAudit(
	ctx context.Context,
	svc *audit.Service,
	action, entityType string,
	entityID uuid.UUID,
	entityName string,
	changes map[string]any,
) error {
	return svc.Record(ctx, audit.RecordRequest{
		Action:     action,
		EntityType: entityType,
		EntityID:   &entityID,
		EntityName: entityName,
		ChangeSet:  changes,
		Outcome:    audit.OutcomeSuccess,
	})
}

// pipelineLookup adapts the pipeline service to the interface execution needs,
// keeping execution free of a dependency on the pipeline package.
type pipelineLookup struct {
	svc *pipeline.Service
}

func (l pipelineLookup) GetForExecution(ctx context.Context, id uuid.UUID) (*execution.PipelineExecutionInfo, error) {
	p, err := l.svc.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	return &execution.PipelineExecutionInfo{
		PipelineID:    p.ID,
		Name:          p.Name,
		Slug:          p.Slug,
		DAGID:         p.DAGID,
		MaxActiveRuns: p.MaxActiveRuns,
		TaskCount:     p.TaskCount,
		// A pipeline is deployable once it has a DAG id, which the pipeline
		// service sets when the graph is published to Airflow.
		IsDeployed: p.DAGID != "",
	}, nil
}
