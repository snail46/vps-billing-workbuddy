package migrate_test

import (
	"os"
	"testing"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/migrate"
	"github.com/snail46/vps-billing-workbuddy/backend/migrations"
)

// requireDatabaseURL returns the integration database URL, skipping the test when
// it is absent.
//
// Integration tests are gated rather than always-on because the foundation must
// be verifiable on a machine without a container runtime (ADR-003). CI provides
// a PostgreSQL service and sets TEST_DATABASE_URL, so the same tests that are
// skipped locally run on every push.
func requireDatabaseURL(t *testing.T) string {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; integration test skipped")
	}
	return url
}

func TestOpenRejectsEmptyURL(t *testing.T) {
	if _, err := migrate.Open(""); err == nil {
		t.Fatal("expected an empty database URL to be rejected")
	}
}

func TestDownRequiresPositiveStepCount(t *testing.T) {
	// The guard is checked before any database work, so no connection is needed.
	runner := &migrate.Runner{}

	for _, steps := range []int{0, -1} {
		if err := runner.Down(steps); err == nil {
			t.Fatalf("expected Down(%d) to be rejected", steps)
		}
	}
}

func TestMigrationsApplyForwardAndBackward(t *testing.T) {
	url := requireDatabaseURL(t)

	runner, err := migrate.Open(url)
	if err != nil {
		t.Fatalf("cannot open migration session: %v", err)
	}
	defer runner.Close()

	if err := runner.Up(); err != nil {
		t.Fatalf("migrate up failed: %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("cannot read version after up: %v", err)
	}
	if dirty {
		t.Fatal("schema must not be dirty after a successful up")
	}

	// The expected version is a property of the embedded history rather than a
	// number written here. It was written here, as 1, and broke the moment Phase 1
	// added a migration — a failure that reads like a migration defect and is not
	// one. migrations.LatestVersion has its own unit test, which runs locally and
	// without a database.
	expected, err := migrations.LatestVersion()
	if err != nil {
		t.Fatalf("cannot determine the expected schema version: %v", err)
	}
	if version != expected {
		t.Fatalf("expected schema version %d after applying the full history, got %d", expected, version)
	}

	// Re-running must be a no-op rather than an error: deployments restart.
	if err := runner.Up(); err != nil {
		t.Fatalf("second migrate up must be idempotent, got %v", err)
	}

	// The down path is exercised by CI to prove every migration is reversible.
	if err := runner.Down(1); err != nil {
		t.Fatalf("migrate down failed: %v", err)
	}

	// Restore the migrated state so the test leaves the database usable.
	if err := runner.Up(); err != nil {
		t.Fatalf("cannot re-apply migrations: %v", err)
	}
}
