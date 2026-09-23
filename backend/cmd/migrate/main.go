// Command migrate applies the versioned database migrations.
//
// It is a separate binary by design (ADR-001, ADR-003): schema change is an
// explicit, auditable deployment step, never a side effect of the server
// starting. docs/17-DEPLOYMENT.md requires production to use versioned
// migrations only.
//
// Streams are separated deliberately:
//
//	stdout  the command's result — the schema version, one number per line
//	stderr  diagnostics, as structured log records
//
// A caller must be able to read the result with a plain command substitution,
// independently of LOG_LEVEL and of the configured log format. Mixing the two on
// stdout would make the output unparseable, and deriving the result from a log
// record at INFO level would make it disappear the moment logging is quietened.
//
// Exit codes: 0 success, 1 migration failure, 2 usage error.
package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/config"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/logging"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/migrate"
)

const usage = `usage: migrate <command> [arguments]

commands:
  up                 apply all pending migrations
  down <steps>       roll back <steps> migrations (a count is mandatory)
  version            print the current schema version to stdout
  force <version>    record <version> without running migrations;
                     only for clearing a dirty state after manual repair
`

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// schemaVersionSource is the part of the migration runner that reporting
// depends on. Depending on the narrow interface rather than on *migrate.Runner
// keeps the result-reporting rules unit-testable without a database.
type schemaVersionSource interface {
	Version() (version uint, dirty bool, err error)
}

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		return exitUsage
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "startup failed: %v\n", err)
		return exitFailure
	}

	// Diagnostics go to stderr so that stdout carries only the result.
	logger, err := logging.New(cfg.LogLevel, cfg.LogFormat, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "startup failed: %v\n", err)
		return exitFailure
	}
	slog.SetDefault(logger)

	runner, err := migrate.Open(cfg.DatabaseURL)
	if err != nil {
		logger.Error("cannot open migration session", slog.String("error", err.Error()))
		return exitFailure
	}
	defer runner.Close()

	command := os.Args[1]
	args := os.Args[2:]

	switch command {
	case "up":
		return runUp(runner, logger)
	case "down":
		return runDown(runner, logger, args)
	case "version":
		return runVersion(runner, logger)
	case "force":
		return runForce(runner, logger, args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", command, usage)
		return exitUsage
	}
}

func runUp(runner *migrate.Runner, logger *slog.Logger) int {
	if err := runner.Up(); err != nil {
		logger.Error("migration up failed", slog.String("error", err.Error()))
		return exitFailure
	}
	return reportVersion(runner, os.Stdout, logger, "migrations applied")
}

func runDown(runner *migrate.Runner, logger *slog.Logger, args []string) int {
	if len(args) < 1 {
		// A bare "down" would drop the whole schema; requiring an explicit count
		// keeps a destructive operation from being a typo away.
		fmt.Fprintf(os.Stderr, "down requires a step count\n\n%s", usage)
		return exitUsage
	}

	steps, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid step count %q: %v\n", args[0], err)
		return exitUsage
	}

	if err := runner.Down(steps); err != nil {
		logger.Error("migration down failed", slog.String("error", err.Error()))
		return exitFailure
	}
	return reportVersion(runner, os.Stdout, logger, "migrations rolled back")
}

func runVersion(runner *migrate.Runner, logger *slog.Logger) int {
	return reportVersion(runner, os.Stdout, logger, "current schema version")
}

func runForce(runner *migrate.Runner, logger *slog.Logger, args []string) int {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "force requires a version\n\n%s", usage)
		return exitUsage
	}

	version, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid version %q: %v\n", args[0], err)
		return exitUsage
	}

	if err := runner.Force(version); err != nil {
		logger.Error("migration force failed", slog.String("error", err.Error()))
		return exitFailure
	}
	logger.Warn("schema version forced without running a migration",
		slog.Int("version", version))
	return exitOK
}

// reportVersion writes the recorded schema version to the result stream and
// returns the exit code for a migration change.
//
// The version is written to the result stream rather than only logged, so a
// caller never has to parse a log record to learn it — the failure mode that made
// a CI assertion break the moment logging was quietened to warn. A dirty schema
// is reported through the exit code as well as the log, because it requires
// operator intervention and must not be mistaken for success.
func reportVersion(source schemaVersionSource, out io.Writer, logger *slog.Logger, message string) int {
	version, dirty, err := source.Version()
	if err != nil {
		logger.Error("cannot read schema version", slog.String("error", err.Error()))
		return exitFailure
	}

	// Exactly one number, so `version="$(migrate version)"` yields it directly.
	fmt.Fprintf(out, "%d\n", version)

	if dirty {
		logger.Error("schema is in a dirty state and needs manual repair",
			slog.Uint64("version", uint64(version)))
		return exitFailure
	}

	logger.Info(message, slog.Uint64("version", uint64(version)))
	return exitOK
}
