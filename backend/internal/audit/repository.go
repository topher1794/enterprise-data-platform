package audit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/edp/edp-control-plane/internal/infrastructure/postgres"
	"github.com/edp/edp-control-plane/internal/platform"
)

// Repository is the persistence contract for the audit trail. It deliberately
// offers no update or delete: the table is append-only, enforced by a trigger.
type Repository interface {
	// Append writes one event.
	Append(ctx context.Context, e *Event) error
	// List returns a bounded page of events, newest first by default.
	List(ctx context.Context, q ListEventsQuery) (platform.PageResult[*Event], error)
	// GetByID returns a single event.
	GetByID(ctx context.Context, id uuid.UUID) (*Event, error)
	// CountSince returns how many events were recorded after a point in time,
	// used to lag-report on audit ingestion.
	CountSince(ctx context.Context, since time.Time) (int64, error)
}

// pgRepository is the PostgreSQL implementation of Repository.
type pgRepository struct {
	db *pgxpool.Pool
}

var _ Repository = (*pgRepository)(nil)

// NewRepository builds a PostgreSQL-backed audit repository.
func NewRepository(db postgres.DB) Repository {
	return &pgRepository{db: db.Pool()}
}

// eventColumns is the canonical SELECT list for audit events.
const eventColumns = `
	id, sequence_no, occurred_at,
	actor_id, actor_email, actor_roles, tenant_id,
	action, entity_type, entity_id, entity_label,
	change_set, outcome,
	host(inet), user_agent, request_id, trace_id, duration_ms`

func scanEvent(row pgx.Row) (*Event, error) {
	var (
		e             Event
		outcomeStr    string
		changeSetJSON []byte
	)

	err := row.Scan(
		&e.ID, &e.SequenceNo, &e.OccurredAt,
		&e.ActorID, &e.ActorEmail, &e.ActorRoles, &e.TenantID,
		&e.Action, &e.EntityType, &e.EntityID, &e.EntityName,
		&changeSetJSON, &outcomeStr,
		&e.IPAddress, &e.UserAgent, &e.RequestID, &e.TraceID, &e.DurationMs,
	)
	if err != nil {
		return nil, err
	}

	e.Outcome = Outcome(outcomeStr)
	e.ChangeSet = jsonMap(changeSetJSON)
	if e.ActorRoles == nil {
		e.ActorRoles = []string{}
	}
	return &e, nil
}

// Append writes one event, populating the generated sequence number.
func (r *pgRepository) Append(ctx context.Context, e *Event) error {
	changeSet, err := postgres.JSONB(e.ChangeSet)
	if err != nil {
		return fmt.Errorf("encode change_set: %w", err)
	}

	const query = `
		INSERT INTO audit_events (
			actor_id, actor_email, actor_roles, tenant_id,
			action, entity_type, entity_id, entity_label,
			change_set, outcome,
			ip_address, user_agent, request_id, trace_id, duration_ms
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10,
			NULLIF($11, '')::inet, $12, $13, $14, $15
		)
		RETURNING id, sequence_no, occurred_at`

	err = r.db.QueryRow(ctx, query,
		e.ActorID, e.ActorEmail, e.ActorRoles, e.TenantID,
		e.Action, e.EntityType, e.EntityID, e.EntityName,
		changeSet, string(e.Outcome),
		e.IPAddress, e.UserAgent, e.RequestID, e.TraceID, e.DurationMs,
	).Scan(&e.ID, &e.SequenceNo, &e.OccurredAt)

	if err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}

func (r *pgRepository) List(ctx context.Context, q ListEventsQuery) (platform.PageResult[*Event], error) {
	var (
		clauses = []string{"TRUE"}
		args    []any
	)

	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}

	if q.ActorID != "" {
		add("actor_id = $%d", q.ActorID)
	}
	if q.TenantID != "" {
		add("tenant_id = $%d", q.TenantID)
	}
	if q.Action != "" {
		add("action = $%d", q.Action)
	}
	if q.EntityType != "" {
		add("entity_type = $%d", q.EntityType)
	}
	if q.EntityID != "" {
		add("entity_id = $%d", q.EntityID)
	}
	if q.Outcome != "" {
		add("outcome = $%d", q.Outcome)
	}
	if q.Since != nil {
		add("occurred_at >= $%d", q.Since.UTC())
	}
	if q.Until != nil {
		add("occurred_at <= $%d", q.Until.UTC())
	}

	where := " WHERE " + strings.Join(clauses, " AND ")

	orderColumn := strings.TrimSpace(q.Sort.By)
	if orderColumn == "" {
		orderColumn = "occurred_at"
	}
	// sequence_no breaks ties on occurred_at so paging over a high-volume
	// window is stable rather than arbitrarily ordered.
	orderBy := fmt.Sprintf("%s %s, sequence_no %s", orderColumn, q.Sort.Direction(), q.Sort.Direction())

	var total int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM audit_events`+where, args...).Scan(&total); err != nil {
		return platform.PageResult[*Event]{}, fmt.Errorf("count audit events: %w", err)
	}
	if total == 0 {
		return platform.NewPageResult([]*Event{}, 0, q.Limit, q.Offset), nil
	}

	args = append(args, q.Limit, q.Offset)
	listQuery := fmt.Sprintf(
		`SELECT %s FROM audit_events%s ORDER BY %s LIMIT $%d OFFSET $%d`,
		eventColumns, where, orderBy, len(args)-1, len(args))

	rows, err := r.db.Query(ctx, listQuery, args...)
	if err != nil {
		return platform.PageResult[*Event]{}, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()

	items := make([]*Event, 0, q.Limit)
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return platform.PageResult[*Event]{}, fmt.Errorf("scan audit event: %w", err)
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		return platform.PageResult[*Event]{}, fmt.Errorf("iterate audit event rows: %w", err)
	}

	return platform.NewPageResult(items, total, q.Limit, q.Offset), nil
}

func (r *pgRepository) GetByID(ctx context.Context, id uuid.UUID) (*Event, error) {
	query := `SELECT ` + eventColumns + ` FROM audit_events WHERE id = $1`

	e, err := scanEvent(r.db.QueryRow(ctx, query, id))
	if err != nil {
		return nil, fmt.Errorf("select audit event: %w", err)
	}
	return e, nil
}

func (r *pgRepository) CountSince(ctx context.Context, since time.Time) (int64, error) {
	const query = `SELECT count(*) FROM audit_events WHERE occurred_at >= $1`

	var n int64
	if err := r.db.QueryRow(ctx, query, since.UTC()).Scan(&n); err != nil {
		return 0, fmt.Errorf("count audit events since: %w", err)
	}
	return n, nil
}
