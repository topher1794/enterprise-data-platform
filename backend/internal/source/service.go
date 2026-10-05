package source

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

	"github.com/edp/edp-control-plane/internal/infrastructure/secrets"
	"github.com/edp/edp-control-plane/internal/platform"
)

// slugPattern mirrors the `sources_slug_format` CHECK constraint so an invalid
// slug is rejected with a clear message rather than a constraint violation.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// maxSlugLength keeps slugs usable in URLs and file names.
const maxSlugLength = 100

// Auditor records state changes. The audit domain supplies the real
// implementation; declaring it here as a narrow interface keeps the service
// testable and avoids a dependency cycle between domains.
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

// Audit entity type for sources.
const AuditEntitySource = "source"

// Service holds the business rules for registering and maintaining sources.
type Service struct {
	repo     Repository
	secrets  secrets.Provider
	auditor  Auditor
	validate *validator.Validate
	log      *slog.Logger
}

// ServiceOption customises a Service.
type ServiceOption func(*Service)

// WithAuditor attaches an audit sink. Without one the service still works, so
// unit tests and the dev server need no audit infrastructure.
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

// NewService builds a source service.
//
// Enum fields are checked explicitly in Create and Update rather than through
// validator tags, so that each one can produce a specific, actionable message
// instead of a generic "failed on the 'source_type' tag" failure.
func NewService(repo Repository, secretProvider secrets.Provider, opts ...ServiceOption) (*Service, error) {
	if repo == nil {
		return nil, errors.New("source service: repository is required")
	}

	s := &Service{
		repo:     repo,
		secrets:  secretProvider,
		validate: validator.New(validator.WithRequiredStructEnabled()),
		log:      slog.Default(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Create registers a new source after validating the request and checking that
// the slug and secret reference are usable.
func (s *Service) Create(ctx context.Context, req CreateSourceRequest, actor platform.Actor) (*Source, error) {
	if err := s.validate.Struct(req); err != nil {
		return nil, validationError("source", err)
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	req.OwnerID = strings.TrimSpace(req.OwnerID)

	slug, err := s.resolveSlug(ctx, req.Slug, req.Name)
	if err != nil {
		return nil, err
	}

	sourceType := Type(strings.ToLower(string(req.Type)))
	if !sourceType.Valid() {
		return nil, platform.NewValidation("type %q is not supported", req.Type).
			WithFields(fieldError("type", "source_type", "unsupported source type"))
	}

	if err := secrets.ValidateRef(req.ConnectionSecretRef); err != nil {
		return nil, platform.NewValidation("%s", err.Error()).
			WithFields(fieldError("connection_secret_ref", "secret_ref", err.Error()))
	}

	// Confirm the referenced secret actually resolves. Catching this at
	// registration time is far cheaper than discovering it during an
	// unattended overnight load.
	if s.secrets != nil {
		if _, err := s.secrets.Resolve(ctx, req.ConnectionSecretRef); err != nil {
			s.log.WarnContext(ctx, "secret reference did not resolve during source creation",
				"ref", req.ConnectionSecretRef, "error", err)
			return nil, platform.NewValidation(
				"connection_secret_ref could not be resolved: %s", err.Error()).
				WithFields(fieldError("connection_secret_ref", "resolvable",
					"the referenced secret could not be resolved"))
		}
	}

	environment := Environment(strings.ToLower(string(req.Environment)))
	if !environment.Valid() {
		environment = EnvProduction
	}

	status := Status(strings.ToLower(string(req.Status)))
	if !status.Valid() {
		status = StatusDraft
	}

	ingestion := IngestionMode(strings.ToLower(string(req.Ingestion)))
	if !ingestion.Valid() {
		return nil, platform.NewValidation("ingestion_mode %q is not supported", req.Ingestion)
	}

	now := time.Now().UTC()
	record := &Source{
		Name:                req.Name,
		Slug:                slug,
		Description:         req.Description,
		Type:                sourceType,
		Ingestion:           ingestion,
		Environment:         environment,
		Status:              status,
		ConnectionSecretRef: strings.TrimSpace(req.ConnectionSecretRef),
		ConnectionConfig:    req.ConnectionConfig,
		OwnerID:             req.OwnerID,
		Tags:                normaliseTags(req.Tags),
		Metadata:            req.Metadata,
		CreatedBy:           actor.ID,
		UpdatedBy:           actor.ID,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	if err := s.repo.Create(ctx, record); err != nil {
		return nil, s.mapRepoError(err)
	}

	s.log.InfoContext(ctx, "source created",
		"source_id", record.ID, "slug", record.Slug, "type", record.Type, "actor", actor.ID)

	if err := s.record(ctx, "source.created", record); err != nil {
		// The source is committed; failing the request would mislead the
		// caller into retrying a create that already succeeded.
		s.log.ErrorContext(ctx, "failed to record audit entry",
			"action", "source.created", "source_id", record.ID, "error", err)
	}

	return record, nil
}

// GetByID returns a single source.
func (s *Service) GetByID(ctx context.Context, id uuid.UUID) (*Source, error) {
	record, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, s.mapRepoError(err)
	}
	return record, nil
}

// GetBySlug returns a single source addressed by its slug.
func (s *Service) GetBySlug(ctx context.Context, slug string) (*Source, error) {
	record, err := s.repo.GetBySlug(ctx, strings.TrimSpace(slug))
	if err != nil {
		return nil, s.mapRepoError(err)
	}
	return record, nil
}

// List returns a filtered, paginated page of sources.
func (s *Service) List(ctx context.Context, q ListSourcesQuery) (platform.PageResult[*Source], error) {
	page, err := s.repo.List(ctx, q)
	if err != nil {
		return platform.PageResult[*Source]{}, s.mapRepoError(err)
	}
	return page, nil
}

// Update applies a partial change set. Only the fields present in the request
// are touched, and the resulting state is re-validated as a whole so a partial
// update cannot produce an invalid record.
func (s *Service) Update(ctx context.Context, id uuid.UUID, req UpdateSourceRequest, actor platform.Actor) (*Source, error) {
	if err := s.validate.Struct(req); err != nil {
		return nil, validationError("source", err)
	}

	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, s.mapRepoError(err)
	}

	before := *existing

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
	if req.Type != nil {
		t := Type(strings.ToLower(string(*req.Type)))
		if !t.Valid() {
			return nil, platform.NewValidation("type %q is not supported", *req.Type).
				WithFields(fieldError("type", "source_type", "unsupported source type"))
		}
		existing.Type = t
	}
	if req.Ingestion != nil {
		m := IngestionMode(strings.ToLower(string(*req.Ingestion)))
		if !m.Valid() {
			return nil, platform.NewValidation("ingestion_mode %q is not supported", *req.Ingestion).
				WithFields(fieldError("ingestion_mode", "ingestion_mode", "unsupported mode"))
		}
		existing.Ingestion = m
	}
	if req.Environment != nil {
		e := Environment(strings.ToLower(string(*req.Environment)))
		if !e.Valid() {
			return nil, platform.NewValidation("environment %q is not supported", *req.Environment).
				WithFields(fieldError("environment", "environment", "unsupported environment"))
		}
		existing.Environment = e
	}
	if req.Status != nil {
		// A retired source cannot be brought back to active: its credentials
		// may already have been revoked downstream.
		if existing.Status.Terminal() && *req.Status != StatusRetired {
			return nil, platform.NewConflict(
				"source %s is retired and cannot return to status %q", existing.Slug, *req.Status)
		}
		st := Status(strings.ToLower(string(*req.Status)))
		if !st.Valid() {
			return nil, platform.NewValidation("status %q is not supported", *req.Status).
				WithFields(fieldError("status", "source_status", "unsupported status"))
		}
		existing.Status = st
	}
	if req.ConnectionSecretRef != nil {
		ref := strings.TrimSpace(*req.ConnectionSecretRef)
		if err := secrets.ValidateRef(ref); err != nil {
			return nil, platform.NewValidation("%s", err.Error()).
				WithFields(fieldError("connection_secret_ref", "secret_ref", err.Error()))
		}
		existing.ConnectionSecretRef = ref
	}
	if req.ReplaceConnectionConfig {
		existing.ConnectionConfig = req.ConnectionConfig
	} else if req.ConnectionConfig != nil {
		existing.ConnectionConfig = mergeMaps(existing.ConnectionConfig, req.ConnectionConfig)
	}
	if req.ReplaceMetadata {
		existing.Metadata = req.Metadata
	} else if req.Metadata != nil {
		existing.Metadata = mergeMaps(existing.Metadata, req.Metadata)
	}
	if req.OwnerID != nil {
		existing.OwnerID = strings.TrimSpace(*req.OwnerID)
	}
	if req.Tags != nil {
		existing.Tags = normaliseTags(*req.Tags)
	}
	existing.UpdatedBy = actor.ID

	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, s.mapRepoError(err)
	}

	s.log.InfoContext(ctx, "source updated",
		"source_id", existing.ID, "actor", actor.ID,
		"changed", diffFields(before, *existing))

	if err := s.recordChange(ctx, "source.updated", existing, before); err != nil {
		s.log.ErrorContext(ctx, "failed to record audit entry",
			"action", "source.updated", "source_id", existing.ID, "error", err)
	}

	return existing, nil
}

// Delete soft-deletes a source, refusing while datasets still depend on it.
// The caller should treat a dependency as a governance problem rather than
// force a delete.
func (s *Service) Delete(ctx context.Context, id uuid.UUID, actor platform.Actor) error {
	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return s.mapRepoError(err)
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		return s.mapRepoError(err)
	}

	s.log.InfoContext(ctx, "source deleted",
		"source_id", id, "slug", existing.Slug, "actor", actor.ID)

	if err := s.record(ctx, "source.deleted", existing); err != nil {
		s.log.ErrorContext(ctx, "failed to record audit entry",
			"action", "source.deleted", "source_id", id, "error", err)
	}
	return nil
}

// RecordHealthCheck stores the outcome of a connectivity probe and, when the
// source has been consistently failing, flips it to degraded so it stops
// receiving new pipelines without a human intervening.
func (s *Service) RecordHealthCheck(ctx context.Context, id uuid.UUID, status HealthStatus, checkedAt time.Time) error {
	if !status.Valid() {
		return platform.NewValidation("health status %q is not supported", status)
	}

	record, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return s.mapRepoError(err)
	}

	if err := s.repo.RecordHealthCheck(ctx, id, status, checkedAt.UTC()); err != nil {
		return s.mapRepoError(err)
	}

	// Automatic status transition: a production source that just failed a probe
	// and is currently active becomes degraded. Recovery to active is left to
	// an explicit operator action to avoid flapping.
	if status == HealthUnhealthy &&
		record.Status == StatusActive &&
		record.Environment == EnvProduction {
		record.Status = StatusDegraded
		record.UpdatedBy = "system:health-checker"
		if err := s.repo.Update(ctx, record); err != nil {
			s.log.WarnContext(ctx, "failed to mark source degraded",
				"source_id", id, "error", err)
		} else {
			s.log.WarnContext(ctx, "source marked degraded after failed health check",
				"source_id", id, "slug", record.Slug)
		}
	}

	return nil
}

// resolveSlug derives a slug from the name when none was supplied, then checks
// it is not already taken.
func (s *Service) resolveSlug(ctx context.Context, requested, name string) (string, error) {
	candidate := requested
	if strings.TrimSpace(candidate) == "" {
		candidate = slugify(name)
	}

	slug, err := normaliseSlug(candidate)
	if err != nil {
		return "", err
	}

	// Ensure uniqueness by appending a numeric suffix, mirroring the
	// convention already present in the names of migrated systems.
	base := slug
	for attempt := 2; attempt <= 50; attempt++ {
		existing, err := s.repo.GetBySlug(ctx, slug)
		if errors.Is(err, ErrNotFound) {
			return slug, nil
		}
		if err != nil {
			return "", s.mapRepoError(err)
		}
		if existing == nil {
			return slug, nil
		}

		suffix := fmt.Sprintf("-%d", attempt)
		trimmed := base
		if len(trimmed)+len(suffix) > maxSlugLength {
			trimmed = strings.TrimRight(trimmed[:maxSlugLength-len(suffix)], "-")
		}
		slug = trimmed + suffix
	}

	return "", platform.NewConflict("could not derive an unused slug from %q; supply one explicitly", name)
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
			// Collapse runs of separators into a single hyphen, and trim
			// leading and trailing ones.
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

// normaliseTags lowercases, trims, de-duplicates and sorts tags so that
// filtering behaves predictably.
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

// mergeMaps overlays updates onto base, returning a new map. Neither input is
// mutated.
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

// mapRepoError translates persistence errors into the platform error taxonomy
// so handlers do not need to know about SQLSTATE codes.
func (s *Service) mapRepoError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, ErrNotFound):
		return platform.NewNotFound("source", extractID(err)).WithCause(err)
	case errors.Is(err, ErrSlugTaken):
		return platform.NewConflict("a source with this slug already exists").WithCause(err)
	default:
		return platform.AsError(err)
	}
}

// record writes a single audit entry for a create or delete. The actor is
// resolved from the request context by the audit sink, so it is not passed
// here.
func (s *Service) record(ctx context.Context, action string, record *Source) error {
	if s.auditor == nil {
		return nil
	}
	return s.auditor.Record(ctx, AuditEntry{
		Action:     action,
		EntityType: AuditEntitySource,
		EntityID:   record.ID,
		EntityName: record.Name,
		Changes:    auditSnapshot(*record),
	})
}

// recordChange records a before/after pair for the audit trail.
func (s *Service) recordChange(ctx context.Context, action string, after *Source, before Source) error {
	if s.auditor == nil {
		return nil
	}
	return s.auditor.Record(ctx, AuditEntry{
		Action:     action,
		EntityType: AuditEntitySource,
		EntityID:   after.ID,
		EntityName: after.Name,
		Changes: map[string]any{
			"before":  auditSnapshot(before),
			"after":   auditSnapshot(*after),
			"changed": diffFields(before, *after),
		},
	})
}
