package execution

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// ExecutionService is the business contract the handler depends on.
type ExecutionService interface {
	Trigger(ctx context.Context, req TriggerRunRequest, actor platform.Actor) (*Run, error)
	Get(ctx context.Context, id uuid.UUID) (*Run, error)
	GetByRunID(ctx context.Context, runID uuid.UUID) (*Run, error)
	List(ctx context.Context, q ListRunsQuery) (platform.PageResult[*Run], error)
	Cancel(ctx context.Context, id uuid.UUID, actor platform.Actor) (*Run, error)

	// Sync and SyncTask are the orchestrator callback surface. They are mounted
	// separately from the user-facing routes so they can be protected by a
	// different credential.
	Sync(ctx context.Context, req SyncRequest) (*Run, error)
	SyncTask(ctx context.Context, req SyncTaskRequest) (*TaskRun, error)

	ListTaskRuns(ctx context.Context, q ListTaskRunsQuery) (platform.PageResult[*TaskRun], error)
	GetTaskRun(ctx context.Context, id uuid.UUID) (*TaskRun, error)

	SetQualityGate(ctx context.Context, runID uuid.UUID, passed bool) (*Run, error)
}

// Handler exposes the execution domain over HTTP.
type Handler struct {
	svc ExecutionService
}

// NewHandler builds an execution handler.
func NewHandler(svc ExecutionService) *Handler {
	return &Handler{svc: svc}
}

// Mount registers the run routes on r.
//
//	GET    /pipeline-runs                      list
//	POST   /pipeline-runs                      trigger a run
//	GET    /pipeline-runs/{runID}              read
//	POST   /pipeline-runs/{runID}/cancel       cancel
//	POST   /pipeline-runs/{runID}/quality-gate record a gate verdict
//	GET    /pipeline-runs/{runID}/tasks        task attempts
//	GET    /pipeline-runs/by-key/{runID}       read by the stable run id
//	POST   /orchestrator/runs/{runID}/sync     orchestrator run callback
//	POST   /orchestrator/tasks/sync            orchestrator task callback
func (h *Handler) Mount(r chi.Router) {
	r.Post("/orchestrator/runs/{runID}/sync", h.Sync)
	r.Post("/orchestrator/tasks/sync", h.SyncTask)

	r.Get("/pipeline-runs/by-key/{runID}", h.GetByKey)

	r.Route("/pipeline-runs", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/", h.Trigger)

		r.Route("/{runID}", func(r chi.Router) {
			r.Get("/", h.Get)
			r.Post("/cancel", h.Cancel)
			r.Post("/quality-gate", h.QualityGate)
			r.Get("/tasks", h.ListTasks)
			r.Get("/tasks/{taskRunID}", h.GetTaskRun)
		})
	})
}

// List handles GET /pipeline-runs.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	page, err := platform.ParsePage(r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	sort, err := platform.ParseSort(r, SortableColumns(), DefaultSort())
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	query := r.URL.Query()

	since, err := parseTime(query.Get("since"), "since")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	until, err := parseTime(query.Get("until"), "until")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	activeOnly := false
	if raw := strings.TrimSpace(query.Get("active_only")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			platform.Fail(w, r, platform.NewBadRequest("active_only must be true or false"))
			return
		}
		activeOnly = parsed
	}

	result, err := h.svc.List(r.Context(), ListRunsQuery{
		Page:        page,
		Sort:        sort,
		PipelineID:  strings.TrimSpace(query.Get("pipeline_id")),
		Status:      query["status"],
		TriggerType: strings.TrimSpace(query.Get("trigger_type")),
		ActiveOnly:  activeOnly,
		Since:       since,
		Until:       until,
		RunID:       strings.TrimSpace(query.Get("run_id")),
	})
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, result)
}

// Trigger handles POST /pipeline-runs.
func (h *Handler) Trigger(w http.ResponseWriter, r *http.Request) {
	req, err := platform.FromRequest[TriggerRunRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	run, err := h.svc.Trigger(r.Context(), req, actorOrAnonymous(r))
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/"+run.ID.String())
	platform.JSON(w, http.StatusCreated, run)
}

// Get handles GET /pipeline-runs/{runID}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "runID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	found, err := h.svc.Get(r.Context(), id)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, found)
}

// GetByKey handles GET /pipeline-runs/by-key/{runID}.
func (h *Handler) GetByKey(w http.ResponseWriter, r *http.Request) {
	runID, err := pathUUID(r, "runID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	found, err := h.svc.GetByRunID(r.Context(), runID)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, found)
}

// Cancel handles POST /pipeline-runs/{runID}/cancel.
func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "runID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	run, err := h.svc.Cancel(r.Context(), id, actorOrAnonymous(r))
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, run)
}

// QualityGateRequest records a quality gate verdict for a run.
type QualityGateRequest struct {
	Passed bool `json:"passed"`
}

// QualityGate handles POST /pipeline-runs/{runID}/quality-gate.
func (h *Handler) QualityGate(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "runID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	body, err := platform.FromRequest[QualityGateRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	run, err := h.svc.SetQualityGate(r.Context(), id, body.Passed)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, run)
}

// ListTasks handles GET /pipeline-runs/{runID}/tasks.
func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	runID, err := pathUUID(r, "runID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	page, err := platform.ParsePage(r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	sort, err := platform.ParseSort(r, TaskRunSortableColumns(), TaskRunDefaultSort())
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	query := r.URL.Query()

	latestOnly := false
	if raw := strings.TrimSpace(query.Get("latest_attempt_only")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			platform.Fail(w, r, platform.NewBadRequest(
				"latest_attempt_only must be true or false"))
			return
		}
		latestOnly = parsed
	}

	result, err := h.svc.ListTaskRuns(r.Context(), ListTaskRunsQuery{
		Page:              page,
		Sort:              sort,
		PipelineRunID:     runID.String(),
		TaskKey:           strings.TrimSpace(query.Get("task_key")),
		Status:            query["status"],
		LatestAttemptOnly: latestOnly,
	})
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, result)
}

// GetTaskRun handles GET /pipeline-runs/{runID}/tasks/{taskRunID}.
func (h *Handler) GetTaskRun(w http.ResponseWriter, r *http.Request) {
	if _, err := pathUUID(r, "runID"); err != nil {
		platform.Fail(w, r, err)
		return
	}

	id, err := pathUUID(r, "taskRunID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	found, err := h.svc.GetTaskRun(r.Context(), id)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, found)
}

// Sync handles POST /orchestrator/runs/{runID}/sync.
func (h *Handler) Sync(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "runID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	req, err := platform.FromRequest[SyncRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	req.PipelineRunID = id

	run, err := h.svc.Sync(r.Context(), req)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, run)
}

// SyncTask handles POST /orchestrator/tasks/sync.
func (h *Handler) SyncTask(w http.ResponseWriter, r *http.Request) {
	req, err := platform.FromRequest[SyncTaskRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	task, err := h.svc.SyncTask(r.Context(), req)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	// A report for an already-finished run is dropped as a no-op, so 202 with an
	// empty body is the honest response rather than a 404.
	if task == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	platform.JSON(w, http.StatusOK, task)
}

// pathUUID extracts and validates a UUID path parameter.
func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, platform.NewBadRequest("%s must be a valid UUID", name)
	}
	return id, nil
}

// parseTime reads an RFC 3339 timestamp, returning nil when absent.
func parseTime(raw, name string) (*time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}

	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return nil, platform.NewBadRequest(
			"%s must be an RFC 3339 timestamp, for example 2026-01-31T09:00:00Z", name)
	}
	utc := parsed.UTC()
	return &utc, nil
}

// actorOrAnonymous returns the authenticated principal, or a synthetic one when
// authentication is disabled.
func actorOrAnonymous(r *http.Request) platform.Actor {
	if actor, ok := platform.ActorFrom(r.Context()); ok {
		return actor
	}
	return platform.Actor{ID: "anonymous", Roles: []string{}, Scopes: []string{}}
}
