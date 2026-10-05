// Package source manages the upstream systems the enterprise data platform
// ingests from: registering them, tracking their health, and rotating the
// secret references that hold their credentials.
package source

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// Type enumerates the connector families the platform supports. The zero value
// is invalid.
type Type string

// Supported source types.
const (
	TypePostgres   Type = "postgres"
	TypeMySQL      Type = "mysql"
	TypeS3         Type = "s3"
	TypeKafka      Type = "kafka"
	TypeBigQuery   Type = "bigquery"
	TypeSnowflake  Type = "snowflake"
	TypeSalesforce Type = "salesforce"
	TypeSAP        Type = "sap"
	TypeSharePoint Type = "sharepoint"
	TypeAPI        Type = "api"
	TypeFile       Type = "file"
)

// allTypes is the authoritative set, used for request validation.
var allTypes = map[Type]bool{
	TypePostgres: true, TypeMySQL: true, TypeS3: true, TypeKafka: true,
	TypeBigQuery: true, TypeSnowflake: true, TypeSalesforce: true,
	TypeSAP: true, TypeSharePoint: true, TypeAPI: true, TypeFile: true,
}

// Valid reports whether t is a supported source type.
func (t Type) Valid() bool { return allTypes[t] }

// Types returns every supported source type, for error messages and docs.
func Types() []Type {
	out := make([]Type, 0, len(allTypes))
	for t := range allTypes {
		out = append(out, t)
	}
	return out
}

// Environment identifies which stage of the delivery lifecycle a source serves.
type Environment string

// Supported environments.
const (
	EnvDevelopment Environment = "development"
	EnvStaging     Environment = "staging"
	EnvProduction  Environment = "production"
)

var allEnvironments = map[Environment]bool{
	EnvDevelopment: true, EnvStaging: true, EnvProduction: true,
}

// Valid reports whether e is a supported environment.
func (e Environment) Valid() bool { return allEnvironments[e] }

// Status is a source's lifecycle state.
type Status string

// Source lifecycle states.
const (
	StatusDraft    Status = "draft"
	StatusActive   Status = "active"
	StatusDegraded Status = "degraded"
	StatusRetired  Status = "retired"
)

var allStatuses = map[Status]bool{
	StatusDraft: true, StatusActive: true, StatusDegraded: true, StatusRetired: true,
}

// Valid reports whether s is a supported status.
func (s Status) Valid() bool { return allStatuses[s] }

// Terminal reports whether the source has reached an end state and can no longer
// serve data.
func (s Status) Terminal() bool { return s == StatusRetired }

// IngestionMode describes how data is collected from the source.
type IngestionMode string

// Supported ingestion modes.
const (
	ModeBatch     IngestionMode = "batch"
	ModeStreaming IngestionMode = "streaming"
	ModeBoth      IngestionMode = "both"
)

var allIngestionModes = map[IngestionMode]bool{
	ModeBatch: true, ModeStreaming: true, ModeBoth: true,
}

// Valid reports whether m is a supported ingestion mode.
func (m IngestionMode) Valid() bool { return allIngestionModes[m] }

// SupportsStreaming reports whether the source can deliver continuous updates.
func (m IngestionMode) SupportsStreaming() bool {
	return m == ModeStreaming || m == ModeBoth
}

// HealthStatus is the outcome of the most recent connectivity probe.
type HealthStatus string

// Health probe outcomes.
const (
	HealthHealthy   HealthStatus = "healthy"
	HealthDegraded  HealthStatus = "degraded"
	HealthUnhealthy HealthStatus = "unhealthy"
)

var allHealthStatuses = map[HealthStatus]bool{
	HealthHealthy: true, HealthDegraded: true, HealthUnhealthy: true,
}

// Valid reports whether h is a supported health status.
func (h HealthStatus) Valid() bool { return allHealthStatuses[h] }

// Source is a registered upstream system. It is the persisted domain entity
// and doubles as the API response representation; the distinction between
// stored and returned fields is carried by the request payloads.
type Source struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`

	Type        Type          `json:"type"`
	Ingestion   IngestionMode `json:"ingestion_mode"`
	Environment Environment   `json:"environment"`
	Status      Status        `json:"status"`

	// ConnectionSecretRef is an opaque pointer such as `vault://infra/db/main`.
	// The credential itself is never stored on this record.
	ConnectionSecretRef string `json:"connection_secret_ref"`
	// ConnectionConfig holds non-sensitive connection metadata only.
	ConnectionConfig map[string]any `json:"connection_config"`

	OwnerID string   `json:"owner_id"`
	Tags    []string `json:"tags"`

	LastHealthCheckAt     *time.Time    `json:"last_health_check_at,omitempty"`
	LastHealthCheckStatus *HealthStatus `json:"last_health_check_status,omitempty"`

	Metadata map[string]any `json:"metadata"`

	CreatedBy string    `json:"created_by"`
	UpdatedBy string    `json:"updated_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsHealthy reports whether the last probe found the source reachable.
func (s *Source) IsHealthy() bool {
	return s.LastHealthCheckStatus != nil && *s.LastHealthCheckStatus == HealthHealthy
}

// HasTag reports whether the source carries the named tag.
func (s *Source) HasTag(tag string) bool {
	for _, t := range s.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// CreateSourceRequest is the payload for registering a new source.
type CreateSourceRequest struct {
	Name        string        `json:"name"        validate:"required,min=1,max=200"`
	Slug        string        `json:"slug"        validate:"omitempty,slug"`
	Description string        `json:"description" validate:"max=2000"`
	Type        Type          `json:"type"        validate:"required"`
	Ingestion   IngestionMode `json:"ingestion_mode" validate:"required"`
	Environment Environment   `json:"environment" validate:"required"`
	Status      Status        `json:"status"      validate:"omitempty"`

	ConnectionSecretRef string         `json:"connection_secret_ref" validate:"required"`
	ConnectionConfig    map[string]any `json:"connection_config"`

	OwnerID string   `json:"owner_id" validate:"required,max=200"`
	Tags    []string `json:"tags"     validate:"max=20,dive,max=60"`

	Metadata map[string]any `json:"metadata"`
}

// UpdateSourceRequest is a partial update. A nil pointer means "leave
// unchanged"; this is why every optional field is a pointer.
type UpdateSourceRequest struct {
	Name        *string        `json:"name"        validate:"omitempty,min=1,max=200"`
	Slug        *string        `json:"slug"        validate:"omitempty,slug"`
	Description *string        `json:"description" validate:"omitempty,max=2000"`
	Type        *Type          `json:"type"        validate:"omitempty"`
	Ingestion   *IngestionMode `json:"ingestion_mode" validate:"omitempty"`
	Environment *Environment   `json:"environment" validate:"omitempty"`
	Status      *Status        `json:"status"      validate:"omitempty"`

	ConnectionSecretRef *string        `json:"connection_secret_ref" validate:"omitempty"`
	ConnectionConfig    map[string]any `json:"connection_config"`
	// ReplaceConnectionConfig distinguishes "leave alone" from "clear"; a
	// caller that wants no config must send an explicit empty object.
	ReplaceConnectionConfig bool `json:"replace_connection_config"`

	OwnerID *string   `json:"owner_id" validate:"omitempty,max=200"`
	Tags    *[]string `json:"tags"     validate:"omitempty,max=20,dive,max=60"`

	Metadata map[string]any `json:"metadata"`
	// ReplaceMetadata has the same clear-vs-absent semantics as above.
	ReplaceMetadata bool `json:"replace_metadata"`
}

// ListSourcesQuery are the filters accepted by the list endpoint. Page and Sort
// are embedded so their fields (limit, offset, sort_by, ...) are read directly.
type ListSourcesQuery struct {
	platform.Page
	platform.Sort

	Type        string `json:"type"`
	Status      string `json:"status"`
	Environment string `json:"environment"`
	OwnerID     string `json:"owner_id"`
	// Search matches the name and description.
	Search string `json:"search"`
	// Tag requires the source to carry every listed tag.
	Tag []string `json:"tag"`
}

// SortableColumns maps the API's `sort_by` values onto real columns. The
// handler passes this to platform.ParseSort so an unvalidated identifier can
// never reach the SQL layer.
func SortableColumns() map[string]string {
	return map[string]string{
		"name":       "name",
		"created_at": "created_at",
		"updated_at": "updated_at",
		"status":     "status",
		"type":       "source_type",
	}
}

// DefaultSort is applied when the caller does not specify one.
func DefaultSort() platform.Sort {
	return platform.Sort{By: "created_at", Order: "desc"}
}

// jsonMap is a small helper for repositories that need to round-trip a JSON
// column that may be absent or SQL NULL.
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
