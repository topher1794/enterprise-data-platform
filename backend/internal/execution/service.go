package execution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/edp/edp-control-plane/internal/infrastructure/airflow"
	"github.com/edp/edp-control-plane/internal/infrastructure/postgres"
	"github.com/edp/edp-control-plane/internal/platform"
)

// Auditor records state changes.
type Auditor interface {
	Record(ctx context.Context, entry AuditEntry) error
}

// AuditEntry describes a state change to record.
type AuditEntry struct {
	Action     string
	EntityType string
	EntityID   uuid.UUID
	EntityName string
	Changes    map[string]any
}

// Audit entity types for this domain.
const (
	AuditEntityRun  = "pipeline_run"
	AuditEntityTask = "task_run"
)

// Orchestrator is the Airflow client, declared as an interface so the service
// can be tested without an Airflow instance.
type Orchestrator interface {
	TriggerRun(ctx context.Context, opts airflow.TriggerRunOptions) (airflow.DAGRun, error)
	GetRunState(ctx context.Context, dagID, dagRunID string) (airflow.DAGRun, error)
	Ping(ctx context.Context) error
}

// PipelineLookup resolves a pipeline's deployment details, which the execution
// domain needs but does not own.
type PipelineLookup interface {
	// GetForExecution returns the DAG id, max concurrent runs and task count
	// for a pipeline.
	GetForExecution(ctx context.Context, pipelineID uuid.UUID) (*PipelineExecutionInfo, error)
}

// PipelineExecutionInfo is the execution-relevant subset of a pipeline.
type PipelineExecutionInfo struct {
	PipelineID     uuid.UUID
	Name           string
	Slug           string
	DAGID          string
	MaxActiveRuns  int
	TaskCount      int
	IsDeployed     bool
	QualityGateIDs []uuid.UUID
}

// Service owns run lifecycle.
type Service struct {
	repo      Repository
	airflow   Orchestrator
	pipelines PipelineLookup
	auditor   Auditor
	log       *slog.Logger

	// staleAfter is how long a run may remain active before the reconciler
	// considers it stuck.
	staleAfter time.Duration
}

// ServiceOption customises a Service.
type ServiceOption func(*Service)

// WithAuditor attaches an audit sink.
func WithAuditor(a Auditor) ServiceOption {
	return func(s *Service) { s.auditor = a }
}

// WithLogger overrides the logger.
func WithLogger(l *slog.Logger) ServiceOption {
	return func(s *Service) {
		if l != nil {
			s.log = l
		}
	}
}

// WithOrchestrator attaches the Airflow client. Without one, triggering is
// unavailable but history and sync still work, which keeps a read-only
// deployment viable.
func WithOrchestrator(o Orchestrator) ServiceOption {
	return func(s *Service) { s.airflow = o }
}

// WithStaleAfter overrides the stuck-run threshold.
func WithStaleAfter(d time.Duration) ServiceOption {
	return func(s *Service) {
		if d > 0 {
			s.staleAfter = d
		}
	}
}

// NewService builds an execution service.
func NewService(repo Repository, pipelines PipelineLookup, opts ...ServiceOption) (*Service, error) {
	if repo == nil {
		return nil, errors.New("execution service: repository is required")
	}
	if pipelines == nil {
		return nil, errors.New("execution service: pipeline lookup is required")
	}

	s := &Service{
		repo:       repo,
		pipelines:  pipelines,
		log:        slog.Default(),
		staleAfter: 24 * time.Hour,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Trigger starts a pipeline run.
//
// The run record is written before Airflow is asked, so a run that Airflow
// accepted but that we failed to record still appears in history and the
// reconciler can find it. If Airflow rejects the trigger the record is marked
// failed rather than deleted, because a user who saw a trigger request deserves
// to see what happened to it.
func (s *Service) Trigger(ctx context.Context, req TriggerRunRequest, actor platform.Actor) (*Run, error) {
	if !req.TriggerType.Valid() {
		return nil, unsupportedEnum("trigger_type", req.TriggerType, listTriggerTypes()...)
	}

	pipeline, err := s.pipelines.GetForExecution(ctx, req.PipelineID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, platform.NewNotFound("pipeline", req.PipelineID)
		}
		return nil, platform.AsError(err)
	}
	if !pipeline.IsDeployed || pipeline.DAGID == "" {
		return nil, platform.NewConflict(
			"pipeline %q has not been deployed to Airflow, so it cannot be triggered",
			pipeline.Slug)
	}

	runID := uuid.New()
	if req.RunID != nil {
		runID = *req.RunID

		// Honour the idempotency key: a repeated trigger with the same RunID
		// returns the existing run instead of starting a second one.
		existing, err := s.repo.GetRunByRunID(ctx, runID)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, platform.AsError(err)
		}
	}

	// Respect max_active_runs before doing anything else, so a burst of trigger
	// requests cannot queue unbounded runs in the orchestrator.
	active, err := s.repo.CountActiveRuns(ctx, pipeline.PipelineID)
	if err != nil {
		return nil, platform.AsError(err)
	}
	if pipeline.MaxActiveRuns > 0 && active >= pipeline.MaxActiveRuns {
		return nil, platform.NewConflict(
			"pipeline %q already has %d active run(s), which is its maximum of %d",
			pipeline.Slug, active, pipeline.MaxActiveRuns).
			WithFields(fieldError("pipeline_id", "max_active_runs",
				"wait for an active run to finish, or raise max_active_runs"))
	}

	run := &Run{
		PipelineID:  pipeline.PipelineID,
		RunID:       runID,
		Status:      RunQueued,
		TriggerType: normaliseTriggerType(string(req.TriggerType)),
		TriggeredBy: actor.ID,
		RunConfig:   map[string]any{},
		Params:      req.Params,
		LogicalDate: req.LogicalDate,
		TaskCount:   pipeline.TaskCount,
		Metadata:    map[string]any{},
	}
	if run.Params == nil {
		run.Params = map[string]any{}
	}

	if err := s.repo.CreateRun(ctx, run); err != nil {
		if postgres.IsUniqueViolation(err, "") {
			return nil, platform.NewConflict(
				"a run with id %s already exists", runID).WithCause(err)
		}
		return nil, platform.AsError(err)
	}

	s.record(ctx, AuditEntry{
		Action:     "triggered",
		EntityType: AuditEntityRun,
		EntityID:   run.ID,
		EntityName: pipeline.Slug,
		Changes:    map[string]any{"run": runSnapshot(run)},
	})

	if s.airflow == nil {
		run.ErrorMessage = "no orchestrator is configured, so the run was recorded but not started"
		if err := s.repo.UpdateRun(ctx, run); err != nil {
			return nil, platform.AsError(err)
		}
		return run, platform.NewUnavailable(
			"the orchestrator is not configured; the run was recorded in queued state")
	}

	dagRun, err := s.airflow.TriggerRun(ctx, airflow.TriggerRunOptions{
		DAGID:       pipeline.DAGID,
		RunID:       runID.String(),
		Conf:        req.Params,
		LogicalDate: req.LogicalDate,
	})
	if err != nil {
		run.Status = RunFailed
		run.ErrorMessage = truncate(triggerFailureMessage(err), 8000)
		finished := time.Now().UTC()
		run.FinishedAt = &finished

		if updateErr := s.repo.UpdateRun(ctx, run); updateErr != nil {
			s.log.ErrorContext(ctx, "failed to record trigger failure",
				"run_id", run.ID, "error", updateErr)
		}

		return nil, platform.NewUnavailable(
			"the orchestrator rejected the trigger: %s", run.ErrorMessage).
			WithCause(err)
	}

	run.DAGRunID = &dagRun.DAGRunID
	if status := normaliseRunStatus(dagRun.State); status.Valid() && status != RunQueued {
		run.Status = status
	}

	if err := s.repo.UpdateRun(ctx, run); err != nil {
		return nil, platform.AsError(err)
	}

	return run, nil
}

// Get returns a single run.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Run, error) {
	run, err := s.repo.GetRunByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, platform.NewNotFound("pipeline run", id)
		}
		return nil, platform.AsError(err)
	}
	return run, nil
}

// GetByRunID returns a run by its stable idempotency key.
func (s *Service) GetByRunID(ctx context.Context, runID uuid.UUID) (*Run, error) {
	run, err := s.repo.GetRunByRunID(ctx, runID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, platform.NewNotFound("pipeline run", runID)
		}
		return nil, platform.AsError(err)
	}
	return run, nil
}

// List returns a page of runs.
func (s *Service) List(ctx context.Context, q ListRunsQuery) (platform.PageResult[*Run], error) {
	if q.Since != nil && q.Until != nil && q.Until.Before(*q.Since) {
		return platform.PageResult[*Run]{}, platform.NewBadRequest(
			"until must not be earlier than since")
	}

	page, err := s.repo.ListRuns(ctx, q)
	if err != nil {
		return platform.PageResult[*Run]{}, platform.AsError(err)
	}
	return page, nil
}

// Sync applies a state transition reported by Airflow.
//
// Transitions are only accepted in a forward direction. Airflow callbacks can
// arrive out of order, and applying a stale "running" after a "success" would
// leave the run stuck in history as never finished.
func (s *Service) Sync(ctx context.Context, req SyncRequest) (*Run, error) {
	status := normaliseRunStatus(string(req.Status))
	if !status.Valid() {
		return nil, unsupportedEnum("status", req.Status, listRunStatuses()...)
	}

	run, err := s.Get(ctx, req.PipelineRunID)
	if err != nil {
		return nil, err
	}

	if run.Status.Terminal() && !status.Terminal() {
		// The run already finished; a late non-terminal report is stale.
		s.log.DebugContext(ctx, "ignoring stale run status report",
			"run_id", run.ID, "current", run.Status, "reported", status)
		return run, nil
	}
	if run.Status == status && !status.Terminal() {
		return run, nil
	}

	run.Status = status
	if req.DAGRunID != "" {
		run.DAGRunID = &req.DAGRunID
	}
	if req.StartedAt != nil {
		run.StartedAt = req.StartedAt
	} else if status == RunRunning && run.StartedAt == nil {
		now := time.Now().UTC()
		run.StartedAt = &now
	}
	if req.FinishedAt != nil {
		run.FinishedAt = req.FinishedAt
	} else if status.Terminal() && run.FinishedAt == nil {
		now := time.Now().UTC()
		run.FinishedAt = &now
	}
	if req.ErrorMessage != "" {
		run.ErrorMessage = truncate(req.ErrorMessage, 8000)
	}
	if req.LogsURL != "" {
		run.LogsURL = req.LogsURL
	}

	// Derive the duration from start to finish rather than trusting a reported
	// value, so the arithmetic is consistent with the timestamps on the row.
	if status.Terminal() && run.StartedAt != nil && run.FinishedAt != nil {
		run.DurationMs = durationMs(*run.StartedAt, *run.FinishedAt)
	}

	if err := s.repo.UpdateRun(ctx, run); err != nil {
		return nil, platform.AsError(err)
	}

	if status.Terminal() {
		// Refresh the counters before reporting the run as finished.
		if err := s.repo.RecountRunTasks(ctx, run.ID); err != nil {
			s.log.WarnContext(ctx, "failed to recount run tasks",
				"run_id", run.ID, "error", err)
		}

		s.record(ctx, AuditEntry{
			Action:     runActionFor(status),
			EntityType: AuditEntityRun,
			EntityID:   run.ID,
			Changes:    map[string]any{"status": status, "duration_ms": run.DurationMs},
		})
	}

	return run, nil
}

// SyncTask applies a task attempt transition reported by Airflow.
func (s *Service) SyncTask(ctx context.Context, req SyncTaskRequest) (*TaskRun, error) {
	status := normaliseTaskStatus(string(req.Status))
	if !status.Valid() {
		return nil, unsupportedEnum("status", req.Status, listTaskStatuses()...)
	}

	taskKey := strings.TrimSpace(req.TaskKey)
	if taskKey == "" {
		return nil, missingField("task_key", "the task key identifies which task ran")
	}

	run, err := s.Get(ctx, req.PipelineRunID)
	if err != nil {
		return nil, err
	}

	// The run has already finished, so a task report arriving now is stale; it
	// would otherwise move a run's counters after the run was closed out.
	if run.Status.Terminal() {
		s.log.DebugContext(ctx, "ignoring task report for a finished run",
			"run_id", run.ID, "task_key", taskKey, "run_status", run.Status)
		return nil, nil
	}

	tryNumber := req.TryNumber
	if tryNumber < 1 {
		// An omitted attempt means the first one; a retry is always reported
		// explicitly by Airflow.
		tryNumber = 1
	}

	task := &TaskRun{
		PipelineRunID:    run.ID,
		TaskID:           req.TaskID,
		TaskKey:          taskKey,
		TryNumber:        tryNumber,
		Status:           status,
		OperatorType:     strings.TrimSpace(req.OperatorType),
		AttemptStartedAt: req.StartedAt,
		FinishedAt:       req.FinishedAt,
		ExitCode:         req.ExitCode,
		OperatorOutput:   truncate(req.OperatorOutput, 16000),
		ErrorMessage:     truncate(req.ErrorMessage, 8000),
		LogsURL:          truncate(req.LogsURL, 2000),
	}

	if task.AttemptStartedAt == nil && status == TaskRunning {
		now := time.Now().UTC()
		task.AttemptStartedAt = &now
	}
	if task.FinishedAt == nil && status.Terminal() {
		now := time.Now().UTC()
		task.FinishedAt = &now
	}
	if status.Terminal() && task.AttemptStartedAt != nil && task.FinishedAt != nil {
		task.DurationMs = durationMs(*task.AttemptStartedAt, *task.FinishedAt)
	}

	if err := s.repo.UpsertTaskRun(ctx, task); err != nil {
		return nil, platform.AsError(err)
	}

	if status == TaskFailed {
		s.record(ctx, AuditEntry{
			Action:     "failed",
			EntityType: AuditEntityTask,
			EntityID:   task.ID,
			EntityName: taskKey,
			Changes: map[string]any{
				"try_number": tryNumber,
				"error":      task.ErrorMessage,
			},
		})
	}

	return task, nil
}

// ListTaskRuns returns a page of task attempts.
func (s *Service) ListTaskRuns(ctx context.Context, q ListTaskRunsQuery) (platform.PageResult[*TaskRun], error) {
	page, err := s.repo.ListTaskRuns(ctx, q)
	if err != nil {
		return platform.PageResult[*TaskRun]{}, platform.AsError(err)
	}
	return page, nil
}

// GetTaskRun returns a single task attempt.
func (s *Service) GetTaskRun(ctx context.Context, id uuid.UUID) (*TaskRun, error) {
	task, err := s.repo.GetTaskRunByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, platform.NewNotFound("task run", id)
		}
		return nil, platform.AsError(err)
	}
	return task, nil
}

// SetQualityGate records whether a run's blocking quality checks passed. It is
// separate from the run status because a run can execute successfully and still
// fail its gate.
func (s *Service) SetQualityGate(ctx context.Context, runID uuid.UUID, passed bool) (*Run, error) {
	run, err := s.Get(ctx, runID)
	if err != nil {
		return nil, err
	}

	run.QualityGatePassed = &passed
	if err := s.repo.UpdateRun(ctx, run); err != nil {
		return nil, platform.AsError(err)
	}

	action := "quality_gate_passed"
	if !passed {
		action = "quality_gate_failed"
	}
	s.record(ctx, AuditEntry{
		Action:     action,
		EntityType: AuditEntityRun,
		EntityID:   run.ID,
		Changes:    map[string]any{"passed": passed},
	})

	return run, nil
}

// Cancel asks Airflow to stop a run.
//
// A run that has already finished cannot be cancelled; reporting that plainly
// is more useful than silently returning the run unchanged.
func (s *Service) Cancel(ctx context.Context, id uuid.UUID, actor platform.Actor) (*Run, error) {
	run, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if run.Status.Terminal() {
		return nil, platform.NewConflict(
			"run %s already finished with status %s and cannot be cancelled",
			run.RunID, run.Status)
	}

	// The control plane records the cancellation immediately. Airflow may keep
	// running for a moment, and the reconciler will confirm the final state.
	run.Status = RunCancelled
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	if run.StartedAt != nil {
		run.DurationMs = durationMs(*run.StartedAt, finished)
	}

	if err := s.repo.UpdateRun(ctx, run); err != nil {
		return nil, platform.AsError(err)
	}

	s.record(ctx, AuditEntry{
		Action:     "cancelled",
		EntityType: AuditEntityRun,
		EntityID:   run.ID,
		Changes:    map[string]any{"cancelled_by": actor.ID},
	})

	return run, nil
}

// Reconcile brings locally-stuck runs back in line with Airflow. It is intended
// to be called on a schedule and returns how many runs it corrected.
func (s *Service) Reconcile(ctx context.Context) (int, error) {
	if s.airflow == nil {
		return 0, nil
	}

	stale, err := s.repo.StaleRuns(ctx, time.Now().UTC().Add(-s.staleAfter))
	if err != nil {
		return 0, platform.AsError(err)
	}

	corrected := 0
	for i := range stale {
		run := stale[i]
		if run.DAGRunID == nil || *run.DAGRunID == "" {
			// Airflow never acknowledged this run, so there is nothing to ask
			// about. Leave it queued for the operator to look at.
			continue
		}

		dagRun, err := s.airflow.GetRunState(ctx, "", *run.DAGRunID)
		if err != nil {
			s.log.WarnContext(ctx, "failed to reconcile run",
				"run_id", run.ID, "dag_run_id", *run.DAGRunID, "error", err)
			continue
		}

		status := normaliseRunStatus(dagRun.State)
		if !status.Valid() {
			continue
		}
		if status == run.Status {
			continue
		}

		run.Status = status
		if status.Terminal() && run.FinishedAt == nil {
			finished := time.Now().UTC()
			run.FinishedAt = &finished
		}
		if err := s.repo.UpdateRun(ctx, &run); err != nil {
			s.log.WarnContext(ctx, "failed to record reconciled run state",
				"run_id", run.ID, "error", err)
			continue
		}

		corrected++
		s.log.InfoContext(ctx, "reconciled stale run",
			"run_id", run.ID, "from", "active", "to", status)
	}

	return corrected, nil
}

// runSnapshot reduces a run to its audit-relevant fields.
func runSnapshot(r *Run) map[string]any {
	if r == nil {
		return nil
	}
	return map[string]any{
		"pipeline_id":  r.PipelineID,
		"run_id":       r.RunID,
		"status":       r.Status,
		"trigger_type": r.TriggerType,
		"triggered_by": r.TriggeredBy,
		"task_count":   r.TaskCount,
		"logical_date": r.LogicalDate,
	}
}

// runActionFor maps a terminal status to the audit action that describes it.
func runActionFor(status RunStatus) string {
	switch status {
	case RunSuccess:
		return "completed"
	case RunFailed:
		return "failed"
	case RunCancelled:
		return "cancelled"
	case RunSkipped:
		return "skipped"
	default:
		return "updated"
	}
}

// durationMs returns the milliseconds between two instants, never negative.
func durationMs(start, end time.Time) *int64 {
	ms := end.Sub(start).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return &ms
}

// truncate bounds a string to a maximum length, marking that it was cut.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// triggerFailureMessage renders an orchestrator error for storage.
func triggerFailureMessage(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%s", err.Error())
}

// record writes an audit entry, tolerating its absence.
func (s *Service) record(ctx context.Context, entry AuditEntry) {
	if s.auditor == nil {
		return
	}
	if err := s.auditor.Record(ctx, entry); err != nil {
		s.log.ErrorContext(ctx, "failed to record audit event",
			"action", entry.Action, "entity_type", entry.EntityType,
			"entity_id", entry.EntityID, "error", err)
	}
}
