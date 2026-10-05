package dataset

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

// slugPattern mirrors the `datasets_slug_format` CHECK constraint.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// maxSlugLength bounds generated slugs.
const maxSlugLength = 100

// Auditor records state changes. The audit domain supplies the implementation;
// declaring it here avoids a dependency cycle between domains.
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

// AuditEntityDataset is the audit entity type for datasets.
const AuditEntityDataset = "dataset"

// Service holds the business rules for datasets. Most of its value is
// governance: classification floors, sensitivity propagation and the
// restrictions on destructive change.
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

// NewService builds a dataset service.
func NewService(repo Repository, opts ...ServiceOption) (*Service, error) {
	if repo == nil {
		return nil, errors.New("dataset service: repository is required")
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

// Create registers a dataset and its schema.
func (s *Service) Create(ctx context.Context, req CreateDatasetRequest, actor platform.Actor) (*Dataset, error) {
	if err := s.validate.Struct(req); err != nil {
		return nil, validationError(err)
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	req.OwnerID = strings.TrimSpace(req.OwnerID)

	kind := Kind(strings.ToLower(string(req.Kind)))
	if !kind.Valid() {
		return nil, invalidEnum("kind", req.Kind,
			"must be one of: table, view, stream, file, api, ml_feature_set")
	}

	classification := Classification(strings.ToLower(string(req.Classification)))
	if !classification.Valid() {
		return nil, invalidEnum("classification", req.Classification,
			"must be one of: public, internal, confidential, restricted")
	}

	status := Status(strings.ToLower(string(req.Status)))
	if !status.Valid() {
		status = StatusDraft
	}

	if req.RetentionDays < 0 {
		return nil, invalidEnum("retention_days", req.RetentionDays, "must not be negative")
	}

	slug, err := s.resolveSlug(ctx, req.Slug, req.Name)
	if err != nil {
		return nil, err
	}

	columns, err := buildColumns(req.Columns)
	if err != nil {
		return nil, err
	}

	// A dataset holding personal or regulated data can never be declared
	// public. This is checked here rather than left to a background policy
	// sweep so an obviously wrong classification cannot be committed at all.
	if floor, ok := sensitivityFloor(columns); ok && !classification.AtLeast(floor) {
		return nil, platform.NewValidation(
			"classification %q is too permissive for columns classified %q or above; use %q or stricter",
			classification, floor, floor).
			WithFields(fieldError("classification", "consistency",
				"classification must be at least "+string(floor)))
	}

	now := time.Now().UTC()
	record := &Dataset{
		Name:             req.Name,
		Slug:             slug,
		Description:      req.Description,
		Kind:             kind,
		Classification:   classification,
		Status:           status,
		PhysicalLocation: req.PhysicalLocation,
		SourceID:         req.SourceID,
		Domain:           strings.TrimSpace(req.Domain),
		RetentionDays:    req.RetentionDays,
		SchemaVersion:    1,
		OwnerID:          req.OwnerID,
		StewardID:        strings.TrimSpace(req.StewardID),
		Tags:             normaliseTags(req.Tags),
		Metadata:         req.Metadata,
		CreatedBy:        actor.ID,
		UpdatedBy:        actor.ID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if record.StewardID == "" {
		// Default stewardship to ownership so every dataset has an accountable
		// party for data quality questions.
		record.StewardID = record.OwnerID
	}

	if err := s.repo.Create(ctx, record, columns); err != nil {
		return nil, mapRepoError(err)
	}

	s.log.InfoContext(ctx, "dataset created",
		"dataset_id", record.ID, "slug", record.Slug,
		"classification", record.Classification, "columns", len(columns),
		"actor", actor.ID)

	s.record(ctx, "dataset.created", record, map[string]any{
		"columns":        len(columns),
		"classification": record.Classification,
		"schema_version": record.SchemaVersion,
	})

	return record, nil
}

// GetByID returns a dataset with its schema populated.
func (s *Service) GetByID(ctx context.Context, id uuid.UUID) (*Dataset, error) {
	record, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, mapRepoError(err)
	}

	columns, err := s.repo.Columns(ctx, id)
	if err != nil {
		return nil, platform.AsError(err)
	}
	record.Columns = columns

	return record, nil
}

// GetBySlug returns a dataset addressed by slug, with its schema.
func (s *Service) GetBySlug(ctx context.Context, slug string) (*Dataset, error) {
	record, err := s.repo.GetBySlug(ctx, strings.TrimSpace(slug))
	if err != nil {
		return nil, mapRepoError(err)
	}

	columns, err := s.repo.Columns(ctx, record.ID)
	if err != nil {
		return nil, platform.AsError(err)
	}
	record.Columns = columns

	return record, nil
}

// List returns a filtered, paginated page of datasets.
func (s *Service) List(ctx context.Context, q ListDatasetsQuery) (platform.PageResult[*Dataset], error) {
	page, err := s.repo.List(ctx, q)
	if err != nil {
		return platform.PageResult[*Dataset]{}, mapRepoError(err)
	}
	return page, nil
}

// Update applies a partial change set, enforcing the governance invariants that
// span fields.
func (s *Service) Update(ctx context.Context, id uuid.UUID, req UpdateDatasetRequest, actor platform.Actor) (*Dataset, error) {
	if err := s.validate.Struct(req); err != nil {
		return nil, validationError(err)
	}

	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, mapRepoError(err)
	}

	existingColumns, err := s.repo.Columns(ctx, id)
	if err != nil {
		return nil, platform.AsError(err)
	}
	existing.Columns = existingColumns
	before := *existing

	// An archived dataset is frozen: its schema, classification and location are
	// part of the historical record that downstream consumers rely on.
	if existing.Status.Terminal() {
		return nil, platform.NewConflict(
			"dataset %s is archived and can no longer be modified", existing.Slug)
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
	if req.Kind != nil {
		k := Kind(strings.ToLower(string(*req.Kind)))
		if !k.Valid() {
			return nil, invalidEnum("kind", *req.Kind,
				"must be one of: table, view, stream, file, api, ml_feature_set")
		}
		existing.Kind = k
	}
	if req.Classification != nil {
		c := Classification(strings.ToLower(string(*req.Classification)))
		if !c.Valid() {
			return nil, invalidEnum("classification", *req.Classification,
				"must be one of: public, internal, confidential, restricted")
		}
		// Downgrading below the sensitivity floor implied by the existing
		// columns would silently expose regulated data.
		if floor, ok := sensitivityFloor(existingColumns); ok && !c.AtLeast(floor) {
			return nil, platform.NewValidation(
				"classification cannot be lowered to %q while the dataset contains columns classified %q",
				c, floor).
				WithFields(fieldError("classification", "downgrade_forbidden",
					"remove or reclassify the sensitive columns first"))
		}
		existing.Classification = c
	}
	if req.Status != nil {
		st := Status(strings.ToLower(string(*req.Status)))
		if !st.Valid() {
			return nil, invalidEnum("status", *req.Status,
				"must be one of: draft, active, deprecated, archived")
		}
		if st == StatusActive && !s.readyForActivation(existing, existingColumns) {
			return nil, platform.NewValidation(
				"dataset cannot become active: it has no steward and no columns").
				WithFields(fieldError("status", "readiness",
					"assign a steward and declare at least one column"))
		}
		existing.Status = st
	}
	if req.ClearSourceID {
		existing.SourceID = nil
	} else if req.SourceID != nil {
		existing.SourceID = req.SourceID
	}
	if req.PhysicalLocation != nil {
		existing.PhysicalLocation = req.PhysicalLocation
	}
	if req.Domain != nil {
		existing.Domain = strings.TrimSpace(*req.Domain)
	}
	if req.RetentionDays != nil {
		if *req.RetentionDays < 0 {
			return nil, invalidEnum("retention_days", *req.RetentionDays, "must not be negative")
		}
		existing.RetentionDays = *req.RetentionDays
	}
	if req.OwnerID != nil {
		existing.OwnerID = strings.TrimSpace(*req.OwnerID)
	}
	if req.StewardID != nil {
		existing.StewardID = strings.TrimSpace(*req.StewardID)
	}
	if req.Tags != nil {
		existing.Tags = normaliseTags(*req.Tags)
	}
	if req.ReplaceMetadata {
		existing.Metadata = req.Metadata
	} else if req.Metadata != nil {
		existing.Metadata = mergeMaps(existing.Metadata, req.Metadata)
	}

	// Schema replacement is handled after the row update so a schema-level
	// failure does not leave a half-applied set of scalar changes.
	schemaChanged := false
	if req.ReplaceColumns != nil {
		columns, err := buildColumns(req.ReplaceColumns)
		if err != nil {
			return nil, err
		}
		if floor, ok := sensitivityFloor(columns); ok && !existing.Classification.AtLeast(floor) {
			return nil, platform.NewValidation(
				"the requested columns are classified %q, which is above the dataset's %q classification",
				floor, existing.Classification).
				WithFields(fieldError("replace_columns", "consistency",
					"raise the classification before adding sensitive columns"))
		}
		existing.Columns = columns
		schemaChanged = true
	}

	// Any schema change is a breaking change from the perspective of consumers,
	// so the major version always advances.
	if schemaChanged || req.BumpSchemaVersion {
		existing.SchemaVersion++
	}
	existing.UpdatedBy = actor.ID

	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, mapRepoError(err)
	}

	if schemaChanged {
		if err := s.repo.ReplaceColumns(ctx, id, existing.Columns); err != nil {
			return nil, mapRepoError(err)
		}
		s.log.InfoContext(ctx, "dataset schema replaced",
			"dataset_id", id, "schema_version", existing.SchemaVersion,
			"columns", len(existing.Columns), "actor", actor.ID)
	}

	s.record(ctx, "dataset.updated", existing, map[string]any{
		"changed":        diffFields(before, *existing),
		"schema_version": existing.SchemaVersion,
	})

	return existing, nil
}

// Delete archives the dataset. It is a soft delete: the row and its schema are
// retained so that lineage, quality history and audit records remain coherent.
func (s *Service) Delete(ctx context.Context, id uuid.UUID, actor platform.Actor) error {
	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return mapRepoError(err)
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		return mapRepoError(err)
	}

	s.log.InfoContext(ctx, "dataset deleted",
		"dataset_id", id, "slug", existing.Slug, "actor", actor.ID)

	s.record(ctx, "dataset.deleted", existing, nil)
	return nil
}

// readyForActivation reports whether a dataset satisfies the minimum bar for
// serving consumers.
func (s *Service) readyForActivation(d *Dataset, columns []Column) bool {
	return strings.TrimSpace(d.StewardID) != "" && len(columns) > 0
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

// buildColumns validates a column set and assigns ordinals, rejecting duplicate
// names and marking sensitive columns from their PII class.
func buildColumns(inputs []ColumnInput) ([]Column, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	out := make([]Column, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))

	for i, in := range inputs {
		name := strings.TrimSpace(in.Name)
		lower := strings.ToLower(name)

		if _, dup := seen[lower]; dup {
			return nil, platform.NewValidation("column %q is declared more than once", name).
				WithFields(fieldError("columns", "duplicate", "duplicate column name: "+name))
		}
		seen[lower] = struct{}{}

		if strings.TrimSpace(in.DataType) == "" {
			return nil, platform.NewValidation("column %q is missing a data_type", name).
				WithFields(fieldError("columns", "required", "data_type is required"))
		}

		pii := PIIClass(strings.ToLower(string(in.PIIClass)))
		if !pii.Valid() {
			return nil, invalidEnum("columns."+name+".pii_class", in.PIIClass,
				"must be one of: none, pii, phi, financial, credentials")
		}

		masking := MaskingStrategy(strings.ToLower(string(in.Masking)))
		if !masking.Valid() {
			masking = MaskNone
		}

		// A sensitive column must have a masking strategy; leaving one at
		// "none" is how regulated data ends up in a log line.
		sensitive := in.IsSensitive || pii.Sensitive()
		if sensitive && masking == MaskNone && pii != PIICredentials {
			return nil, platform.NewValidation(
				"column %q is sensitive but has no masking_strategy", name).
				WithFields(fieldError("columns."+name+".masking_strategy", "required",
					"a masking strategy is required for sensitive columns"))
		}

		if in.IsPrimaryKey && in.IsNullable {
			return nil, platform.NewValidation(
				"primary key column %q cannot be nullable", name).
				WithFields(fieldError("columns."+name+".is_nullable", "primary_key",
					"a primary key column must not be nullable"))
		}

		out = append(out, Column{
			Name:         name,
			Ordinal:      i,
			DataType:     strings.TrimSpace(in.DataType),
			IsNullable:   in.IsNullable,
			IsPrimaryKey: in.IsPrimaryKey,
			IsSensitive:  sensitive,
			PIIClass:     pii,
			DefaultValue: in.DefaultValue,
			Description:  strings.TrimSpace(in.Description),
			Masking:      masking,
		})
	}

	return out, nil
}

// sensitivityFloor returns the minimum classification implied by a column set,
// so the dataset carrying it cannot be labelled less sensitive.
func sensitivityFloor(columns []Column) (Classification, bool) {
	floor := ClassPublic

	for _, c := range columns {
		var implied Classification

		switch c.PIIClass {
		case PIICredentials:
			implied = ClassRestricted
		case PIIFinancial, PIIHealth:
			implied = ClassConfidential
		case PIIPersonal:
			implied = ClassInternal
		case PIINone, "":
			implied = ClassPublic
		default:
			implied = ClassRestricted
		}

		// A column explicitly flagged sensitive but carrying no PII class still
		// warrants internal handling.
		if c.IsSensitive && implied == ClassPublic {
			implied = ClassInternal
		}
		if implied.Rank() > floor.Rank() {
			floor = implied
		}
	}

	if floor == ClassPublic {
		return ClassPublic, false
	}
	return floor, true
}

// record writes an audit entry, logging rather than failing the request: the
// mutation has already been committed, and surfacing an audit failure to the
// caller would invite a retry that duplicates the work.
func (s *Service) record(ctx context.Context, action string, d *Dataset, extra map[string]any) {
	if s.auditor == nil {
		return
	}

	changes := auditSnapshot(*d)
	for k, v := range extra {
		changes[k] = v
	}

	if err := s.auditor.Record(ctx, AuditEntry{
		Action:     action,
		EntityType: AuditEntityDataset,
		EntityID:   d.ID,
		EntityName: d.Name,
		Changes:    changes,
	}); err != nil {
		s.log.ErrorContext(ctx, "failed to record audit entry",
			"action", action, "dataset_id", d.ID, "error", err)
	}
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

// normaliseTags lowercases, trims, de-duplicates and sorts tags.
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

// mapRepoError translates persistence errors into the platform taxonomy.
func mapRepoError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, ErrNotFound):
		return platform.NewNotFound("dataset", extractID(err)).WithCause(err)
	case errors.Is(err, ErrSlugTaken):
		return platform.NewConflict("a dataset with this slug already exists").WithCause(err)
	case errors.Is(err, ErrSchemaConflict):
		return platform.NewValidation("%s", err.Error()).WithCause(err)
	default:
		return platform.AsError(err)
	}
}
