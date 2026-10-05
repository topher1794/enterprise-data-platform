// Package postgres owns the lifecycle of the control plane's PostgreSQL
// connection pool and exposes transaction helpers used by repositories.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/edp/edp-control-plane/internal/config"
)

// pgxPool aliases the driver's pool type. Embedding the alias rather than
// *pgxpool.Pool directly leaves the identifier `Pool` free for the method,
// while still promoting the pool's full API onto our wrapper.
type pgxPool = pgxpool.Pool

// Pool wraps the driver's connection pool so the rest of the codebase depends
// on a local type rather than the driver package directly. Its methods are the
// promoted pgxpool methods.
type Pool struct {
	*pgxPool
}

// DB exposes the underlying pool for repositories that need the raw handle.
type DB interface {
	Pool() *pgxpool.Pool
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
	Ping(ctx context.Context) error
	Close()
}

var _ DB = (*Pool)(nil)

// NewPool dials PostgreSQL, applies pool limits, verifies connectivity and
// returns a ready-to-use pool. The caller owns closing it.
func NewPool(ctx context.Context, cfg config.DatabaseConfig) (*Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("parse database configuration: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolCfg.HealthCheckPeriod = 30 * time.Second

	// Application name surfaces in pg_stat_activity, which makes it easy to
	// attribute load to this service during incident triage.
	poolCfg.ConnConfig.RuntimeParams["application_name"] = "edp-control-plane"

	// Fail fast with a bounded timeout rather than hanging on a bad host.
	dialCtx, cancel := context.WithTimeout(ctx, cfg.HealthTimeout)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(dialCtx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	if err := pool.Ping(dialCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database at %s: %w", cfg.RedactedDSN(), err)
	}

	return &Pool{pgxPool: pool}, nil
}

// Pool returns the underlying driver pool.
func (p *Pool) Pool() *pgxpool.Pool { return p.pgxPool }

// WithTx runs fn inside a transaction, committing on success, rolling back on
// error or panic, and mapping rollback/commit failures to wrapped errors.
// The ctx passed to fn carries the transaction so nested calls reuse it.
func (p *Pool) WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := p.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			// Rollback on the background context: the request context may
			// already be cancelled, which would otherwise mask the failure.
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

// IsNoRows reports whether err is pgx's empty-result sentinel.
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// IsUniqueViolation reports whether err is a PostgreSQL unique constraint
// violation (SQLSTATE 23505), optionally restricted to a named constraint.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}

// IsForeignKeyViolation reports whether err is a foreign key violation
// (SQLSTATE 23503).
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
