// Package airflow is a thin client over the Airflow stable REST API, used to
// trigger DAG runs and to read run state back into the control plane.
package airflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/edp/edp-control-plane/internal/config"
)

// Errors surfaced by the client.
var (
	// ErrNotFound maps to Airflow's 404.
	ErrNotFound = errors.New("airflow resource not found")
	// ErrUnauthorized maps to Airflow's 401/403.
	ErrUnauthorized = errors.New("airflow rejected the credentials")
	// ErrUnavailable maps to Airflow being unreachable or returning 5xx.
	ErrUnavailable = errors.New("airflow is unavailable")
	// ErrConflict maps to a duplicate run id, which callers may treat as an
	// idempotent success.
	ErrConflict = errors.New("airflow run already exists")
)

// maxErrorBodyBytes bounds how much of an error response we read before
// logging, so a misbehaving upstream cannot flood the logs.
const maxErrorBodyBytes = 2 << 10

// Client talks to a single Airflow deployment.
type Client struct {
	baseURL  string
	username string
	password string

	dagIDPrefix string
	http        *http.Client
}

// NewClient builds a client from configuration.
func NewClient(cfg config.AirflowConfig) (*Client, error) {
	if !cfg.Enabled {
		return nil, errors.New("airflow client: integration is disabled")
	}

	base, err := url.Parse(strings.TrimSuffix(cfg.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("airflow client: invalid base url %q: %w", cfg.BaseURL, err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("airflow client: base url scheme must be http or https, got %q", base.Scheme)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &Client{
		baseURL:     base.String(),
		username:    cfg.Username,
		password:    cfg.Password,
		dagIDPrefix: cfg.DAGIDPrefix,
		// Bound the whole exchange; per-request deadlines are also applied by
		// callers so a slow trigger cannot consume the whole request budget.
		http: &http.Client{Timeout: timeout},
	}, nil
}

// DAGRun is the subset of an Airflow DAG run the control plane consumes.
type DAGRun struct {
	DAGRunID    string  `json:"dag_run_id"`
	DAGID       string  `json:"dag_id"`
	State       string  `json:"state"`
	LogicalDate *string `json:"logical_date,omitempty"`
	StartedAt   *string `json:"start_date,omitempty"`
	EndedAt     *string `json:"end_date,omitempty"`
}

// RunState values reported by Airflow.
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateSuccess   = "success"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// TriggerRunOptions describes a run to request from Airflow.
type TriggerRunOptions struct {
	// DAGID is the Airflow DAG to run. It is prefixed with the configured
	// dag_id_prefix when one is set.
	DAGID string
	// RunID is the caller-supplied idempotency key. Supplying a stable value
	// makes a retried request safe: Airflow rejects the duplicate rather than
	// starting a second run.
	RunID string
	// Conf is the Airflow DAG run configuration payload, merged into params.
	Conf map[string]any
	// LogicalDate pins the run's data interval, used for backfills.
	LogicalDate *time.Time
}

// TriggerRun asks Airflow to start a DAG run.
func (c *Client) TriggerRun(ctx context.Context, opts TriggerRunOptions) (DAGRun, error) {
	dagID, err := c.qualifyDAGID(opts.DAGID)
	if err != nil {
		return DAGRun{}, err
	}

	body := map[string]any{}
	if opts.RunID != "" {
		body["dag_run_id"] = opts.RunID
	}
	if len(opts.Conf) > 0 {
		body["conf"] = opts.Conf
	}
	if opts.LogicalDate != nil {
		body["logical_date"] = opts.LogicalDate.UTC().Format(time.RFC3339)
	}

	ctx, cancel := context.WithTimeout(ctx, c.http.Timeout)
	defer cancel()

	var run DAGRun
	path := fmt.Sprintf("/api/v2/dags/%s/dagRuns", url.PathEscape(dagID))
	if err := c.do(ctx, http.MethodPost, path, body, &run); err != nil {
		return DAGRun{}, fmt.Errorf("trigger dag %q: %w", dagID, err)
	}
	return run, nil
}

// GetRunState reads the current state of a DAG run.
func (c *Client) GetRunState(ctx context.Context, dagID, dagRunID string) (DAGRun, error) {
	qualified, err := c.qualifyDAGID(dagID)
	if err != nil {
		return DAGRun{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.http.Timeout)
	defer cancel()

	var run DAGRun
	path := fmt.Sprintf(
		"/api/v2/dags/%s/dagRuns/%s",
		url.PathEscape(qualified), url.PathEscape(dagRunID),
	)
	if err := c.do(ctx, http.MethodGet, path, nil, &run); err != nil {
		return DAGRun{}, fmt.Errorf("get dag run %s/%s: %w", qualified, dagRunID, err)
	}
	return run, nil
}

// PauseDAG suspends scheduling for a DAG, leaving existing runs untouched.
func (c *Client) PauseDAG(ctx context.Context, dagID string) error {
	return c.setPaused(ctx, dagID, true)
}

// UnpauseDAG resumes scheduling for a DAG.
func (c *Client) UnpauseDAG(ctx context.Context, dagID string) error {
	return c.setPaused(ctx, dagID, false)
}

func (c *Client) setPaused(ctx context.Context, dagID string, paused bool) error {
	qualified, err := c.qualifyDAGID(dagID)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, c.http.Timeout)
	defer cancel()

	body := map[string]any{"is_paused": paused}
	path := fmt.Sprintf("/api/v2/dags/%s", url.PathEscape(qualified))
	if err := c.do(ctx, http.MethodPatch, path, body, nil); err != nil {
		verb := "pause"
		if !paused {
			verb = "unpause"
		}
		return fmt.Errorf("%s dag %q: %w", verb, qualified, err)
	}
	return nil
}

// Ping verifies connectivity, used by the readiness probe.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.http.Timeout)
	defer cancel()

	if err := c.do(ctx, http.MethodGet, "/api/v2/monitor/health", nil, nil); err != nil {
		return fmt.Errorf("airflow health check: %w", err)
	}
	return nil
}

// qualifyDAGID applies the configured prefix and rejects empty identifiers.
func (c *Client) qualifyDAGID(dagID string) (string, error) {
	trimmed := strings.TrimSpace(dagID)
	if trimmed == "" {
		return "", errors.New("airflow: dag id must not be empty")
	}
	if strings.ContainsAny(trimmed, "/\\") {
		return "", fmt.Errorf("airflow: dag id %q must not contain path separators", dagID)
	}
	return c.dagIDPrefix + trimmed, nil
}

// do performs one JSON request, mapping transport and status errors onto the
// package's sentinel errors so callers can branch with errors.Is.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrUnavailable, sanitize(err.Error()))
	}
	defer func() {
		// Drain a bounded amount so the connection can be reused, then close.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: status %d", ErrUnauthorized, resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s %s returned 404", ErrNotFound, method, path)
	case resp.StatusCode == http.StatusConflict:
		// Airflow uses 409 for a duplicate dag_run_id, which callers treat as
		// an idempotent success rather than a failure.
		return fmt.Errorf("%w: conflict (a run with this id already exists)", ErrConflict)
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	case resp.StatusCode >= 400:
		return fmt.Errorf("airflow: %s %s returned %d: %s",
			method, path, resp.StatusCode, c.errorSummary(resp))
	}

	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode airflow response: %w", err)
	}
	return nil
}

// errorSummary extracts the `detail` or `title` field Airflow returns on
// errors, falling back to a truncated body.
func (c *Client) errorSummary(resp *http.Response) string {
	var payload struct {
		Detail string `json:"detail"`
		Title  string `json:"title"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxErrorBodyBytes)).Decode(&payload); err == nil {
		if payload.Detail != "" {
			return sanitize(payload.Detail)
		}
		if payload.Title != "" {
			return sanitize(payload.Title)
		}
	}
	return strconv.Itoa(resp.StatusCode)
}

// sanitize collapses whitespace and strips control characters so untrusted
// upstream text cannot forge multi-line log records.
func sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
