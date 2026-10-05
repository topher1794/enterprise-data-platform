package governance

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// GovernanceService is the business contract the handler depends on.
type GovernanceService interface {
	CreatePolicy(ctx context.Context, req CreatePolicyRequest, actor platform.Actor) (*Policy, error)
	GetPolicy(ctx context.Context, id uuid.UUID) (*Policy, error)
	GetPolicyBySlug(ctx context.Context, slug string) (*Policy, error)
	ListPolicies(ctx context.Context, q ListPoliciesQuery) (platform.PageResult[*Policy], error)
	UpdatePolicy(ctx context.Context, id uuid.UUID, req UpdatePolicyRequest, actor platform.Actor) (*Policy, error)
	DeletePolicy(ctx context.Context, id uuid.UUID, actor platform.Actor) error

	CreateContract(ctx context.Context, req CreateContractRequest, actor platform.Actor) (*Contract, error)
	GetContract(ctx context.Context, id uuid.UUID) (*Contract, error)
	ListContracts(ctx context.Context, q ListContractsQuery) (platform.PageResult[*Contract], error)
	UpdateContract(ctx context.Context, id uuid.UUID, req UpdateContractRequest, actor platform.Actor) (*Contract, error)
	DeleteContract(ctx context.Context, id uuid.UUID, actor platform.Actor) error
	SignContract(ctx context.Context, id uuid.UUID, actor platform.Actor) (*Contract, error)

	// Evaluate resolves the policies applying to a resource and the effective
	// remediation. It is exposed so a client can show a dry-run of the rules
	// before committing data.
	Evaluate(ctx context.Context, q EvaluateQuery) (*PolicyDecision, error)
	EffectiveRetention(ctx context.Context, resourceType ResourceType, resourceID uuid.UUID) (int, error)
}

// Handler exposes the governance domain over HTTP.
type Handler struct {
	svc GovernanceService
}

// NewHandler builds a governance handler.
func NewHandler(svc GovernanceService) *Handler {
	return &Handler{svc: svc}
}

// Mount registers the governance routes on r.
//
//	GET    /policies                        list
//	POST   /policies                        create
//	GET    /policies/{policyID}             read
//	PATCH  /policies/{policyID}             partial update
//	DELETE /policies/{policyID}             archive
//	GET    /policies/by-slug/{slug}         read by slug
//	POST   /policies/evaluate               dry-run the applicable policies
//	GET    /policies/retention              effective retention for a resource
//	GET    /data-contracts                  list
//	POST   /data-contracts                  create
//	GET    /data-contracts/{contractID}     read
//	PATCH  /data-contracts/{contractID}     partial update
//	DELETE /data-contracts/{contractID}     archive
//	POST   /data-contracts/{contractID}/sign  sign off a contract
func (h *Handler) Mount(r chi.Router) {
	r.Get("/policies/by-slug/{slug}", h.GetPolicyBySlug)
	r.Get("/policies/retention", h.Retention)
	r.Post("/policies/evaluate", h.Evaluate)

	r.Route("/policies", func(r chi.Router) {
		r.Get("/", h.ListPolicies)
		r.Post("/", h.CreatePolicy)

		r.Route("/{policyID}", func(r chi.Router) {
			r.Get("/", h.GetPolicy)
			r.Patch("/", h.UpdatePolicy)
			r.Delete("/", h.DeletePolicy)
		})
	})

	r.Route("/data-contracts", func(r chi.Router) {
		r.Get("/", h.ListContracts)
		r.Post("/", h.CreateContract)

		r.Route("/{contractID}", func(r chi.Router) {
			r.Get("/", h.GetContract)
			r.Patch("/", h.UpdateContract)
			r.Delete("/", h.DeleteContract)
			r.Post("/sign", h.SignContract)
		})
	})
}

// ListPolicies handles GET /policies.
func (h *Handler) ListPolicies(w http.ResponseWriter, r *http.Request) {
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

	var enabled *bool
	if raw := strings.TrimSpace(query.Get("enabled")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			platform.Fail(w, r, platform.NewBadRequest("enabled must be true or false"))
			return
		}
		enabled = &parsed
	}

	result, err := h.svc.ListPolicies(r.Context(), ListPoliciesQuery{
		Page:         page,
		Sort:         sort,
		PolicyType:   strings.TrimSpace(query.Get("policy_type")),
		ResourceType: strings.TrimSpace(query.Get("resource_type")),
		ResourceID:   strings.TrimSpace(query.Get("resource_id")),
		Effect:       strings.TrimSpace(query.Get("effect")),
		Enabled:      enabled,
		Search:       strings.TrimSpace(query.Get("search")),
	})
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, result)
}

// CreatePolicy handles POST /policies.
func (h *Handler) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	req, err := platform.FromRequest[CreatePolicyRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	created, err := h.svc.CreatePolicy(r.Context(), req, actorOrAnonymous(r))
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/"+created.ID.String())
	platform.JSON(w, http.StatusCreated, created)
}

// GetPolicy handles GET /policies/{policyID}.
func (h *Handler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "policyID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	found, err := h.svc.GetPolicy(r.Context(), id)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, found)
}

// GetPolicyBySlug handles GET /policies/by-slug/{slug}.
func (h *Handler) GetPolicyBySlug(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	if slug == "" {
		platform.Fail(w, r, platform.NewBadRequest("slug is required"))
		return
	}

	found, err := h.svc.GetPolicyBySlug(r.Context(), slug)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, found)
}

// UpdatePolicy handles PATCH /policies/{policyID}.
func (h *Handler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "policyID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	req, err := platform.FromRequest[UpdatePolicyRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	if req.isEmpty() {
		platform.Fail(w, r, platform.NewBadRequest(
			"the request body must contain at least one field to change"))
		return
	}

	updated, err := h.svc.UpdatePolicy(r.Context(), id, req, actorOrAnonymous(r))
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, updated)
}

// DeletePolicy handles DELETE /policies/{policyID}.
func (h *Handler) DeletePolicy(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "policyID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	if err := h.svc.DeletePolicy(r.Context(), id, actorOrAnonymous(r)); err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.NoContent(w)
}

// Evaluate handles POST /policies/evaluate.
func (h *Handler) Evaluate(w http.ResponseWriter, r *http.Request) {
	req, err := platform.FromRequest[EvaluateQuery](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	decision, err := h.svc.Evaluate(r.Context(), req)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, decision)
}

// Retention handles GET /policies/retention.
func (h *Handler) Retention(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	resourceType := normaliseResourceType(query.Get("resource_type"))
	if resourceType == "" {
		platform.Fail(w, r, platform.NewBadRequest(
			"resource_type is required, one of: %s",
			strings.Join(listResourceTypes(), ", ")))
		return
	}
	if !resourceType.Valid() {
		platform.Fail(w, r, invalidEnum("resource_type", resourceType, listResourceTypes()...))
		return
	}

	id, err := uuid.Parse(strings.TrimSpace(query.Get("resource_id")))
	if err != nil {
		platform.Fail(w, r, platform.NewBadRequest("resource_id must be a valid UUID"))
		return
	}

	days, err := h.svc.EffectiveRetention(r.Context(), resourceType, id)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, map[string]any{
		"resource_type": resourceType,
		"resource_id":   id,
		// Zero means no retention policy applies, which is reported explicitly
		// rather than as a null so a client can distinguish it from an error.
		"retention_days": days,
		"has_policy":     days > 0,
	})
}

// ListContracts handles GET /data-contracts.
func (h *Handler) ListContracts(w http.ResponseWriter, r *http.Request) {
	page, err := platform.ParsePage(r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	sort, err := platform.ParseSort(r, ContractSortableColumns(), ContractDefaultSort())
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	query := r.URL.Query()

	result, err := h.svc.ListContracts(r.Context(), ListContractsQuery{
		Page:      page,
		Sort:      sort,
		DatasetID: strings.TrimSpace(query.Get("dataset_id")),
		Status:    strings.TrimSpace(query.Get("status")),
		Search:    strings.TrimSpace(query.Get("search")),
	})
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	platform.JSON(w, http.StatusOK, result)
}

// CreateContract handles POST /data-contracts.
func (h *Handler) CreateContract(w http.ResponseWriter, r *http.Request) {
	req, err := platform.FromRequest[CreateContractRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	created, err := h.svc.CreateContract(r.Context(), req, actorOrAnonymous(r))
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/"+created.ID.String())
	platform.JSON(w, http.StatusCreated, created)
}

// GetContract handles GET /data-contracts/{contractID}.
func (h *Handler) GetContract(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "contractID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	found, err := h.svc.GetContract(r.Context(), id)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, found)
}

// UpdateContract handles PATCH /data-contracts/{contractID}.
func (h *Handler) UpdateContract(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "contractID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	req, err := platform.FromRequest[UpdateContractRequest](r)
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	if req.isEmpty() {
		platform.Fail(w, r, platform.NewBadRequest(
			"the request body must contain at least one field to change"))
		return
	}

	updated, err := h.svc.UpdateContract(r.Context(), id, req, actorOrAnonymous(r))
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, updated)
}

// DeleteContract handles DELETE /data-contracts/{contractID}.
func (h *Handler) DeleteContract(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "contractID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	if err := h.svc.DeleteContract(r.Context(), id, actorOrAnonymous(r)); err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.NoContent(w)
}

// SignContract handles POST /data-contracts/{contractID}/sign.
func (h *Handler) SignContract(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "contractID")
	if err != nil {
		platform.Fail(w, r, err)
		return
	}

	signed, err := h.svc.SignContract(r.Context(), id, actorOrAnonymous(r))
	if err != nil {
		platform.Fail(w, r, err)
		return
	}
	platform.JSON(w, http.StatusOK, signed)
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
