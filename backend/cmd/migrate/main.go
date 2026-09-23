// Command migrate applies the versioned database migrations.
//
// It is a separate binary by design (ADR-001, ADR-003): schema change is an
// explicit, auditable deployment step, never a side effect of the server
// starting. docs/17-DEPLOYMENT.md requires production to use versioned
// migrations only.
//
// Exit codes: 0 success, 1 migration failure, 2 usage error.
package main

import (
	"fmt"
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
  version            print the current schema version
  force <version>    record <version> without running migrations;
                     only for clearing a dirty state after manual repair
`

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

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

	logger, err := logging.New(cfg.LogLevel, cfg.LogFormat, os.Stdout)
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
	return reportVersion(runner, logger, "migrations applied")
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
	return reportVersion(runner, logger, "migrations rolled back")
}

func runVersion(runner *migrate.Runner, logger *slog.Logger) int {
	return reportVersion(runner, logger, "current schema version")
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

// reportVersion reads back the recorded version after a change, so the command's
// output states what the database actually contains rather than what was
// requested.
func reportVersion(runner *migrate.Runner, logger *slog.Logger, message string) int {
	version, dirty, err := runner.Version()
	if err != nil {
		logger.Error("cannot read schema version", slog.String("error", err.Error()))
		return exitFailure
	}

	if dirty {
		// A dirty schema means a migration failed part-way and requires operator
		// intervention. Reporting success here would hide a broken database.
		logger.Error("schema is in a dirty state and needs manual repair",
			slog.Uint64("version", uint64(version)))
		return exitFailure
	}

	logger.Info(message, slog.Uint64("version", uint64(version)))
	return exitOK
}
