// Package pipeline manages data pipelines: their directed acyclic graphs of
// tasks, their schedules, and the contract with the orchestrator that runs them.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// Status is a pipeline's lifecycle state.
type Status string

// Pipeline lifecycle states.
const (
	StatusDraft    Status = "draft"
	StatusActive   Status = "active"
	StatusPaused   Status = "paused"
	StatusArchived Status = "archived"
)

var allStatuses = map[Status]bool{
	StatusDraft: true, StatusActive: true, StatusPaused: true, StatusArchived: true,
}

// Valid reports whether s is a supported status.
func (s Status) Valid() bool { return allStatuses[s] }

// Terminal reports whether the pipeline is archived and frozen.
func (s Status) Terminal() bool { return s == StatusArchived }

// Schedulable reports whether the pipeline may be triggered by the orchestrator
// on its schedule.
func (s Status) Schedulable() bool { return s == StatusActive }

// Pipeline is a data pipeline.
type Pipeline struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`

	// DAGID is the identifier published to Airflow. It is empty until the
	// pipeline is deployed.
	DAGID string `json:"dag_id"`

	Status   Status `json:"status"`
	Schedule string `json:"schedule"`
	Timezone string `json:"timezone"`

	MaxActiveRuns int  `json:"max_active_runs"`
	Catchup       bool `json:"catchup"`

	DefaultDatasetID *uuid.UUID `json:"default_dataset_id,omitempty"`

	OwnerID string   `json:"owner_id"`
	Tags    []string `json:"tags"`

	// TaskCount is maintained by the service and avoids a count(*) per read.
	TaskCount int `json:"task_count"`
	// GraphHash changes whenever the DAG shape changes, letting the execution
	// service detect drift between the desired and deployed graph.
	GraphHash string `json:"graph_hash"`

	// Tasks and Dependencies are populated on detail reads only.
	Tasks        []Task       `json:"tasks,omitempty"`
	Dependencies []Dependency `json:"dependencies,omitempty"`

	Metadata map[string]any `json:"metadata"`

	CreatedBy string    `json:"created_by"`
	UpdatedBy string    `json:"updated_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// HasTag reports whether the pipeline carries the named tag.
func (p *Pipeline) HasTag(tag string) bool {
	for _, t := range p.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// TaskByKey finds a task by its key.
func (p *Pipeline) TaskByKey(key string) (Task, bool) {
	for _, t := range p.Tasks {
		if t.TaskKey == key {
			return t, true
		}
	}
	return Task{}, false
}

// Task is one node in a pipeline's DAG.
type Task struct {
	ID         uuid.UUID `json:"id"`
	PipelineID uuid.UUID `json:"pipeline_id"`
	TaskKey    string    `json:"task_key"`
	Name       string    `json:"name"`

	OperatorType   string         `json:"operator_type"`
	OperatorConfig map[string]any `json:"operator_config"`

	UpstreamDatasetID   *uuid.UUID `json:"upstream_dataset_id,omitempty"`
	DownstreamDatasetID *uuid.UUID `json:"downstream_dataset_id,omitempty"`

	MaxRetries        int `json:"max_retries"`
	RetryDelaySeconds int `json:"retry_delay_seconds"`
	TimeoutSeconds    int `json:"timeout_seconds"`

	// SLAMinutes is the freshness expectation for the task's output. It is
	// meaningful only when the task declares a downstream dataset.
	SLAMinutes *int `json:"sla_minutes,omitempty"`

	IsCritical bool `json:"is_critical"`
	Enabled    bool `json:"enabled"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TaskInput is a task as supplied by a client. It differs from Task only in
// fields that need three-state semantics: a *bool distinguishes an explicit
// `false` from an omitted value, which a plain bool cannot.
//
// The service maps every TaskInput to a Task, filling defaults for the fields
// that were left unset.
type TaskInput struct {
	TaskKey        string         `json:"task_key"    validate:"required,max=100"`
	Name           string         `json:"name"        validate:"omitempty,max=200"`
	OperatorType   string         `json:"operator_type" validate:"required,max=60"`
	OperatorConfig map[string]any `json:"operator_config"`

	UpstreamDatasetID   *uuid.UUID `json:"upstream_dataset_id"`
	DownstreamDatasetID *uuid.UUID `json:"downstream_dataset_id"`

	MaxRetries        *int `json:"max_retries"`
	RetryDelaySeconds *int `json:"retry_delay_seconds"`
	TimeoutSeconds    *int `json:"timeout_seconds"`

	SLAMinutes *int `json:"sla_minutes"`

	IsCritical bool `json:"is_critical"`
	// Enabled defaults to true when omitted, matching the behaviour authors
	// expect when they add a task to a draft pipeline.
	Enabled *bool `json:"enabled"`
}

// toTask applies defaults to a client-supplied task.
func (in TaskInput) toTask() Task {
	t := Task{
		TaskKey:             strings.TrimSpace(in.TaskKey),
		Name:                strings.TrimSpace(in.Name),
		OperatorType:        strings.ToLower(strings.TrimSpace(in.OperatorType)),
		OperatorConfig:      in.OperatorConfig,
		UpstreamDatasetID:   in.UpstreamDatasetID,
		DownstreamDatasetID: in.DownstreamDatasetID,
		SLAMinutes:          in.SLAMinutes,
		IsCritical:          in.IsCritical,
	}

	// Default the display name from the key so a task declared as
	// "load_orders" reads as "Load Orders" in the UI without extra input.
	if t.Name == "" {
		t.Name = humanise(t.TaskKey)
	}

	// Defaults mirror the column defaults in the schema, so an omitted field
	// behaves the same whether it is applied in Go or by PostgreSQL.
	if in.MaxRetries != nil {
		t.MaxRetries = *in.MaxRetries
	}
	if in.RetryDelaySeconds != nil {
		t.RetryDelaySeconds = *in.RetryDelaySeconds
	}
	if in.TimeoutSeconds != nil {
		t.TimeoutSeconds = *in.TimeoutSeconds
	} else {
		t.TimeoutSeconds = 3600
	}
	if t.RetryDelaySeconds == 0 {
		t.RetryDelaySeconds = 300
	}
	if in.Enabled != nil {
		t.Enabled = *in.Enabled
	} else {
		t.Enabled = true
	}
	if t.OperatorConfig == nil {
		t.OperatorConfig = map[string]any{}
	}

	return t
}

// Dependency is a directed edge between two tasks of the same pipeline:
// upstream must complete before downstream may start.
type Dependency struct {
	UpstreamKey   string    `json:"upstream_key"`
	DownstreamKey string    `json:"downstream_key"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
}

// CreatePipelineRequest is the payload for creating a pipeline.
type CreatePipelineRequest struct {
	Name        string `json:"name"        validate:"required,min=1,max=200"`
	Slug        string `json:"slug"        validate:"omitempty,max=100"`
	Description string `json:"description" validate:"max=4000"`

	DAGID    string `json:"dag_id"    validate:"omitempty,max=200"`
	Schedule string `json:"schedule"  validate:"omitempty,max=100"`
	Timezone string `json:"timezone"  validate:"omitempty,max=64"`
	Status   Status `json:"status"`

	MaxActiveRuns    int        `json:"max_active_runs"`
	Catchup          bool       `json:"catchup"`
	DefaultDatasetID *uuid.UUID `json:"default_dataset_id"`
	OwnerID          string     `json:"owner_id"      validate:"required,max=200"`
	Tags             []string   `json:"tags"          validate:"max=20,dive,max=60"`

	// Tasks and Dependencies define the DAG. The service validates the graph
	// before persisting anything.
	Tasks        []TaskInput  `json:"tasks"        validate:"max=500,dive"`
	Dependencies []Dependency `json:"dependencies" validate:"max=2000,dive"`

	Metadata map[string]any `json:"metadata"`
}

// UpdatePipelineRequest is a partial update of the pipeline and, optionally, its
// graph.
type UpdatePipelineRequest struct {
	Name        *string `json:"name"        validate:"omitempty,min=1,max=200"`
	Slug        *string `json:"slug"        validate:"omitempty,max=100"`
	Description *string `json:"description" validate:"omitempty,max=4000"`

	DAGID               *string    `json:"dag_id"    validate:"omitempty,max=200"`
	ClearDAGID          bool       `json:"clear_dag_id"`
	Schedule            *string    `json:"schedule"  validate:"omitempty,max=100"`
	Timezone            *string    `json:"timezone"  validate:"omitempty,max=64"`
	Status              *Status    `json:"status"`
	MaxActiveRuns       *int       `json:"max_active_runs"`
	Catchup             *bool      `json:"catchup"`
	DefaultDatasetID    *uuid.UUID `json:"default_dataset_id"`
	ClearDefaultDataset bool       `json:"clear_default_dataset"`

	OwnerID *string   `json:"owner_id" validate:"omitempty,max=200"`
	Tags    *[]string `json:"tags"    validate:"omitempty,max=20,dive,max=60"`

	// Tasks and Dependencies replace the whole graph when present. Omitting
	// them leaves the existing graph untouched.
	ReplaceGraph bool         `json:"replace_graph"`
	Tasks        []TaskInput  `json:"tasks"        validate:"max=500,dive"`
	Dependencies []Dependency `json:"dependencies" validate:"max=2000,dive"`

	Metadata map[string]any `json:"metadata"`
	// ReplaceMetadata distinguishes "leave alone" from "clear".
	ReplaceMetadata bool `json:"replace_metadata"`
}

// PipelineSpec is the pipeline plus its graph, the unit the Validator checks.
type PipelineSpec struct {
	Name          string
	Slug          string
	DAGID         string
	Schedule      string
	Timezone      string
	MaxActiveRuns int
	Tasks         []Task
	Dependencies  []Dependency
}

// Spec projects a pipeline and an optional replacement graph into a PipelineSpec.
func Spec(p *Pipeline, tasks []Task, deps []Dependency) PipelineSpec {
	spec := PipelineSpec{
		Name:          p.Name,
		Slug:          p.Slug,
		DAGID:         p.DAGID,
		Schedule:      p.Schedule,
		Timezone:      p.Timezone,
		MaxActiveRuns: p.MaxActiveRuns,
	}
	if tasks != nil {
		spec.Tasks = tasks
	} else {
		spec.Tasks = p.Tasks
	}
	if deps != nil {
		spec.Dependencies = deps
	} else {
		spec.Dependencies = p.Dependencies
	}
	return spec
}

// ListPipelinesQuery are the filters accepted by the list endpoint.
type ListPipelinesQuery struct {
	platform.Page
	platform.Sort

	Status   string   `json:"status"`
	OwnerID  string   `json:"owner_id"`
	DAGID    string   `json:"dag_id"`
	Search   string   `json:"search"`
	Tag      []string `json:"tag"`
	Deployed *bool    `json:"deployed"`
}

// SortableColumns maps the API's `sort_by` values onto real columns.
func SortableColumns() map[string]string {
	return map[string]string{
		"name":       "name",
		"created_at": "created_at",
		"updated_at": "updated_at",
		"status":     "status",
		"task_count": "task_count",
	}
}

// DefaultSort is applied when the caller does not specify one.
func DefaultSort() platform.Sort {
	return platform.Sort{By: "created_at", Order: "desc"}
}

// GraphHash returns a stable fingerprint of a task set and its edges. Two
// graphs with the same hash have identical shape, regardless of task names,
// retry policy or metadata.
//
// The hash covers, for each task in sorted key order: the key, the operator
// type, the upstream and downstream datasets, and the sorted dependency edges.
func GraphHash(tasks []Task, deps []Dependency) string {
	keys := make([]string, 0, len(tasks))
	byKey := make(map[string]Task, len(tasks))
	for _, t := range tasks {
		keys = append(keys, t.TaskKey)
		byKey[t.TaskKey] = t
	}
	sort.Strings(keys)

	h := sha256.New()

	for _, key := range keys {
		task := byKey[key]
		h.Write([]byte(task.TaskKey))
		h.Write([]byte{0})
		h.Write([]byte(task.OperatorType))
		h.Write([]byte{0})
		h.Write([]byte(uuidString(task.UpstreamDatasetID)))
		h.Write([]byte{0})
		h.Write([]byte(uuidString(task.DownstreamDatasetID)))
		h.Write([]byte{0})
	}

	edges := make([]string, 0, len(deps))
	for _, d := range deps {
		edges = append(edges, d.UpstreamKey+"\x00"+d.DownstreamKey)
	}
	sort.Strings(edges)
	for _, edge := range edges {
		h.Write([]byte(edge))
		h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil))
}

func uuidString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// jsonMap decodes a nullable JSONB column, tolerating SQL NULL.
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
