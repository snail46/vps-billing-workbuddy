package migrate_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

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

	// The test's whole subject is dropping and re-applying schema, and go test runs
	// every package's binary at once against this same shared database. Rolling the
	// commercial tables out from under a package that is reading rows mid-test does
	// not fail here — it fails there, as a row that vanishes between its insert and
	// its lookup. A database of this test's own keeps the blast radius its own.
	probe := provisionProbeDatabase(t, url)

	runner, err := migrate.Open(probe)
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

// provisionProbeDatabase creates a throwaway database beside the shared one and
// returns a connection URL for it.
//
// The connection URL is required to be the postgres:// form, which is what CI and
// the compose file both set. Creating a database needs CREATEDB, which the compose
// service's user holds because it owns the cluster.
func provisionProbeDatabase(t *testing.T, shared string) string {
	t.Helper()

	parsed, err := url.Parse(shared)
	if err != nil || parsed.Scheme == "" || parsed.Path == "" {
		t.Fatalf("TEST_DATABASE_URL is not the postgres:// form this test provisions against: %q", shared)
	}

	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("name the probe database: %v", err)
	}
	name := "migrate_probe_" + hex.EncodeToString(buf)

	admin := *parsed
	admin.Path = "/postgres"
	conn, err := pgx.Connect(context.Background(), admin.String())
	if err != nil {
		t.Fatalf("connect to the server to provision the probe database: %v", err)
	}

	// A leftover from a run that crashed between the create and the drop would make
	// every later run fail at the create, so the name is cleared first. FORCE ends any
	// connections a crashed run left behind (PostgreSQL 13+).
	_, _ = conn.Exec(context.Background(), `DROP DATABASE IF EXISTS `+pgx.Identifier{name}.Sanitize()+` WITH (FORCE)`)
	if _, err := conn.Exec(context.Background(), `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = conn.Close(context.Background())
		t.Fatalf("create the probe database: %v", err)
	}

	probe := *parsed
	probe.Path = "/" + name
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP DATABASE IF EXISTS `+pgx.Identifier{name}.Sanitize()+` WITH (FORCE)`)
		_ = conn.Close(context.Background())
	})
	return probe.String()
}
