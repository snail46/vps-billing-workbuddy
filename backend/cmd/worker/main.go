// Command worker runs the asynchronous process.
//
// It owns everything that must not happen inside a request: consuming the
// outbox, executing Workflows, running Operations and reconciling desired
// against observed state. docs/02 and AGENTS.md require long operations to be
// Operation/Workflow/Worker driven rather than synchronous HTTP.
//
// Phase 0 establishes the process, its dependency connections and its shutdown
// behaviour. No scheduled work exists yet: the outbox arrives with the commerce
// phase. Running the real lifecycle now means the container, its configuration
// and its deployment wiring are proven before any queue exists, rather than
// being debugged for the first time under business load.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/config"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/db"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/logging"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/redisx"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/version"
)

// dependencyCheckTimeout bounds a single idle dependency probe.
const dependencyCheckTimeout = 5 * time.Second

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "startup failed: %v\n", err)
		return 1
	}

	logger, err := logging.New(cfg.LogLevel, cfg.LogFormat, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "startup failed: %v\n", err)
		return 1
	}
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runWorker(ctx, cfg, logger); err != nil {
		logger.ErrorContext(ctx, "worker stopped with error", slog.String("error", err.Error()))
		return 1
	}
	return 0
}

func runWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	logger.InfoContext(ctx, "worker starting",
		slog.String("env", cfg.AppEnv),
		slog.Duration("tick_interval", cfg.WorkerTickInterval),
		slog.String("version", version.Version),
		slog.String("commit", version.Commit),
	)

	pool, err := db.NewPool(ctx, db.Options{
		URL:               cfg.DatabaseURL,
		MaxConns:          cfg.DBMaxConns,
		MinConns:          cfg.DBMinConns,
		ConnectTimeout:    cfg.DBConnectTimeout,
		MaxConnLifetime:   cfg.DBMaxConnLifetime,
		MaxConnIdleTime:   cfg.DBMaxConnIdleTime,
		HealthCheckPeriod: cfg.DBHealthCheckPeriod,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	redisClient, err := redisx.NewClient(ctx, redisx.Options{
		URL:          cfg.RedisURL,
		DialTimeout:  cfg.RedisDialTimeout,
		ReadTimeout:  cfg.RedisReadTimeout,
		WriteTimeout: cfg.RedisWriteTimeout,
	})
	if err != nil {
		return err
	}
	defer func() { _ = redisClient.Close() }()

	logger.InfoContext(ctx, "worker ready")

	ticker := time.NewTicker(cfg.WorkerTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.InfoContext(ctx, "worker shutdown signal received, stopping")
			return nil

		case <-ticker.C:
			// A dependency outage is logged and retried on the next tick rather
			// than terminating the process. docs/18-TEST-PLAN.md exercises Redis
			// restarts and worker crashes, so the worker has to tolerate a
			// temporary loss of its backing services instead of requiring an
			// operator to restart it.
			probeCtx, cancel := context.WithTimeout(ctx, dependencyCheckTimeout)
			if err := pool.Ping(probeCtx); err != nil {
				logger.ErrorContext(probeCtx, "postgres unreachable",
					slog.String("error", err.Error()))
			}
			if err := redisClient.Ping(probeCtx).Err(); err != nil {
				logger.ErrorContext(probeCtx, "redis unreachable",
					slog.String("error", err.Error()))
			}
			cancel()
		}
	}
}
