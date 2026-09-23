// Package migrate applies the embedded versioned migrations.
//
// It is a thin, explicit wrapper around golang-migrate so that the migration
// mechanism has one implementation shared by cmd/migrate, CI and the compose
// environment. Nothing else in the platform applies schema changes: the server
// never migrates on boot, because that would put schema mutation on the
// request-serving path and make a restart an implicit, unreviewed schema change.
package migrate

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	pgxdriver "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	// Registers the "pgx" database/sql driver used below. The golang-migrate
	// pgx driver also imports it, but relying on a transitive side effect for a
	// driver registration is the kind of fragility that breaks silently on a
	// dependency bump.
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/snail46/vps-billing-workbuddy/backend/migrations"
)

// driverName labels the database driver. The migration tooling uses it as an
// identifier only; the driver instance is passed explicitly.
const driverName = "pgx5"

// Runner owns an open migration session.
type Runner struct {
	migrator *migrate.Migrate
	db       *sql.DB
}

// Open connects and prepares the migrator.
//
// It fails if the database is unreachable, so a migration never reports success
// against a connection it could not establish.
func Open(databaseURL string) (*Runner, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("migrate: database URL is required")
	}

	source, err := iofs.New(migrations.FS, migrations.Dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: open embedded migration source: %w", err)
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		_ = source.Close()
		return nil, fmt.Errorf("migrate: open database: %w", err)
	}

	// An empty Config is intentional: the driver derives the database name, the
	// current schema and the schema_migrations table name from the connection.
	driver, err := pgxdriver.WithInstance(db, &pgxdriver.Config{})
	if err != nil {
		_ = db.Close()
		_ = source.Close()
		return nil, fmt.Errorf("migrate: create database driver: %w", err)
	}

	migrator, err := migrate.NewWithInstance("iofs", source, driverName, driver)
	if err != nil {
		_ = db.Close()
		_ = source.Close()
		return nil, fmt.Errorf("migrate: create migrator: %w", err)
	}

	return &Runner{migrator: migrator, db: db}, nil
}

// Close releases the migrator and the database handle.
//
// Cleanup errors are not returned: the migration outcome has already been
// reported, and a failure to close a connection on process exit must not be
// mistaken for a failure to migrate.
func (r *Runner) Close() {
	_, _ = r.migrator.Close()
	_ = r.db.Close()
}

// Up applies every pending migration. It is a no-op when the schema is current.
func (r *Runner) Up() error {
	if err := r.migrator.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate: apply up: %w", err)
	}
	return nil
}

// Down rolls back the given number of migrations.
//
// A step count is required rather than optional. A bare "down" would drop the
// entire schema, which is a destructive operation that must never be the
// accidental result of a mistyped command.
func (r *Runner) Down(steps int) error {
	if steps <= 0 {
		return fmt.Errorf("migrate: down requires a positive step count, got %d", steps)
	}
	if err := r.migrator.Steps(-steps); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate: apply down %d: %w", steps, err)
	}
	return nil
}

// Version reports the current schema version and whether it is dirty.
//
// A dirty schema means a migration failed part-way and requires operator
// intervention; it is surfaced rather than hidden so it cannot go unnoticed.
func (r *Runner) Version() (version uint, dirty bool, err error) {
	version, dirty, err = r.migrator.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, fmt.Errorf("migrate: read version: %w", err)
	}
	return version, dirty, nil
}

// Force sets the recorded version without running any migration.
//
// This exists solely to clear a dirty state after an operator has repaired the
// schema by hand. It is deliberately verbose in its naming and documented here
// so it is never used as a shortcut to skip a migration.
func (r *Runner) Force(version int) error {
	if err := r.migrator.Force(version); err != nil {
		return fmt.Errorf("migrate: force version %d: %w", version, err)
	}
	return nil
}
