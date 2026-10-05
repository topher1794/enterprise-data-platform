package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// slugPattern mirrors the `pipelines_slug_format` CHECK constraint.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// maxSlugLength bounds generated slugs.
const maxSlugLength = 100

// Auditor records state changes. The audit domain supplies the implementation.
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

// AuditEntityPipeline is the audit entity type for pipelines.
const AuditEntityPipeline = "pipeline"

// Service holds the business rules for pipelines. Its central responsibility is
// guaranteeing that only valid DAGs are ever persisted.
type Service struct {
	repo     Repository
	auditor  Auditor
	validate *validator.Validate
	log      *slog.Logger
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

// NewService builds a pipeline service.
func NewService(repo Repository, opts ...ServiceOption) (*Service, error) {
	if repo == nil {
		return nil, errors.New("pipeline service: repository is required")
	}

	s := &Service{
		repo:     repo,
		validate: validator.New(validator.WithRequiredStructEnabled()),
		log:      slog.Default(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Create validates the graph and persists a new pipeline.
func (s *Service) Create(ctx context.Context, req CreatePipelineRequest, actor platform.Actor) (*Pipeline, error) {
	if err := s.validate.Struct(req); err != nil {
		return nil, validationError(err)
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	req.OwnerID = strings.TrimSpace(req.OwnerID)

	status := Status(strings.ToLower(string(req.Status)))
	if !status.Valid() {
		status = StatusDraft
	}

	maxActiveRuns := req.MaxActiveRuns
	if maxActiveRuns == 0 {
		maxActiveRuns = 1
	}

	timezone := strings.TrimSpace(req.Timezone)
	if timezone == "" {
		timezone = "UTC"
	}

	tasks := normaliseTasks(req.Tasks)
	deps := normaliseDependencies(req.Dependencies)

	slug, err := s.resolveSlug(ctx, req.Slug, req.Name)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	record := &Pipeline{
		Name:             req.Name,
		Slug:             slug,
		Description:      req.Description,
		DAGID:            strings.TrimSpace(req.DAGID),
		Status:           status,
		Schedule:         strings.TrimSpace(req.Schedule),
		Timezone:         timezone,
		MaxActiveRuns:    maxActiveRuns,
		Catchup:          req.Catchup,
		DefaultDatasetID: req.DefaultDatasetID,
		OwnerID:          req.OwnerID,
		Tags:             normaliseTags(req.Tags),
		TaskCount:        len(tasks),
		GraphHash:        GraphHash(tasks, deps),
		Tasks:            tasks,
		Dependencies:     deps,
		Metadata:         req.Metadata,
		CreatedBy:        actor.ID,
		UpdatedBy:        actor.ID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	// Validate the whole definition, including the DAG identifier against the
	// ones already deployed, before anything is written.
	if err := s.validateSpec(ctx, record, nil); err != nil {
		return nil, err
	}

	if err := s.repo.Create(ctx, record, tasks, deps); err != nil {
		return nil, mapRepoError(err)
	}

	s.log.InfoContext(ctx, "pipeline created",
		"pipeline_id", record.ID, "slug", record.Slug,
		"tasks", len(tasks), "graph_hash", record.GraphHash, "actor", actor.ID)

	s.record(ctx, "pipeline.created", record, map[string]any{
		"task_count":      len(tasks),
		"edge_count":      len(deps),
		"graph_hash":      record.GraphHash,
		"execution_order": executionOrder(tasks, deps),
	})

	return record, nil
}

// GetByID returns a pipeline with its graph populated.
func (s *Service) GetByID(ctx context.Context, id uuid.UUID) (*Pipeline, error) {
	record, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, mapRepoError(err)
	}
	if err := s.attachGraph(ctx, record); err != nil {
		return nil, err
	}
	return record, nil
}

// GetBySlug returns a pipeline addressed by slug, with its graph.
func (s *Service) GetBySlug(ctx context.Context, slug string) (*Pipeline, error) {
	record, err := s.repo.GetBySlug(ctx, strings.TrimSpace(slug))
	if err != nil {
		return nil, mapRepoError(err)
	}
	if err := s.attachGraph(ctx, record); err != nil {
		return nil, err
	}
	return record, nil
}

// List returns a filtered, paginated page of pipelines without their graphs.
func (s *Service) List(ctx context.Context, q ListPipelinesQuery) (platform.PageResult[*Pipeline], error) {
	page, err := s.repo.List(ctx, q)
	if err != nil {
		return platform.PageResult[*Pipeline]{}, mapRepoError(err)
	}
	return page, nil
}

// Update applies a partial change set. When a replacement graph is supplied it
// is validated before any write, so a rejected graph leaves the pipeline
// untouched.
func (s *Service) Update(ctx context.Context, id uuid.UUID, req UpdatePipelineRequest, actor platform.Actor) (*Pipeline, error) {
	if err := s.validate.Struct(req); err != nil {
		return nil, validationError(err)
	}

	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, mapRepoError(err)
	}
	if err := s.attachGraph(ctx, existing); err != nil {
		return nil, err
	}
	before := *existing

	if existing.Status.Terminal() {
		return nil, platform.NewConflict(
			"pipeline %s is archived and can no longer be modified", existing.Slug)
	}

	if req.Name != nil {
		existing.Name = strings.TrimSpace(*req.Name)
	}
	if req.Slug != nil {
		slug, err := normaliseSlug(*req.Slug)
		if err != nil {
			return nil, err
		}
		existing.Slug = slug
	}
	if req.Description != nil {
		existing.Description = strings.TrimSpace(*req.Description)
	}
	if req.ClearDAGID {
		existing.DAGID = ""
	} else if req.DAGID != nil {
		existing.DAGID = strings.TrimSpace(*req.DAGID)
	}
	if req.Schedule != nil {
		existing.Schedule = strings.TrimSpace(*req.Schedule)
	}
	if req.Timezone != nil {
		tz := strings.TrimSpace(*req.Timezone)
		if tz == "" {
			tz = "UTC"
		}
		existing.Timezone = tz
	}
	if req.Status != nil {
		st := Status(strings.ToLower(string(*req.Status)))
		if !st.Valid() {
			return nil, invalidEnum("status", *req.Status,
				"must be one of: draft, active, paused, archived")
		}
		// A pipeline can only be activated once it is deployable: it needs a
		// DAG identifier and at least one enabled task.
		if st == StatusActive {
			if existing.DAGID == "" {
				return nil, platform.NewValidation(
					"pipeline cannot become active before it has a dag_id").
					WithFields(fieldError("status", "readiness", "a dag_id is required"))
			}
			if !hasEnabledTask(existing.Tasks) {
				return nil, platform.NewValidation(
					"pipeline cannot become active with no enabled tasks").
					WithFields(fieldError("status", "readiness",
						"at least one task must be enabled"))
			}
		}
		existing.Status = st
	}
	if req.MaxActiveRuns != nil {
		existing.MaxActiveRuns = *req.MaxActiveRuns
	}
	if req.Catchup != nil {
		existing.Catchup = *req.Catchup
	}
	if req.ClearDefaultDataset {
		existing.DefaultDatasetID = nil
	} else if req.DefaultDatasetID != nil {
		existing.DefaultDatasetID = req.DefaultDatasetID
	}
	if req.OwnerID != nil {
		existing.OwnerID = strings.TrimSpace(*req.OwnerID)
	}
	if req.Tags != nil {
		existing.Tags = normaliseTags(*req.Tags)
	}
	if req.ReplaceMetadata {
		existing.Metadata = req.Metadata
	} else if req.Metadata != nil {
		existing.Metadata = mergeMaps(existing.Metadata, req.Metadata)
	}

	// A replacement graph is applied to the in-memory record first so the whole
	// post-change definition can be validated as one unit.
	graphReplaced := false
	if req.ReplaceGraph || req.Tasks != nil || req.Dependencies != nil {
		tasks := normaliseTasks(req.Tasks)
		deps := normaliseDependencies(req.Dependencies)

		existing.Tasks = tasks
		existing.Dependencies = deps
		existing.TaskCount = len(tasks)
		existing.GraphHash = GraphHash(tasks, deps)
		graphReplaced = true
	}

	existing.UpdatedBy = actor.ID

	if err := s.validateSpec(ctx, existing, &before); err != nil {
		return nil, err
	}

	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, mapRepoError(err)
	}

	if graphReplaced {
		if err := s.repo.ReplaceGraph(ctx, id, existing.Tasks, existing.Dependencies, existing.GraphHash); err != nil {
			return nil, mapRepoError(err)
		}
		s.log.InfoContext(ctx, "pipeline graph replaced",
			"pipeline_id", id, "tasks", len(existing.Tasks),
			"edges", len(existing.Dependencies), "graph_hash", existing.GraphHash,
			"actor", actor.ID)
	}

	s.record(ctx, "pipeline.updated", existing, map[string]any{
		"changed":        diffFields(before, *existing),
		"graph_replaced": graphReplaced,
		"graph_hash":     existing.GraphHash,
	})

	return existing, nil
}

// Delete archives a pipeline.
func (s *Service) Delete(ctx context.Context, id uuid.UUID, actor platform.Actor) error {
	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return mapRepoError(err)
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		return mapRepoError(err)
	}

	s.log.InfoContext(ctx, "pipeline deleted",
		"pipeline_id", id, "slug", existing.Slug, "actor", actor.ID)

	s.record(ctx, "pipeline.deleted", existing, nil)
	return nil
}

// ExecutionOrder returns the tasks of a pipeline in dependency order, along
// with the order itself. It is exposed so callers can present a run sequence
// without re-implementing the topological sort.
func (s *Service) ExecutionOrder(ctx context.Context, id uuid.UUID) ([]Task, []string, error) {
	record, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	keys := make([]string, 0, len(record.Tasks))
	for _, t := range record.Tasks {
		keys = append(keys, t.TaskKey)
	}

	order, complete := TopologicalOrder(keys, record.Dependencies)
	if !complete {
		return nil, nil, platform.NewInternal(
			"pipeline %s has a stored dependency graph that contains a cycle", record.Slug)
	}

	byKey := make(map[string]Task, len(record.Tasks))
	for _, t := range record.Tasks {
		byKey[t.TaskKey] = t
	}

	ordered := make([]Task, 0, len(order))
	for _, key := range order {
		if task, ok := byKey[key]; ok {
			ordered = append(ordered, task)
		}
	}
	return ordered, order, nil
}

// validateSpec runs the structural and topological checks and converts any
// issues into a single 422. before is the pre-change record for an update, used
// to exempt the pipeline from colliding with its own DAG identifier.
func (s *Service) validateSpec(ctx context.Context, p *Pipeline, before *Pipeline) error {
	known, err := s.repo.AllDAGIDs(ctx)
	if err != nil {
		return platform.AsError(err)
	}
	// A pipeline's own DAG id is not a collision with itself.
	if before != nil && before.DAGID != "" {
		delete(known, before.DAGID)
	}

	validator := NewValidator(known)
	issues := validator.Validate(Spec(p, nil, nil))
	if len(issues) == 0 {
		return nil
	}

	out := platform.NewValidation("the pipeline definition is not valid")
	for _, issue := range issues {
		out = out.WithFields(platform.FieldError{
			Field:   issue.Field,
			Rule:    issue.Code,
			Message: issue.Message,
		})
	}

	s.log.DebugContext(ctx, "pipeline definition rejected",
		"pipeline_id", p.ID, "slug", p.Slug, "issue_count", len(issues))
	return out
}

// attachGraph loads tasks and edges onto a pipeline.
func (s *Service) attachGraph(ctx context.Context, p *Pipeline) error {
	tasks, err := s.repo.Tasks(ctx, p.ID)
	if err != nil {
		return platform.AsError(err)
	}
	deps, err := s.repo.Dependencies(ctx, p.ID)
	if err != nil {
		return platform.AsError(err)
	}
	p.Tasks = tasks
	p.Dependencies = deps
	return nil
}

// resolveSlug derives a slug when none was supplied and ensures uniqueness.
func (s *Service) resolveSlug(ctx context.Context, requested, name string) (string, error) {
	candidate := requested
	if strings.TrimSpace(candidate) == "" {
		candidate = slugify(name)
	}

	slug, err := normaliseSlug(candidate)
	if err != nil {
		return "", err
	}

	base := slug
	for attempt := 2; attempt <= 50; attempt++ {
		_, err := s.repo.GetBySlug(ctx, slug)
		if errors.Is(err, ErrNotFound) {
			return slug, nil
		}
		if err != nil {
			return "", mapRepoError(err)
		}

		suffix := fmt.Sprintf("-%d", attempt)
		trimmed := base
		if len(trimmed)+len(suffix) > maxSlugLength {
			trimmed = strings.TrimRight(trimmed[:maxSlugLength-len(suffix)], "-")
		}
		slug = trimmed + suffix
	}

	return "", platform.NewConflict(
		"could not derive an unused slug from %q; supply one explicitly", name)
}

// normaliseTasks applies defaults via TaskInput.toTask and trims identifiers.
// The deeper structural checks belong to Validator.
func normaliseTasks(inputs []TaskInput) []Task {
	out := make([]Task, 0, len(inputs))
	for _, in := range inputs {
		out = append(out, in.toTask())
	}
	return out
}

// normaliseDependencies trims and de-duplicates edges.
func normaliseDependencies(inputs []Dependency) []Dependency {
	if len(inputs) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(inputs))
	out := make([]Dependency, 0, len(inputs))
	for _, in := range inputs {
		d := Dependency{
			UpstreamKey:   strings.TrimSpace(in.UpstreamKey),
			DownstreamKey: strings.TrimSpace(in.DownstreamKey),
		}
		key := d.UpstreamKey + "\x00" + d.DownstreamKey
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, d)
	}
	return out
}

// hasEnabledTask reports whether at least one task is enabled.
func hasEnabledTask(tasks []Task) bool {
	for _, t := range tasks {
		if t.Enabled {
			return true
		}
	}
	return false
}

// executionOrder renders the topological order for the audit trail. A cyclic
// graph yields an empty string rather than a partial order, because a partial
// order in an audit record is misleading.
func executionOrder(tasks []Task, deps []Dependency) string {
	keys := make([]string, 0, len(tasks))
	for _, t := range tasks {
		keys = append(keys, t.TaskKey)
	}
	order, complete := TopologicalOrder(keys, deps)
	if !complete {
		return ""
	}
	return strings.Join(order, " -> ")
}

// humanise turns a task_key such as "load_orders" into "Load Orders".
func humanise(key string) string {
	if key == "" {
		return ""
	}
	words := strings.FieldsFunc(key, func(r rune) bool { return r == '_' || r == '-' })
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// normaliseSlug validates and canonicalises a slug.
func normaliseSlug(slug string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(slug))
	if trimmed == "" {
		return "", platform.NewValidation("slug must not be empty").
			WithFields(fieldError("slug", "required", "a slug or name is required"))
	}
	if len(trimmed) > maxSlugLength {
		return "", platform.NewValidation("slug must not exceed %d characters", maxSlugLength).
			WithFields(fieldError("slug", "max", "slug is too long"))
	}
	if !slugPattern.MatchString(trimmed) {
		return "", platform.NewValidation(
			"slug %q must be lowercase alphanumeric words separated by single hyphens", slug).
			WithFields(fieldError("slug", "slug", "slug must match ^[a-z0-9]+(-[a-z0-9]+)*$"))
	}
	return trimmed, nil
}

// slugify derives a slug from a display name.
func slugify(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))

	var b strings.Builder
	b.Grow(len(lower))

	prevHyphen := false
	for _, r := range lower {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}

	slug := strings.Trim(b.String(), "-")
	if len(slug) > maxSlugLength {
		slug = strings.Trim(slug[:maxSlugLength], "-")
	}
	return slug
}

// normaliseTags lowercases, trims and de-duplicates tags.
func normaliseTags(tags []string) []string {
	if len(tags) == 0 {
		return []string{}
	}

	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		trimmed := strings.ToLower(strings.TrimSpace(tag))
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// mergeMaps overlays updates onto base without mutating either.
func mergeMaps(base, updates map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(updates))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range updates {
		out[k] = v
	}
	return out
}

// record writes an audit entry, logging rather than failing the request.
func (s *Service) record(ctx context.Context, action string, p *Pipeline, extra map[string]any) {
	if s.auditor == nil {
		return
	}

	changes := auditSnapshot(*p)
	for k, v := range extra {
		changes[k] = v
	}

	if err := s.auditor.Record(ctx, AuditEntry{
		Action:     action,
		EntityType: AuditEntityPipeline,
		EntityID:   p.ID,
		EntityName: p.Name,
		Changes:    changes,
	}); err != nil {
		s.log.ErrorContext(ctx, "failed to record audit entry",
			"action", action, "pipeline_id", p.ID, "error", err)
	}
}

// mapRepoError translates persistence errors into the platform taxonomy.
func mapRepoError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, ErrNotFound):
		return platform.NewNotFound("pipeline", extractID(err)).WithCause(err)
	case errors.Is(err, ErrSlugTaken):
		return platform.NewConflict("a pipeline with this slug already exists").WithCause(err)
	case errors.Is(err, ErrDAGIDTaken):
		return platform.NewConflict("that dag_id is already deployed by another pipeline").WithCause(err)
	case errors.Is(err, ErrArchiveConflict):
		return platform.NewConflict("the pipeline is archived").WithCause(err)
	default:
		return platform.AsError(err)
	}
}
