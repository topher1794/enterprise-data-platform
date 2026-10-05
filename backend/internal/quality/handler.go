package quality

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

// QualityService is the business contract the handler depends on.
type QualityService interface {
	CreateRule(ctx context.Context, req CreateRuleRequest, actor platform.Actor) (*Rule, error)
	GetRule(ctx context.Context, id uuid.UUID) (*Rule, error)
	ListRules(ctx context.Context, q ListRulesQuery) (platform.PageResult[*Rule], error)
	UpdateRule(ctx context.Context, id uuid.UUID, req UpdateRuleRequest, actor platform.Actor) (*Rule, error)
	DeleteRule(ctx context.Context, id uuid.UUID, actor platform.Actor) error

	ListChecks(ctx context.Context, q ListCheckRunsQuery) (platform.PageResult[*CheckRun], error)
	GetCheck(ctx context.Context, id uuid.UUID) (*CheckRun, error)

	Health(ctx context.Context, datasetID uuid.UUID) (*RuleHealth, error)
}

// Handler exposes the quality domain over HTTP.
type Handler struct {
	svc QualityService
}

// NewHandler builds a quality handler.
func NewHandler(svc QualityService) *Handler {
	return &Handler{svc: svc}
}

// Mount registers the quality routes on r.
//
//	GET    /quality-rules                              list
//	POST   /quality-rules                              create
//	GET    /quality-rules/{ruleID}                     read
//	PATCH  /quality-rules/{ruleID}                     partial update
//	DELETE /quality-rules/{ruleID}                     archive
//	GET    /quality-rules/{ruleID}/checks              evaluation history
//	GET    /quality-checks/{checkID}                   read one evaluation
//	GET    /quality-checks                             list evaluations
//	GET    /datasets/{datasetID}/quality-health        current rule health
func (h *Handler) Mount(r chi.Router) {
	r.Get("/quality-checks", h.ListChecks)
	r.Get("/quality-checks/{checkID}", h.GetCheck)

	r.Route("/quality-rules", func(r chi.Router) {
		r.Get("/", h.ListRules)
		r.Post("/", h.CreateRule)

		r.Route("/{ruleID}", func(r chi.Router) {
			r.Get("/", h.GetRule)
			r.Patch("/", h.UpdateRule)
			r.Delete("/", h.DeleteRule)
			r.Get("/checks", h.ListChecksForRule)
		})
	})

	r.Get("/datasets/{datasetID}/quality-health", h.Health)
}

// ListRules handles GET /quality-rules.
func (h *Handler) ListRules(w http.ResponseWriter, r *http.Request) {
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

	result, err := h.svc.ListRules(r.Context(), ListRulesQuery{
		Page:       page,
		Sort:       sort,
		DatasetID:  strings.TrimSpace(query.Get("dataset_id")),
		PipelineID: strings.TrimSpace(query.Get("pipeline_id")),
		RuleType:   query["rule_type"],
		Severity:   query["severity"],
		Dimension:  query["dimension"],
		Enabled:    optionalBool(query, "enabled"),
		Blocking:   optionalBool(query, "blocking"),
		Search:     strings.TrimSpace(query.Get("search")),
		Tag:        query["tag"],
	})
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, result)
}

// CreateRule handles POST /quality-rules.
func (h *Handler) CreateRule(w http.ResponseWriter, r *http.Request) {
	req, err := platform.FromRequest[CreateRuleRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	created, err := h.svc.CreateRule(r.Context(), req, actorOrAnonymous(r))
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	w.Header().Set("Location",
		strings.TrimSuffix(r.URL.Path, "/")+"/"+created.ID.String())
	platform.JSON(w, http.StatusCreated, created)
}

// GetRule handles GET /quality-rules/{ruleID}.
func (h *Handler) GetRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "ruleID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	found, err := h.svc.GetRule(r.Context(), id)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, found)
}

// UpdateRule handles PATCH /quality-rules/{ruleID}.
func (h *Handler) UpdateRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "ruleID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	req, err := platform.FromRequest[UpdateRuleRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	if req.isEmpty() {
		platform.Fail(w, r, platform.NewBadRequest(
			"the request body must contain at least one field to change"))
		return
	}

	updated, err := h.svc.UpdateRule(r.Context(), id, req, actorOrAnonymous(r))
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, updated)
}

// DeleteRule handles DELETE /quality-rules/{ruleID}.
func (h *Handler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "ruleID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	if err := h.svc.DeleteRule(r.Context(), id, actorOrAnonymous(r)); err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.NoContent(w)
}

// ListChecks handles GET /quality-checks.
func (h *Handler) ListChecks(w http.ResponseWriter, r *http.Request) {
	page, err := platform.ParsePage(r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	sort, err := platform.ParseSort(r, CheckRunSortableColumns(), CheckRunDefaultSort())
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

	result, err := h.svc.ListChecks(r.Context(), ListCheckRunsQuery{
		Page:          page,
		Sort:          sort,
		RuleID:        strings.TrimSpace(query.Get("rule_id")),
		DatasetID:     strings.TrimSpace(query.Get("dataset_id")),
		PipelineRunID: strings.TrimSpace(query.Get("pipeline_run_id")),
		Status:        query["status"],
		Since:         since,
		Until:         until,
	})
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, result)
}

// ListChecksForRule handles GET /quality-rules/{ruleID}/checks.
func (h *Handler) ListChecksForRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "ruleID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	page, err := platform.ParsePage(r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	sort, err := platform.ParseSort(r, CheckRunSortableColumns(), CheckRunDefaultSort())
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

	result, err := h.svc.ListChecks(r.Context(), ListCheckRunsQuery{
		Page:   page,
		Sort:   sort,
		RuleID: id.String(),
		Status: query["status"],
		Since:  since,
		Until:  until,
	})
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, result)
}

// GetCheck handles GET /quality-checks/{checkID}.
func (h *Handler) GetCheck(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "checkID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	found, err := h.svc.GetCheck(r.Context(), id)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, found)
}

// Health handles GET /datasets/{datasetID}/quality-health.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	datasetID, err := pathUUID(r, "datasetID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	health, err := h.svc.Health(r.Context(), datasetID)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, health)
}

// optionalBool reads an optional boolean query parameter. An absent or empty
// value yields nil, meaning "no filter".
func optionalBool(query map[string][]string, name string) *bool {
	values, ok := query[name]
	if !ok || len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return nil
	}

	parsed, err := strconv.ParseBool(strings.TrimSpace(values[0]))
	if err != nil {
		return nil
	}
	return &parsed
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

// pathUUID extracts and validates a UUID path parameter.
func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, platform.NewBadRequest("%s must be a valid UUID", name)
	}
	return id, nil
}

// actorOrAnonymous returns the authenticated principal, or a synthetic one when
// authentication is disabled.
func actorOrAnonymous(r *http.Request) platform.Actor {
	if actor, ok := platform.ActorFrom(r.Context()); ok {
		return actor
	}
	return platform.Actor{ID: "anonymous", Roles: []string{}, Scopes: []string{}}
}
