package audit

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// AuditService is the query contract the handler depends on. Note the absence of
// Record: audit events are written by the domains that perform the action, not
// by anything a client can call.
type AuditService interface {
	List(ctx context.Context, q ListEventsQuery) (platform.PageResult[*Event], error)
	GetByID(ctx context.Context, id uuid.UUID) (*Event, error)
}

// Handler exposes the read-only audit query API.
type Handler struct {
	svc AuditService
}

// NewHandler builds an audit handler.
func NewHandler(svc AuditService) *Handler {
	return &Handler{svc: svc}
}

// Mount registers the audit routes on r.
//
//	GET /audit-events                     list, requires an explicit time bound
//	GET /audit-events/{eventID}           read one event
//
// The write side is deliberately absent: events are appended by the domains
// performing the action.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/audit-events", h.List)
	r.Get("/audit-events/{eventID}", h.Get)
}

// List handles GET /audit-events.
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

	since, err := parseTimeParam(query.Get("since"), "since")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	until, err := parseTimeParam(query.Get("until"), "until")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	// Default to a one-hour window so the common case cannot scan the whole
	// table, while still being useful for an operator investigating "what just
	// happened".
	if since == nil && until == nil {
		defaultWindow := time.Now().UTC().Add(-time.Hour)
		since = &defaultWindow
	}

	// entity_id is validated here rather than in the repository so a typo
	// returns 400 rather than an empty result set.
	if raw := strings.TrimSpace(query.Get("entity_id")); raw != "" {
		if _, err := uuid.Parse(raw); err != nil {
			platform.Fail(w, r, platform.NewBadRequest("entity_id must be a valid UUID"))
			return
		}
	}

	result, err := h.svc.List(r.Context(), ListEventsQuery{
		Page:       page,
		Sort:       sort,
		ActorID:    strings.TrimSpace(query.Get("actor_id")),
		TenantID:   strings.TrimSpace(query.Get("tenant_id")),
		Action:     strings.TrimSpace(query.Get("action")),
		EntityType: strings.TrimSpace(query.Get("entity_type")),
		EntityID:   strings.TrimSpace(query.Get("entity_id")),
		Outcome:    strings.TrimSpace(query.Get("outcome")),
		Since:      since,
		Until:      until,
	})
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, result)
}

// Get handles GET /audit-events/{eventID}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "eventID"))
	if err != nil {
		platform.Fail(w, r, platform.NewBadRequest("eventID must be a valid UUID"))
		return
	}

	found, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, found)
}

// parseTimeParam reads an RFC 3339 timestamp from the query string. An empty
// value yields a nil time and no error.
func parseTimeParam(raw, name string) (*time.Time, error) {
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
