package postgres

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	// Registers the "postgres" database driver with golang-migrate.
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	// Registers the "iofs" source so migrations can be embedded.
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Runner applies and rolls back schema migrations.
type Runner struct {
	migrate *migrate.Migrate
}

// NewRunner binds a migration source rooted at migrationsFS to the given
// database. migrationsFS must contain files named
// {version}_{title}.up.sql and {version}_{title}.down.sql.
func NewRunner(migrationsFS fs.FS, dsn string) (*Runner, error) {
	src, err := iofs.New(migrationsFS, ".")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return nil, fmt.Errorf("initialise migrator: %w", err)
	}

	return &Runner{migrate: m}, nil
}

// Migrate applies all pending migrations. It is a no-op when the schema is
// already up to date.
func (r *Runner) Migrate() error {
	if err := r.migrate.Up(); err != nil {
		if errors.Is(err, migrate.ErrNoChange) {
			slog.Info("database schema already up to date")
			return nil
		}
		return fmt.Errorf("apply migrations: %w", err)
	}

	version, dirty, err := r.migrate.Version()
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if dirty {
		return fmt.Errorf("schema version %d is dirty; resolve manually before restarting", version)
	}

	slog.Info("database migrations applied", "version", version)
	return nil
}

// Rollback steps back exactly one migration.
func (r *Runner) Rollback() error {
	if err := r.migrate.Steps(-1); err != nil {
		if errors.Is(err, migrate.ErrNoChange) {
			slog.Info("no migration to roll back")
			return nil
		}
		return fmt.Errorf("roll back migration: %w", err)
	}
	return nil
}

// Version reports the current schema version and dirty flag.
func (r *Runner) Version() (uint, bool, error) {
	v, dirty, err := r.migrate.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, fmt.Errorf("read schema version: %w", err)
	}
	return v, dirty, nil
}

// Close releases the migrator's database handle.
func (r *Runner) Close() error {
	sourceErr, dbErr := r.migrate.Close()
	return errors.Join(sourceErr, dbErr)
}
