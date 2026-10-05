package audit

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// Service writes to and queries the audit trail.
type Service struct {
	repo Repository
	log  *slog.Logger

	// maxChangeSetBytes bounds how much detail is written per event, so a large
	// schema diff cannot produce a multi-megabyte audit row.
	maxChangeSetBytes int
	// slowWriteThreshold logs a warning when an audit write takes unusually
	// long, which indicates pool pressure.
	slowWriteThreshold time.Duration
}

// ServiceOption customises a Service.
type ServiceOption func(*Service)

// WithLogger overrides the logger.
func WithLogger(l *slog.Logger) ServiceOption {
	return func(s *Service) {
		if l != nil {
			s.log = l
		}
	}
}

// WithMaxChangeSetBytes overrides the change-set size limit.
func WithMaxChangeSetBytes(n int) ServiceOption {
	return func(s *Service) {
		if n > 0 {
			s.maxChangeSetBytes = n
		}
	}
}

// NewService builds an audit service.
func NewService(repo Repository, opts ...ServiceOption) (*Service, error) {
	if repo == nil {
		return nil, errors.New("audit service: repository is required")
	}

	s := &Service{
		repo:               repo,
		log:                slog.Default(),
		maxChangeSetBytes:  64 << 10,
		slowWriteThreshold: 500 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Record writes an audit event for the current request.
//
// The actor, tenant, correlation ID and peer address are all taken from the
// request context, never from the caller. That is the point: a domain service
// cannot misattribute an action, and cannot forge an actor identity.
//
// Record returns an error rather than swallowing it, but callers in the
// mutating domains log and continue, because failing a committed write just
// because auditing hiccuped would be worse than a gap in the trail.
func (s *Service) Record(ctx context.Context, req RecordRequest) error {
	return s.RecordFromHTTP(ctx, nil, req)
}

// RecordFromHTTP writes an audit event, additionally capturing the peer address
// and user agent from r. Passing a nil request is equivalent to Record.
func (s *Service) RecordFromHTTP(ctx context.Context, r *http.Request, req RecordRequest) error {
	if strings.TrimSpace(req.Action) == "" {
		return errors.New("audit service: action is required")
	}
	if strings.TrimSpace(req.EntityType) == "" {
		return errors.New("audit service: entity_type is required")
	}

	outcome := req.Outcome
	if !outcome.Valid() {
		outcome = OutcomeSuccess
	}

	e := &Event{
		Action:     req.Action,
		EntityType: req.EntityType,
		EntityID:   req.EntityID,
		EntityName: req.EntityName,
		ChangeSet:  s.limitChangeSet(Redact(req.ChangeSet)),
		Outcome:    outcome,
		UserAgent:  req.UserAgent,
		RequestID:  firstNonEmpty(req.RequestID, platform.RequestIDFrom(ctx)),
		TraceID:    req.TraceID,
		DurationMs: req.DurationMs,
	}

	if actor, ok := platform.ActorFrom(ctx); ok {
		e.ActorID = actor.ID
		e.ActorEmail = actor.Email
		e.ActorRoles = actor.Roles
		e.TenantID = actor.TenantID
	} else {
		// An unauthenticated write still needs a non-empty actor so the trail
		// distinguishes it from a malformed row.
		e.ActorID = "anonymous"
		e.ActorRoles = []string{}
	}

	if r != nil {
		if req.IPAddress != "" {
			e.IPAddress = req.IPAddress
		} else {
			e.IPAddress = PeerAddress(r)
		}
		if req.UserAgent == "" {
			e.UserAgent = r.UserAgent()
		}
	}

	started := time.Now()
	if err := s.repo.Append(ctx, e); err != nil {
		s.log.ErrorContext(ctx, "failed to append audit event",
			"action", e.Action, "entity_type", e.EntityType,
			"entity_id", e.EntityID, "request_id", e.RequestID, "error", err)
		return err
	}

	if elapsed := time.Since(started); elapsed > s.slowWriteThreshold {
		s.log.WarnContext(ctx, "audit write was slow",
			"action", e.Action, "duration_ms", elapsed.Milliseconds())
	}

	s.log.DebugContext(ctx, "audit event recorded",
		"action", e.Action, "entity_type", e.EntityType,
		"entity_id", e.EntityID, "sequence_no", e.SequenceNo)
	return nil
}

// List returns a bounded page of audit events.
func (s *Service) List(ctx context.Context, q ListEventsQuery) (platform.PageResult[*Event], error) {
	// An unbounded time-range query against an append-only table can scan the
	// whole history; require a lower bound to protect the database.
	if q.Since == nil && q.Offset == 0 && q.Limit >= platform.MaxPageSize {
		return platform.PageResult[*Event]{}, platform.NewBadRequest(
			"an audit query without a since bound must use a limit below %d; "+
				"supply since=<RFC3339> to search a wider window",
			platform.MaxPageSize)
	}

	if q.Since != nil && q.Until != nil && q.Until.Before(*q.Since) {
		return platform.PageResult[*Event]{}, platform.NewBadRequest(
			"until must not be earlier than since")
	}

	page, err := s.repo.List(ctx, q)
	if err != nil {
		return platform.PageResult[*Event]{}, platform.AsError(err)
	}
	return page, nil
}

// GetByID returns a single event.
func (s *Service) GetByID(ctx context.Context, id uuid.UUID) (*Event, error) {
	e, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, platform.NewNotFound("audit event", id).WithCause(err)
	}
	return e, nil
}

// RecordDenied writes an audit entry for an authorization denial. It is
// separate from Record so a denial cannot be misreported as a success by a
// caller that defaults the outcome field.
func (s *Service) RecordDenied(ctx context.Context, r *http.Request, actor platform.Actor, action, reason string) error {
	return s.RecordFromHTTP(ctx, r, RecordRequest{
		Action:     action,
		EntityType: EntityAuth,
		Outcome:    OutcomeDenied,
		ChangeSet: map[string]any{
			"reason":  reason,
			"roles":   actor.Roles,
			"subject": actor.ID,
		},
	})
}

// limitChangeSet truncates an oversized change set so one event cannot dominate
// storage. The truncation is explicit in the payload rather than silent.
func (s *Service) limitChangeSet(changes map[string]any) map[string]any {
	if changes == nil {
		return nil
	}
	if s.estimateSize(changes) <= s.maxChangeSetBytes {
		return changes
	}

	s.log.Warn("audit change set exceeded the size limit and was truncated",
		"limit_bytes", s.maxChangeSetBytes)

	truncated := make(map[string]any, len(changes)+1)
	kept := 0

	for k, v := range changes {
		candidate := map[string]any{k: v}
		if s.estimateSize(truncated)+s.estimateSize(candidate) > s.maxChangeSetBytes {
			break
		}
		truncated[k] = v
		kept++
	}

	truncated["_truncated"] = true
	truncated["_omitted_fields"] = len(changes) - kept
	return truncated
}

// estimateSize approximates the encoded size of a change set without a full
// marshal, which would allocate a copy of the whole structure.
func (s *Service) estimateSize(changes map[string]any) int {
	total := 0
	for k, v := range changes {
		total += len(k) + 8
		if rendered, ok := v.(string); ok {
			total += len(rendered)
			continue
		}
		total += 64
		if nested, ok := v.(map[string]any); ok {
			total += s.estimateSize(nested)
		}
	}
	return total
}

// firstNonEmpty returns the first non-empty value, or "".
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// PeerAddress returns the client IP for an HTTP request.
//
// X-Forwarded-For is consulted first because the service normally sits behind a
// load balancer, but only when the immediate peer is a private address; trusting
// the header from an arbitrary public peer would let any client forge its
// recorded address.
func PeerAddress(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" && isPrivatePeer(r.RemoteAddr) {
		// The left-most entry is the original client.
		if idx := strings.IndexByte(forwarded, ','); idx > 0 {
			return strings.TrimSpace(forwarded[:idx])
		}
		return strings.TrimSpace(forwarded)
	}

	if realIP := strings.TrimSpace(r.Header.Get("X-Real-Ip")); realIP != "" && isPrivatePeer(r.RemoteAddr) {
		return realIP
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isPrivatePeer reports whether addr is a loopback, link-local or RFC 1918
// address, i.e. plausibly a trusted proxy.
func isPrivatePeer(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}
