// Package integration holds tests that need a real PostgreSQL instance.
//
// They are gated on EDP_TEST_DATABASE_URL so `go test ./...` stays green on a
// machine without a database. In CI, point that variable at a service container:
//
//	EDP_TEST_DATABASE_URL='postgres://edp:edp@localhost:5432/edp_test?sslmode=disable' go test ./tests/...
//
// The first migration creates extensions, so the role needs superuser or the
// extensions must already be installed.
package integration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/edp/edp-control-plane/internal/infrastructure/postgres"
	"github.com/edp/edp-control-plane/migrations"
)

// schemaTables are the tables every fully migrated database must contain.
var schemaTables = []string{
	"sources",
	"datasets",
	"pipelines",
	"pipeline_tasks",
	"pipeline_dependencies",
	"quality_rules",
	"quality_check_runs",
	"governance_policies",
	"data_contracts",
	"lineage_edges",
	"pipeline_runs",
	"task_runs",
	"audit_events",
}

// finalVersion is the migration version a fully applied schema should report.
const finalVersion = 9

// testDSN returns the DSN for integration tests, skipping when none is set.
func testDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("EDP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("EDP_TEST_DATABASE_URL is not set; skipping database integration test")
	}
	return dsn
}

// newRunner builds a migration runner against the test database.
func newRunner(t *testing.T, dsn string) *postgres.Runner {
	t.Helper()

	runner, err := postgres.NewRunner(migrations.FS, dsn)
	if err != nil {
		t.Fatalf("build migration runner: %v", err)
	}
	t.Cleanup(func() {
		if err := runner.Close(); err != nil {
			t.Errorf("close migration runner: %v", err)
		}
	})
	return runner
}

// openDB connects with database/sql for schema introspection.
func openDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping database: %v", err)
	}
	return db
}

// tableExists reports whether a table is visible in the public schema.
func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	var found bool
	err := db.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		                WHERE table_schema = 'public' AND table_name = $1)`,
		name,
	).Scan(&found)
	if err != nil {
		t.Fatalf("check for table %q: %v", name, err)
	}
	return found
}

// TestMigrationsApplyAndRollBack is the highest-value test in the repository: it
// is the only thing that proves the SQL is valid and that the up and down files
// actually pair up. Nothing else exercises the schema.
func TestMigrationsApplyAndRollBack(t *testing.T) {
	dsn := testDSN(t)
	runner := newRunner(t, dsn)
	db := openDB(t, dsn)

	// Start from a known state regardless of what the database held before.
	if err := runner.Migrate(); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if err := rollbackAll(t, runner); err != nil {
		t.Fatalf("reset to version 0: %v", err)
	}

	t.Run("applies every migration", func(t *testing.T) {
		if err := runner.Migrate(); err != nil {
			t.Fatalf("migrate up: %v", err)
		}

		version, dirty, err := runner.Version()
		if err != nil {
			t.Fatalf("read version: %v", err)
		}
		if dirty {
			t.Fatal("schema is dirty after a clean migration run")
		}
		if version != finalVersion {
			t.Fatalf("expected version %d, got %d", finalVersion, version)
		}
	})

	t.Run("creates every domain table", func(t *testing.T) {
		for _, table := range schemaTables {
			if !tableExists(t, db, table) {
				t.Errorf("expected table %q to exist after migrating", table)
			}
		}
	})

	t.Run("is idempotent", func(t *testing.T) {
		// Applying an already-current schema must be a no-op rather than an
		// error, or a repeated deployment breaks.
		if err := runner.Migrate(); err != nil {
			t.Fatalf("second migrate up: %v", err)
		}
	})

	t.Run("rolls back to zero", func(t *testing.T) {
		if err := rollbackAll(t, runner); err != nil {
			t.Fatalf("rollback: %v", err)
		}

		version, dirty, err := runner.Version()
		if err != nil {
			t.Fatalf("read version: %v", err)
		}
		if dirty {
			t.Fatal("schema is dirty after rolling back")
		}
		// Runner.Version reports version 0 rather than an error when the schema
		// is empty, because it swallows golang-migrate's ErrNilVersion.
		if version != 0 {
			t.Fatalf("expected version 0 after rolling back, got %d", version)
		}
	})

	t.Run("drops every domain table", func(t *testing.T) {
		for _, table := range schemaTables {
			if tableExists(t, db, table) {
				t.Errorf("expected table %q to be dropped after rolling back", table)
			}
		}
	})

	t.Run("reapplies cleanly", func(t *testing.T) {
		if err := runner.Migrate(); err != nil {
			t.Fatalf("migrate up after rollback: %v", err)
		}
	})
}

// TestMigrationsReachEveryVersionOneAtATime proves each migration is
// independently reversible. A down file that does not undo its up file only
// shows up when the steps are exercised individually.
func TestMigrationsReachEveryVersionOneAtATime(t *testing.T) {
	dsn := testDSN(t)
	runner := newRunner(t, dsn)

	if err := runner.Migrate(); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if err := rollbackAll(t, runner); err != nil {
		t.Fatalf("reset to version 0: %v", err)
	}

	for target := uint(1); target <= finalVersion; target++ {
		if err := runner.Migrate(); err != nil {
			t.Fatalf("apply migration %d: %v", target, err)
		}

		version, dirty, err := runner.Version()
		if err != nil {
			t.Fatalf("read version: %v", err)
		}
		if dirty || version != target {
			t.Fatalf("expected clean version %d, got %d (dirty=%t)", target, version, dirty)
		}

		if err := runner.Rollback(); err != nil {
			t.Fatalf("roll back migration %d: %v", target, err)
		}

		version, _, err = runner.Version()
		if err != nil {
			t.Fatalf("read version after rollback: %v", err)
		}
		if version != target-1 {
			t.Fatalf("expected version %d after rolling back, got %d", target-1, version)
		}
	}

	// Leave the database fully migrated for whatever runs next.
	if err := runner.Migrate(); err != nil {
		t.Fatalf("final migrate up: %v", err)
	}
}

// rollbackAll steps down until the schema is empty.
func rollbackAll(t *testing.T, runner *postgres.Runner) error {
	t.Helper()

	for {
		version, dirty, err := runner.Version()
		if err != nil {
			return fmt.Errorf("read version: %w", err)
		}
		if dirty {
			return fmt.Errorf("schema is dirty at version %d; cannot roll back", version)
		}
		if version == 0 {
			return nil
		}
		if err := runner.Rollback(); err != nil {
			return fmt.Errorf("roll back from version %d: %w", version, err)
		}
	}
}
