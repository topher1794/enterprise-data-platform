// Package execution owns pipeline and task runs: triggering them through
// Airflow, tracking their state, and exposing run history.
package execution

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// RunStatus is the lifecycle state of a pipeline run.
type RunStatus string

// Run statuses.
const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunSuccess   RunStatus = "success"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
	RunSkipped   RunStatus = "skipped"
)

var runStatuses = map[RunStatus]bool{
	RunQueued: true, RunRunning: true, RunSuccess: true,
	RunFailed: true, RunCancelled: true, RunSkipped: true,
}

// Valid reports whether s is a supported status.
func (s RunStatus) Valid() bool { return runStatuses[s] }

// Terminal reports whether the run has finished.
func (s RunStatus) Terminal() bool {
	return s == RunSuccess || s == RunFailed || s == RunCancelled || s == RunSkipped
}

// Active reports whether the run is still in flight.
func (s RunStatus) Active() bool { return s == RunQueued || s == RunRunning }

// TaskStatus is the lifecycle state of one task attempt within a run.
type TaskStatus string

// Task statuses.
const (
	TaskQueued         TaskStatus = "queued"
	TaskRunning        TaskStatus = "running"
	TaskSuccess        TaskStatus = "success"
	TaskFailed         TaskStatus = "failed"
	TaskRetrying       TaskStatus = "retrying"
	TaskSkipped        TaskStatus = "skipped"
	TaskUpstreamFailed TaskStatus = "upstream_failed"
)

var taskStatuses = map[TaskStatus]bool{
	TaskQueued: true, TaskRunning: true, TaskSuccess: true, TaskFailed: true,
	TaskRetrying: true, TaskSkipped: true, TaskUpstreamFailed: true,
}

// Valid reports whether s is a supported task status.
func (s TaskStatus) Valid() bool { return taskStatuses[s] }

// Terminal reports whether the attempt has finished.
func (s TaskStatus) Terminal() bool {
	return s == TaskSuccess || s == TaskFailed || s == TaskSkipped || s == TaskUpstreamFailed
}

// TriggerType records why a run was started.
type TriggerType string

// Trigger types.
const (
	TriggerManual    TriggerType = "manual"
	TriggerScheduled TriggerType = "scheduled"
	TriggerAPI       TriggerType = "api"
	TriggerUpstream  TriggerType = "upstream"
	TriggerBackfill  TriggerType = "backfill"
)

var triggerTypes = map[TriggerType]bool{
	TriggerManual: true, TriggerScheduled: true, TriggerAPI: true,
	TriggerUpstream: true, TriggerBackfill: true,
}

// Valid reports whether t is a supported trigger type.
func (t TriggerType) Valid() bool { return triggerTypes[t] }

// Run is one execution of a pipeline.
type Run struct {
	ID         uuid.UUID `json:"id"`
	PipelineID uuid.UUID `json:"pipeline_id"`

	// RunID is the control plane's own stable identifier, used as Airflow's
	// idempotency key so a retried trigger request cannot start a second run.
	RunID uuid.UUID `json:"run_id"`
	// DAGRunID is Airflow's identifier, set once Airflow accepts the run.
	DAGRunID *string `json:"dag_run_id,omitempty"`

	Status      RunStatus   `json:"status"`
	TriggerType TriggerType `json:"trigger_type"`
	TriggeredBy string      `json:"triggered_by"`

	RunConfig map[string]any `json:"run_config"`
	Params    map[string]any `json:"params"`

	LogicalDate *time.Time `json:"logical_date,omitempty"`
	QueuedAt    time.Time  `json:"queued_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	DurationMs  *int64     `json:"duration_ms,omitempty"`

	TaskCount      int `json:"task_count"`
	SucceededCount int `json:"succeeded_count"`
	FailedCount    int `json:"failed_count"`
	SkippedCount   int `json:"skipped_count"`

	// QualityGatePassed is nil when no gate ran. It is distinct from the run
	// status: a run can succeed while failing a quality gate.
	QualityGatePassed *bool  `json:"quality_gate_passed,omitempty"`
	ErrorMessage      string `json:"error_message"`
	LogsURL           string `json:"logs_url"`

	Metadata map[string]any `json:"metadata"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Progress reports how far along a run is.
func (r Run) Progress() RunProgress {
	finished := r.SucceededCount + r.FailedCount + r.SkippedCount
	if r.TaskCount == 0 {
		// A run with no counted tasks reports as not started rather than
		// dividing by zero or claiming to be complete.
		return RunProgress{Finished: finished, Total: 0}
	}
	return RunProgress{
		Finished: finished,
		Total:    r.TaskCount,
		Percent:  int(float64(finished) / float64(r.TaskCount) * 100),
	}
}

// RunProgress summarises task completion within a run.
type RunProgress struct {
	Finished int `json:"finished"`
	Total    int `json:"total"`
	Percent  int `json:"percent"`
}

// TaskRun is one attempt at one task within a run.
type TaskRun struct {
	ID            uuid.UUID `json:"id"`
	PipelineRunID uuid.UUID `json:"pipeline_run_id"`
	// TaskID is nil when the task has since been deleted from the pipeline, so
	// the run history stays readable.
	TaskID    *uuid.UUID `json:"task_id,omitempty"`
	TaskKey   string     `json:"task_key"`
	TryNumber int        `json:"try_number"`

	Status       TaskStatus `json:"status"`
	OperatorType string     `json:"operator_type"`

	AttemptStartedAt *time.Time `json:"attempt_started_at,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
	DurationMs       *int64     `json:"duration_ms,omitempty"`
	ExitCode         *int       `json:"exit_code,omitempty"`

	OperatorOutput string `json:"operator_output"`
	ErrorMessage   string `json:"error_message"`
	LogsURL        string `json:"logs_url"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TriggerRunRequest asks for a pipeline run.
//
// The RunID is generated by the server when absent. Callers may supply one to
// make a trigger idempotent: a retried request carrying the same RunID is
// rejected rather than starting a second run.
type TriggerRunRequest struct {
	PipelineID uuid.UUID `json:"pipeline_id" validate:"required"`

	// RunID is the optional idempotency key.
	RunID *uuid.UUID `json:"run_id" validate:"omitempty"`

	TriggerType TriggerType `json:"trigger_type" validate:"required"`

	Params map[string]any `json:"params"`

	// LogicalDate pins the data interval, used for backfills and replays.
	LogicalDate *time.Time `json:"logical_date"`
}

// ListRunsQuery filters the run history.
type ListRunsQuery struct {
	platform.Page
	platform.Sort

	PipelineID  string   `json:"pipeline_id"`
	Status      []string `json:"status"`
	TriggerType string   `json:"trigger_type"`
	// ActiveOnly restricts to runs that have not finished.
	ActiveOnly bool `json:"active_only"`
	// Since and Until bound created_at. Both inclusive.
	Since *time.Time `json:"since"`
	Until *time.Time `json:"until"`
	// RunID looks a run up by the control plane's own identifier, which is how a
	// caller reconciles a trigger response with its own records.
	RunID string `json:"run_id"`
}

// SortableColumns maps API sort keys onto real columns.
func SortableColumns() map[string]string {
	return map[string]string{
		"created_at":  "created_at",
		"queued_at":   "queued_at",
		"started_at":  "started_at",
		"finished_at": "finished_at",
		"status":      "status",
		"duration_ms": "duration_ms",
	}
}

// DefaultSort orders newest first.
func DefaultSort() platform.Sort {
	return platform.Sort{By: "created_at", Order: "desc"}
}

// ListTaskRunsQuery filters task attempts.
type ListTaskRunsQuery struct {
	platform.Page
	platform.Sort

	PipelineRunID string   `json:"pipeline_run_id"`
	TaskKey       string   `json:"task_key"`
	Status        []string `json:"status"`
	// LatestAttemptOnly returns just the final attempt per task, which is what a
	// run summary shows.
	LatestAttemptOnly bool `json:"latest_attempt_only"`
}

// TaskRunSortableColumns maps API sort keys onto real columns.
func TaskRunSortableColumns() map[string]string {
	return map[string]string{
		"created_at":         "created_at",
		"finished_at":        "finished_at",
		"status":             "status",
		"try_number":         "try_number",
		"attempt_started_at": "attempt_started_at",
	}
}

// TaskRunDefaultSort orders by task key then attempt.
func TaskRunDefaultSort() platform.Sort {
	return platform.Sort{By: "created_at", Order: "asc"}
}

// SyncRequest applies a state transition observed from Airflow. It is written by
// the callback handler and the poller, not by clients.
type SyncRequest struct {
	PipelineRunID uuid.UUID `json:"pipeline_run_id" validate:"required"`

	Status       RunStatus  `json:"status" validate:"required"`
	DAGRunID     string     `json:"dag_run_id"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	ErrorMessage string     `json:"error_message" validate:"max=8000"`
	LogsURL      string     `json:"logs_url" validate:"max=2000"`
}

// SyncTaskRequest applies a task attempt transition.
type SyncTaskRequest struct {
	PipelineRunID uuid.UUID  `json:"pipeline_run_id" validate:"required"`
	TaskKey       string     `json:"task_key" validate:"required"`
	TaskID        *uuid.UUID `json:"task_id"`
	TryNumber     int        `json:"try_number" validate:"omitempty,gte=1"`

	Status       TaskStatus `json:"status" validate:"required"`
	OperatorType string     `json:"operator_type" validate:"max=100"`

	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	ExitCode   *int       `json:"exit_code"`

	OperatorOutput string `json:"operator_output"`
	ErrorMessage   string `json:"error_message" validate:"max=8000"`
	LogsURL        string `json:"logs_url" validate:"max=2000"`
}

func normaliseRunStatus(v string) RunStatus {
	return RunStatus(strings.ToLower(strings.TrimSpace(v)))
}

func normaliseTaskStatus(v string) TaskStatus {
	return TaskStatus(strings.ToLower(strings.TrimSpace(v)))
}

func normaliseTriggerType(v string) TriggerType {
	return TriggerType(strings.ToLower(strings.TrimSpace(v)))
}

// decodeJSONMap converts a nullable JSONB column into a map.
func decodeJSONMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}
