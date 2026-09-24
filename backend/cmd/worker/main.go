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

	"github.com/snail46/vps-billing-workbuddy/backend/internal/commerce"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/config"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/db"
	sqlcgen "github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/logging"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/operation"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/outbox"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider/mockprovider"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provision"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/redisx"
	commercestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/commerce"
	infrastore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/infra"
	instancestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/instance"
	operationstore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/operation"
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

	// The operation system (ADR-008): the database is the queue, and this
	// process is one of the workers that drains it.
	opStore := operationstore.New(pool)
	infraStore := infrastore.New(pool)
	engine := operation.NewEngine(opStore, infraStore, logger, operation.DefaultMaxRetries)
	publisher := outbox.New(sqlcgen.New(pool), logger)

	// The provision workflow (ADR-009): the subscription's activation event
	// starts its operation, and the runner walks docs/07's chain against the
	// mock provider until the direct one arrives in Phase 7.
	commerceStore := commercestore.New(pool)
	gateway := mockprovider.New("mock")
	commerceSvc, err := commerce.NewService(commerce.Deps{
		Store: commerceStore,
	})
	if err != nil {
		return fmt.Errorf("build the commerce service: %w", err)
	}
	provisionRunner := provision.NewRunner(provision.Deps{
		Subscriptions: commerceSvc,
		Nodes:         infraStore,
		Instances:     instancestore.New(pool),
		Providers:     map[string]provider.Provider{gateway.Name(): gateway},
		Outbox:        commerceStore,
		Logger:        logger,
	})
	if err := engine.Register(provision.OperationType, provisionRunner); err != nil {
		return fmt.Errorf("register the provision runner: %w", err)
	}
	if err := publisher.Register(commerce.EventSubscriptionActivated,
		provision.HandleSubscriptionActivated(provision.BridgeDeps{Engine: engine})); err != nil {
		return fmt.Errorf("register the provision bridge: %w", err)
	}

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

			workCtx, workCancel := context.WithTimeout(ctx, dependencyCheckTimeout)
			claimed, err := engine.Tick(workCtx)
			if err != nil {
				logger.ErrorContext(workCtx, "the operation tick failed",
					slog.String("error", err.Error()))
			} else if claimed {
				logger.InfoContext(workCtx, "an operation was claimed and executed")
			}
			delivered, err := publisher.Deliver(workCtx)
			if err != nil {
				logger.ErrorContext(workCtx, "the outbox tick failed",
					slog.String("error", err.Error()))
			} else if delivered > 0 {
				logger.InfoContext(workCtx, "outbox events delivered",
					slog.Int("count", delivered))
			}
			if released, err := infraStore.SweepExpiredReservations(workCtx, time.Now().UTC()); err != nil {
				logger.ErrorContext(workCtx, "the reservation sweep failed",
					slog.String("error", err.Error()))
			} else if released > 0 {
				logger.InfoContext(workCtx, "expired reservations released",
					slog.Int("count", released))
			}
			workCancel()
		}
	}
}
