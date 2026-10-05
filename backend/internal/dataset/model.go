// Package dataset manages logical, governed data products: their schemas,
// classification and stewardship. It is the reference implementation of a
// vertical slice in this control plane.
package dataset

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// Kind describes how a dataset is physically represented.
type Kind string

// Supported dataset kinds.
const (
	KindTable        Kind = "table"
	KindView         Kind = "view"
	KindStream       Kind = "stream"
	KindFile         Kind = "file"
	KindAPI          Kind = "api"
	KindMLFeatureSet Kind = "ml_feature_set"
)

var allKinds = map[Kind]bool{
	KindTable: true, KindView: true, KindStream: true,
	KindFile: true, KindAPI: true, KindMLFeatureSet: true,
}

// Valid reports whether k is a supported kind.
func (k Kind) Valid() bool { return allKinds[k] }

// Classification is the sensitivity label that drives governance policy.
type Classification string

// Supported classifications, ordered from least to most sensitive.
const (
	ClassPublic       Classification = "public"
	ClassInternal     Classification = "internal"
	ClassConfidential Classification = "confidential"
	ClassRestricted   Classification = "restricted"
)

var allClassifications = map[Classification]bool{
	ClassPublic: true, ClassInternal: true,
	ClassConfidential: true, ClassRestricted: true,
}

// Valid reports whether c is a supported classification.
func (c Classification) Valid() bool { return allClassifications[c] }

// Rank returns the ordinal sensitivity of the classification, for comparisons
// such as "at least confidential".
func (c Classification) Rank() int {
	switch c {
	case ClassPublic:
		return 0
	case ClassInternal:
		return 1
	case ClassConfidential:
		return 2
	case ClassRestricted:
		return 3
	default:
		return -1
	}
}

// AtLeast reports whether c is at least as sensitive as other.
func (c Classification) AtLeast(other Classification) bool {
	return c.Rank() >= other.Rank()
}

// RequiresRestrictedAccess reports whether the classification demands more than
// ordinary internal access.
func (c Classification) RequiresRestrictedAccess() bool {
	return c.AtLeast(ClassConfidential)
}

// Status is a dataset's lifecycle state.
type Status string

// Dataset lifecycle states.
const (
	StatusDraft      Status = "draft"
	StatusActive     Status = "active"
	StatusDeprecated Status = "deprecated"
	StatusArchived   Status = "archived"
)

var allStatuses = map[Status]bool{
	StatusDraft: true, StatusActive: true, StatusDeprecated: true, StatusArchived: true,
}

// Valid reports whether s is a supported status.
func (s Status) Valid() bool { return allStatuses[s] }

// Terminal reports whether the dataset is archived and can no longer change.
func (s Status) Terminal() bool { return s == StatusArchived }

// PIIClass marks how sensitive an individual column is.
type PIIClass string

// Supported PII classes.
const (
	PIINone        PIIClass = "none"
	PIIPersonal    PIIClass = "pii"
	PIIHealth      PIIClass = "phi"
	PIIFinancial   PIIClass = "financial"
	PIICredentials PIIClass = "credentials"
)

var allPIIClasses = map[PIIClass]bool{
	PIINone: true, PIIPersonal: true, PIIHealth: true,
	PIIFinancial: true, PIICredentials: true,
}

// Valid reports whether p is a supported PII class.
func (p PIIClass) Valid() bool { return allPIIClasses[p] }

// Sensitive reports whether the class warrants masking or restricted access.
func (p PIIClass) Sensitive() bool { return p != PIINone && p != "" }

// MaskingStrategy describes how a sensitive value is obscured.
type MaskingStrategy string

// Supported masking strategies.
const (
	MaskNone     MaskingStrategy = "none"
	MaskHash     MaskingStrategy = "hash"
	MaskRedact   MaskingStrategy = "redact"
	MaskTokenize MaskingStrategy = "tokenize"
	MaskEncrypt  MaskingStrategy = "encrypt"
)

var allMaskingStrategies = map[MaskingStrategy]bool{
	MaskNone: true, MaskHash: true, MaskRedact: true, MaskTokenize: true, MaskEncrypt: true,
}

// Valid reports whether m is a supported masking strategy.
func (m MaskingStrategy) Valid() bool { return allMaskingStrategies[m] }

// Column is one field in a dataset's schema.
type Column struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	// Ordinal is the zero-based position, which determines physical column order.
	Ordinal      int      `json:"ordinal"`
	DataType     string   `json:"data_type"`
	IsNullable   bool     `json:"is_nullable"`
	IsPrimaryKey bool     `json:"is_primary_key"`
	IsSensitive  bool     `json:"is_sensitive"`
	PIIClass     PIIClass `json:"pii_class"`

	DefaultValue *string         `json:"default_value,omitempty"`
	Description  string          `json:"description"`
	Masking      MaskingStrategy `json:"masking_strategy"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Dataset is a governed data product.
type Dataset struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`

	Kind           Kind           `json:"kind"`
	Classification Classification `json:"classification"`
	Status         Status         `json:"status"`

	// PhysicalLocation locates the data: a catalog-qualified table name, an S3
	// prefix, a Kafka topic. Its shape depends on Kind.
	PhysicalLocation map[string]any `json:"physical_location"`

	// SourceID is the upstream source this dataset is loaded from, when there is
	// exactly one. The authoritative provenance lives in the lineage graph.
	SourceID *uuid.UUID `json:"source_id,omitempty"`

	Domain        string `json:"domain"`
	RetentionDays int    `json:"retention_days"`
	// SchemaVersion increments on every breaking schema change.
	SchemaVersion int `json:"schema_version"`

	OwnerID   string   `json:"owner_id"`
	StewardID string   `json:"steward_id"`
	Tags      []string `json:"tags"`

	// Observed statistics, refreshed by the quality service rather than set by
	// callers of this API.
	RowCount        *int64     `json:"row_count,omitempty"`
	SizeBytes       *int64     `json:"size_bytes,omitempty"`
	LastRefreshedAt *time.Time `json:"last_refreshed_at,omitempty"`

	// Columns is populated on detail reads and omitted on list reads.
	Columns []Column `json:"columns,omitempty"`

	Metadata map[string]any `json:"metadata"`

	CreatedBy string    `json:"created_by"`
	UpdatedBy string    `json:"updated_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// HasTag reports whether the dataset carries the named tag.
func (d *Dataset) HasTag(tag string) bool {
	for _, t := range d.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// ColumnByName finds a column by name, case-insensitively.
func (d *Dataset) ColumnByName(name string) (Column, bool) {
	for _, c := range d.Columns {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return Column{}, false
}

// ContainsSensitiveColumns reports whether any column is marked sensitive, which
// the service uses to reject a downgrade to a public classification.
func (d *Dataset) ContainsSensitiveColumns() bool {
	for _, c := range d.Columns {
		if c.IsSensitive || c.PIIClass.Sensitive() {
			return true
		}
	}
	return false
}

// CreateDatasetRequest is the payload for registering a dataset.
type CreateDatasetRequest struct {
	Name        string `json:"name"        validate:"required,min=1,max=200"`
	Slug        string `json:"slug"        validate:"omitempty,max=100"`
	Description string `json:"description" validate:"max=4000"`

	Kind           Kind           `json:"kind"            validate:"required"`
	Classification Classification `json:"classification" validate:"required"`
	Status         Status         `json:"status"`

	PhysicalLocation map[string]any `json:"physical_location"`
	SourceID         *uuid.UUID     `json:"source_id"`

	Domain        string   `json:"domain"         validate:"max=120"`
	RetentionDays int      `json:"retention_days"`
	OwnerID       string   `json:"owner_id"       validate:"required,max=200"`
	StewardID     string   `json:"steward_id"     validate:"max=200"`
	Tags          []string `json:"tags"          validate:"max=20,dive,max=60"`

	Columns []ColumnInput `json:"columns" validate:"max=2000,dive"`

	Metadata map[string]any `json:"metadata"`
}

// ColumnInput describes one column on create or replace.
type ColumnInput struct {
	Name         string          `json:"name"          validate:"required,max=128"`
	DataType     string          `json:"data_type"     validate:"required,max=128"`
	IsNullable   bool            `json:"is_nullable"`
	IsPrimaryKey bool            `json:"is_primary_key"`
	IsSensitive  bool            `json:"is_sensitive"`
	PIIClass     PIIClass        `json:"pii_class"`
	Description  string          `json:"description"  validate:"max=1000"`
	Masking      MaskingStrategy `json:"masking_strategy"`
	DefaultValue *string         `json:"default_value"`
}

// UpdateDatasetRequest is a partial update. Pointer fields distinguish
// "unchanged" from "set to the zero value".
type UpdateDatasetRequest struct {
	Name        *string `json:"name"        validate:"omitempty,min=1,max=200"`
	Slug        *string `json:"slug"        validate:"omitempty,max=100"`
	Description *string `json:"description" validate:"omitempty,max=4000"`

	Kind           *Kind           `json:"kind"`
	Classification *Classification `json:"classification"`
	Status         *Status         `json:"status"`

	PhysicalLocation map[string]any `json:"physical_location"`
	// ClearSourceID detaches the dataset from its upstream source.
	ClearSourceID bool       `json:"clear_source_id"`
	SourceID      *uuid.UUID `json:"source_id"`

	Domain        *string   `json:"domain"         validate:"omitempty,max=120"`
	RetentionDays *int      `json:"retention_days"`
	OwnerID       *string   `json:"owner_id"       validate:"omitempty,max=200"`
	StewardID     *string   `json:"steward_id"     validate:"omitempty,max=200"`
	Tags          *[]string `json:"tags"          validate:"omitempty,max=20,dive,max=60"`

	// ReplaceColumns swaps the whole schema. Omitted fields leave it alone.
	ReplaceColumns []ColumnInput `json:"replace_columns"`
	// BumpSchemaVersion forces a major schema version increment, used when a
	// breaking change is made without altering the column list.
	BumpSchemaVersion bool `json:"bump_schema_version"`

	Metadata map[string]any `json:"metadata"`
	// ReplaceMetadata has the same clear-versus-absent semantics as the
	// source domain.
	ReplaceMetadata bool `json:"replace_metadata"`
}

// ListDatasetsQuery are the filters accepted by the list endpoint.
type ListDatasetsQuery struct {
	platform.Page
	platform.Sort

	Kind           string   `json:"kind"`
	Classification string   `json:"classification"`
	Status         string   `json:"status"`
	SourceID       string   `json:"source_id"`
	Domain         string   `json:"domain"`
	OwnerID        string   `json:"owner_id"`
	StewardID      string   `json:"steward_id"`
	Search         string   `json:"search"`
	Tag            []string `json:"tag"`
	// IncludeColumns populates the schema on each result. It is off by default
	// because the column set dominates the response size.
	IncludeColumns bool `json:"include_columns"`
}

// SortableColumns maps the API's `sort_by` values onto real columns.
func SortableColumns() map[string]string {
	return map[string]string{
		"name":           "name",
		"created_at":     "created_at",
		"updated_at":     "updated_at",
		"status":         "status",
		"classification": "classification",
		"schema_version": "schema_version",
		"last_refreshed": "last_refreshed_at",
	}
}

// DefaultSort is applied when the caller does not specify one.
func DefaultSort() platform.Sort {
	return platform.Sort{By: "created_at", Order: "desc"}
}

// jsonMap decodes a nullable JSONB column into a map, tolerating SQL NULL and
// malformed legacy data.
func jsonMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}
