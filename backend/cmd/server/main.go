// Command server runs the Business API process.
//
// It is the only process that serves HTTP. It never executes host commands and
// never calls a Provider directly (docs/02-ARCHITECTURE.md): infrastructure work
// is expressed as an Operation and performed by the worker through the Provider
// Contract.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/config"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/db"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/health"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpapi"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/logging"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/redisx"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/version"
)

func main() {
	os.Exit(run())
}

// run isolates startup so that every failure path returns an exit code instead
// of calling os.Exit from deep inside the call stack, where deferred cleanup
// would be skipped.
func run() int {
	cfg, err := config.Load()
	if err != nil {
		// The logger depends on the configuration that just failed to load, so
		// this is the one place a plain stderr write is correct.
		fmt.Fprintf(os.Stderr, "startup failed: %v\n", err)
		return 1
	}

	logger, err := logging.New(cfg.LogLevel, cfg.LogFormat, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "startup failed: %v\n", err)
		return 1
	}
	slog.SetDefault(logger)

	// NotifyContext cancels the context on SIGINT/SIGTERM, giving every
	// subsystem a single cancellation signal to observe.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := serve(ctx, cfg, logger); err != nil {
		logger.ErrorContext(ctx, "server stopped with error", slog.String("error", err.Error()))
		return 1
	}
	return 0
}

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	logger.InfoContext(ctx, "server starting",
		slog.String("env", cfg.AppEnv),
		slog.String("addr", cfg.HTTPAddr),
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

	router := httpapi.NewRouter(httpapi.Deps{
		Config: cfg,
		Logger: logger,
		Health: health.NewHandler(health.Options{
			Environment: cfg.AppEnv,
			// Only platform-critical dependencies are registered. A single
			// offline Provider must never make the platform unready (docs/17);
			// provider reachability is reported by its own admin surface.
			Checks: []health.Check{
				db.NewChecker(pool),
				redisx.NewChecker(redisClient),
			},
			Logger: logger,
		}),
	})

	srv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: router,
		// ReadHeaderTimeout is set explicitly: without it a slowloris client can
		// hold connections open indefinitely.
		ReadTimeout:       cfg.HTTPReadTimeout,
		ReadHeaderTimeout: cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	// Buffered so the goroutine cannot block if serve returns via the signal
	// path before the error is read.
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("http server: %w", err)
		}
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		logger.InfoContext(ctx, "shutdown signal received",
			slog.Duration("grace", cfg.HTTPShutdownTimeout))
	}

	// A fresh context is required: the parent is already cancelled, so reusing
	// it would make Shutdown give up immediately instead of draining.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTPShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	logger.InfoContext(shutdownCtx, "server stopped cleanly")
	return nil
}
