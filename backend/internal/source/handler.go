package source

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// SourceService is the business contract the handler depends on. Declaring it
// here keeps the transport layer decoupled from the concrete service type and
// makes the handler straightforward to test.
type SourceService interface {
	Create(ctx context.Context, req CreateSourceRequest, actor platform.Actor) (*Source, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Source, error)
	GetBySlug(ctx context.Context, slug string) (*Source, error)
	List(ctx context.Context, q ListSourcesQuery) (platform.PageResult[*Source], error)
	Update(ctx context.Context, id uuid.UUID, req UpdateSourceRequest, actor platform.Actor) (*Source, error)
	Delete(ctx context.Context, id uuid.UUID, actor platform.Actor) error
	RecordHealthCheck(ctx context.Context, id uuid.UUID, status HealthStatus, checkedAt time.Time) error
}

// Handler exposes the source domain over HTTP.
type Handler struct {
	svc SourceService
}

// NewHandler builds a source handler.
func NewHandler(svc SourceService) *Handler {
	return &Handler{svc: svc}
}

// Mount registers the source routes on r. The caller is responsible for
// applying authentication and authorization middleware beforehand.
//
//	GET    /sources                   list
//	POST   /sources                   create
//	GET    /sources/{sourceID}        read
//	PATCH  /sources/{sourceID}        partial update
//	DELETE /sources/{sourceID}        soft delete
//	GET    /sources/by-slug/{slug}    read by slug
//	POST   /sources/{sourceID}/health-checks   record a probe result
func (h *Handler) Mount(r chi.Router) {
	// Registered before the parameterised routes so "by-slug" is not captured
	// as a source ID.
	r.Get("/sources/by-slug/{slug}", h.GetBySlug)

	r.Route("/sources", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/", h.Create)

		r.Route("/{sourceID}", func(r chi.Router) {
			r.Get("/", h.Get)
			r.Patch("/", h.Update)
			r.Delete("/", h.Delete)
			r.Post("/health-checks", h.RecordHealthCheck)
		})
	})
}

// List handles GET /sources.
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
	result, err := h.svc.List(r.Context(), ListSourcesQuery{
		Page:        page,
		Sort:        sort,
		Type:        strings.ToLower(query.Get("type")),
		Status:      strings.ToLower(query.Get("status")),
		Environment: strings.ToLower(query.Get("environment")),
		OwnerID:     strings.TrimSpace(query.Get("owner_id")),
		Search:      strings.TrimSpace(query.Get("q")),
		Tag:         query["tag"],
	})
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, result)
}

// Create handles POST /sources.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	req, err := platform.FromRequest[CreateSourceRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	created, err := h.svc.Create(r.Context(), req, actorOrAnonymous(r))
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	w.Header().Set("Location", resourceLocation(r, created.ID))
	platform.JSON(w, http.StatusCreated, created)
}

// Get handles GET /sources/{sourceID}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "sourceID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	found, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, found)
}

// GetBySlug handles GET /sources/by-slug/{slug}.
func (h *Handler) GetBySlug(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	if slug == "" {
		platform.Fail(w, r, platform.NewBadRequest("slug is required"))
		return
	}

	found, err := h.svc.GetBySlug(r.Context(), slug)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, found)
}

// Update handles PATCH /sources/{sourceID}.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "sourceID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	req, err := platform.FromRequest[UpdateSourceRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	// Reject a no-op explicitly: silently returning 200 hides client bugs.
	if isEmptyUpdate(req) {
		platform.Fail(w, r, platform.NewBadRequest(
			"the request body must contain at least one field to change"))
		return
	}

	updated, err := h.svc.Update(r.Context(), id, req, actorOrAnonymous(r))
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, updated)
}

// Delete handles DELETE /sources/{sourceID}.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "sourceID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	if err := h.svc.Delete(r.Context(), id, actorOrAnonymous(r)); err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.NoContent(w)
}

// HealthCheckRequest is the body accepted by the health-check endpoint.
type HealthCheckRequest struct {
	// Status is required. An omitted status is rejected rather than assumed,
	// so a caller cannot silently record a passing probe.
	Status HealthStatus `json:"status" validate:"required"`
	// CheckedAt is an RFC 3339 timestamp defaulting to now.
	CheckedAt string `json:"checked_at"`
}

// RecordHealthCheck handles POST /sources/{sourceID}/health-checks. It is
// called by the connectivity prober and by operators after manual inspection.
func (h *Handler) RecordHealthCheck(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "sourceID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	req, err := platform.FromRequest[HealthCheckRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	if !req.Status.Valid() {
		platform.Fail(w, r, platform.NewValidation(
			"status must be one of: healthy, degraded, unhealthy").
			WithFields(fieldError("status", "oneof", "unsupported health status")))
		return
	}

	checkedAt := time.Now().UTC()
	if strings.TrimSpace(req.CheckedAt) != "" {
		parsed, err := time.Parse(time.RFC3339, req.CheckedAt)
		if err != nil {
			platform.Fail(w, r, platform.NewValidation(
				"checked_at must be an RFC 3339 timestamp, for example 2026-01-31T09:00:00Z").
				WithFields(fieldError("checked_at", "datetime", "not a valid RFC 3339 timestamp")))
			return
		}
		checkedAt = parsed.UTC()
	}

	if err := h.svc.RecordHealthCheck(r.Context(), id, req.Status, checkedAt); err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.NoContent(w)
}

// pathUUID extracts and validates a UUID path parameter, bounding the value
// echoed back in the error message.
func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	raw := chi.URLParam(r, name)
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, platform.NewBadRequest(
			"%s must be a valid UUID", name).
			WithFields(fieldError(name, "uuid", "must be a valid UUID"))
	}
	return id, nil
}

// actorOrAnonymous returns the authenticated principal, or a synthetic
// anonymous one when authentication is disabled. Routes that require a real
// principal are additionally wrapped in middleware.RequireAuthentication.
func actorOrAnonymous(r *http.Request) platform.Actor {
	if actor, ok := platform.ActorFrom(r.Context()); ok {
		return actor
	}
	return platform.Actor{ID: "anonymous", Roles: []string{}, Scopes: []string{}}
}

// resourceLocation builds the canonical Location header for a created resource.
func resourceLocation(r *http.Request, id uuid.UUID) string {
	return strings.TrimSuffix(r.URL.Path, "/") + "/" + id.String()
}

// isEmptyUpdate reports whether a PATCH body would change nothing.
func isEmptyUpdate(req UpdateSourceRequest) bool {
	return req.Name == nil &&
		req.Slug == nil &&
		req.Description == nil &&
		req.Type == nil &&
		req.Ingestion == nil &&
		req.Environment == nil &&
		req.Status == nil &&
		req.ConnectionSecretRef == nil &&
		!req.ReplaceConnectionConfig &&
		req.ConnectionConfig == nil &&
		req.OwnerID == nil &&
		req.Tags == nil &&
		!req.ReplaceMetadata &&
		req.Metadata == nil
}
