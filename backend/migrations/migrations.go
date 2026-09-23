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

import "embed"

// Dir is the path inside FS that holds the migration scripts.
const Dir = "."

// FS holds every .sql migration.
//
//go:embed *.sql
var FS embed.FS
