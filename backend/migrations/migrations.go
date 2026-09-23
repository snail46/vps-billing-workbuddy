// Package migrations embeds the versioned SQL migrations.
//
// The migrations are compiled into the binary rather than read from disk. This
// is what allows one migration mechanism to serve local development, CI and
// production (ADR-003): the migrate binary needs no volume mount, no external
// migration image, and cannot run a different set of migrations than the ones it
// was built with. A deployment's schema history is therefore pinned to the
// artefact it deploys, which is what "production only allows versioned
// migrations" (docs/17) requires in practice.
package migrations

import (
	"embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Dir is the path inside FS that holds the migration scripts.
const Dir = "."

// FS holds every .sql migration.
//
//go:embed *.sql
var FS embed.FS

// LatestVersion reports the highest version in the embedded history.
//
// The version a migrated database should report is a property of the migrations
// themselves, not a number to write down. It has been written down twice and was
// wrong both times: once in the CI assertion and once in the integration test that
// calls this, each failing when a phase added a migration and each failure looking
// like a migration defect rather than a stale expectation.
//
// CI derives the same value in scripts/expected-migration-version.sh, because that
// step runs before a Go toolchain is useful and has no access to this package. The
// duplication is deliberate and small, and both copies are covered: this one by
// migrations_test.go, and the script by scripts/check-migrations.py.
func LatestVersion() (uint, error) {
	entries, err := FS.ReadDir(Dir)
	if err != nil {
		return 0, fmt.Errorf("migrations: reading the embedded directory: %w", err)
	}

	var latest uint
	var found bool

	for _, entry := range entries {
		name := entry.Name()
		// Only up migrations define the version. Counting down migrations would
		// inflate it, and they exist in equal number.
		if entry.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}

		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return 0, fmt.Errorf("migrations: %q has no version prefix", name)
		}
		version, err := strconv.ParseUint(prefix, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("migrations: %q has a non-numeric version prefix %q", name, prefix)
		}

		if uint(version) > latest {
			latest = uint(version)
		}
		found = true
	}

	if !found {
		return 0, errors.New("migrations: no up migrations are embedded")
	}
	return latest, nil
}
