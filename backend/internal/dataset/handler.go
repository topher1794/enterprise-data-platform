package dataset

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// DatasetService is the business contract the handler depends on.
type DatasetService interface {
	Create(ctx context.Context, req CreateDatasetRequest, actor platform.Actor) (*Dataset, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Dataset, error)
	GetBySlug(ctx context.Context, slug string) (*Dataset, error)
	List(ctx context.Context, q ListDatasetsQuery) (platform.PageResult[*Dataset], error)
	Update(ctx context.Context, id uuid.UUID, req UpdateDatasetRequest, actor platform.Actor) (*Dataset, error)
	Delete(ctx context.Context, id uuid.UUID, actor platform.Actor) error
}

// Handler exposes the dataset domain over HTTP.
type Handler struct {
	svc DatasetService
}

// NewHandler builds a dataset handler.
func NewHandler(svc DatasetService) *Handler {
	return &Handler{svc: svc}
}

// Mount registers the dataset routes on r.
//
//	GET    /datasets                  list
//	POST   /datasets                  create
//	GET    /datasets/{datasetID}      read, including the schema
//	PATCH  /datasets/{datasetID}      partial update
//	DELETE /datasets/{datasetID}      archive
//	GET    /datasets/by-slug/{slug}   read by slug
func (h *Handler) Mount(r chi.Router) {
	r.Get("/datasets/by-slug/{slug}", h.GetBySlug)

	r.Route("/datasets", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/", h.Create)

		r.Route("/{datasetID}", func(r chi.Router) {
			r.Get("/", h.Get)
			r.Patch("/", h.Update)
			r.Delete("/", h.Delete)
		})
	})
}

// List handles GET /datasets.
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
	result, err := h.svc.List(r.Context(), ListDatasetsQuery{
		Page:           page,
		Sort:           sort,
		Kind:           strings.ToLower(query.Get("kind")),
		Classification: strings.ToLower(query.Get("classification")),
		Status:         strings.ToLower(query.Get("status")),
		SourceID:       strings.TrimSpace(query.Get("source_id")),
		Domain:         strings.TrimSpace(query.Get("domain")),
		OwnerID:        strings.TrimSpace(query.Get("owner_id")),
		StewardID:      strings.TrimSpace(query.Get("steward_id")),
		Search:         strings.TrimSpace(query.Get("q")),
		Tag:            query["tag"],
		IncludeColumns: parseBool(query.Get("include_columns"), false),
	})
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, result)
}

// Create handles POST /datasets.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	req, err := platform.FromRequest[CreateDatasetRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	created, err := h.svc.Create(r.Context(), req, actorOrAnonymous(r))
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/"+created.ID.String())
	platform.JSON(w, http.StatusCreated, created)
}

// Get handles GET /datasets/{datasetID}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "datasetID")
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

// GetBySlug handles GET /datasets/by-slug/{slug}.
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

// Update handles PATCH /datasets/{datasetID}.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "datasetID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	req, err := platform.FromRequest[UpdateDatasetRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
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

// Delete handles DELETE /datasets/{datasetID}.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "datasetID")
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

// pathUUID extracts and validates a UUID path parameter.
func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, platform.NewBadRequest("%s must be a valid UUID", name).
			WithFields(fieldError(name, "uuid", "must be a valid UUID"))
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

// parseBool reads an optional boolean query parameter.
func parseBool(raw string, fallback bool) bool {
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return v
}

// isEmptyUpdate reports whether a PATCH body would change nothing.
func isEmptyUpdate(req UpdateDatasetRequest) bool {
	return req.Name == nil &&
		req.Slug == nil &&
		req.Description == nil &&
		req.Kind == nil &&
		req.Classification == nil &&
		req.Status == nil &&
		req.PhysicalLocation == nil &&
		req.ClearSourceID == false &&
		req.SourceID == nil &&
		req.Domain == nil &&
		req.RetentionDays == nil &&
		req.OwnerID == nil &&
		req.StewardID == nil &&
		req.Tags == nil &&
		req.ReplaceColumns == nil &&
		!req.BumpSchemaVersion &&
		req.Metadata == nil &&
		!req.ReplaceMetadata
}
