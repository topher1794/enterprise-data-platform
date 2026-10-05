package pipeline

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// PipelineService is the business contract the handler depends on.
type PipelineService interface {
	Create(ctx context.Context, req CreatePipelineRequest, actor platform.Actor) (*Pipeline, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Pipeline, error)
	GetBySlug(ctx context.Context, slug string) (*Pipeline, error)
	List(ctx context.Context, q ListPipelinesQuery) (platform.PageResult[*Pipeline], error)
	Update(ctx context.Context, id uuid.UUID, req UpdatePipelineRequest, actor platform.Actor) (*Pipeline, error)
	Delete(ctx context.Context, id uuid.UUID, actor platform.Actor) error
	ExecutionOrder(ctx context.Context, id uuid.UUID) ([]Task, []string, error)
}

// Handler exposes the pipeline domain over HTTP.
type Handler struct {
	svc PipelineService
}

// NewHandler builds a pipeline handler.
func NewHandler(svc PipelineService) *Handler {
	return &Handler{svc: svc}
}

// Mount registers the pipeline routes on r.
//
//	GET    /pipelines                       list
//	POST   /pipelines                       create, with its DAG
//	GET    /pipelines/{pipelineID}          read, including tasks and edges
//	PATCH  /pipelines/{pipelineID}          partial update
//	DELETE /pipelines/{pipelineID}          archive
//	GET    /pipelines/by-slug/{slug}        read by slug
//	GET    /pipelines/{pipelineID}/order    dependency-ordered task list
func (h *Handler) Mount(r chi.Router) {
	r.Get("/pipelines/by-slug/{slug}", h.GetBySlug)

	r.Route("/pipelines", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/", h.Create)

		r.Route("/{pipelineID}", func(r chi.Router) {
			r.Get("/", h.Get)
			r.Patch("/", h.Update)
			r.Delete("/", h.Delete)
			r.Get("/order", h.Order)
		})
	})
}

// List handles GET /pipelines.
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

	// A bare "deployed" flag is accepted as a convenience for
	// `?deployed=true`; an unparseable value is an error rather than a silent
	// default, which would return the wrong result set.
	var deployed *bool
	if raw := query.Get("deployed"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			platform.Fail(w, r, platform.NewBadRequest("deployed must be true or false"))
			return
		}
		deployed = &v
	}

	result, err := h.svc.List(r.Context(), ListPipelinesQuery{
		Page:     page,
		Sort:     sort,
		Status:   strings.ToLower(query.Get("status")),
		OwnerID:  strings.TrimSpace(query.Get("owner_id")),
		DAGID:    strings.TrimSpace(query.Get("dag_id")),
		Search:   strings.TrimSpace(query.Get("q")),
		Tag:      query["tag"],
		Deployed: deployed,
	})
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, result)
}

// Create handles POST /pipelines.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	req, err := platform.FromRequest[CreatePipelineRequest](r)
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

// Get handles GET /pipelines/{pipelineID}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "pipelineID")
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

// GetBySlug handles GET /pipelines/by-slug/{slug}.
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

// Update handles PATCH /pipelines/{pipelineID}.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "pipelineID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	req, err := platform.FromRequest[UpdatePipelineRequest](r)
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

// Delete handles DELETE /pipelines/{pipelineID}.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "pipelineID")
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

// ExecutionOrderResponse is the payload for the ordering endpoint.
type ExecutionOrderResponse struct {
	PipelineID uuid.UUID `json:"pipeline_id"`
	// Order is the task keys in dependency order.
	Order []string `json:"order"`
	// Tasks are the full task definitions, in the same order as Order.
	Tasks []Task `json:"tasks"`
}

// Order handles GET /pipelines/{pipelineID}/order.
func (h *Handler) Order(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "pipelineID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	tasks, order, err := h.svc.ExecutionOrder(r.Context(), id)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, ExecutionOrderResponse{
		PipelineID: id,
		Order:      order,
		Tasks:      tasks,
	})
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

// isEmptyUpdate reports whether a PATCH body would change nothing.
func isEmptyUpdate(req UpdatePipelineRequest) bool {
	return req.Name == nil &&
		req.Slug == nil &&
		req.Description == nil &&
		req.DAGID == nil &&
		!req.ClearDAGID &&
		req.Schedule == nil &&
		req.Timezone == nil &&
		req.Status == nil &&
		req.MaxActiveRuns == nil &&
		req.Catchup == nil &&
		req.DefaultDatasetID == nil &&
		!req.ClearDefaultDataset &&
		req.OwnerID == nil &&
		req.Tags == nil &&
		!req.ReplaceGraph &&
		req.Tasks == nil &&
		req.Dependencies == nil &&
		req.Metadata == nil &&
		!req.ReplaceMetadata
}
