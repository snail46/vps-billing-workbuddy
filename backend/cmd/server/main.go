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

	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/commerce"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/config"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/db"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/health"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpapi"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/logging"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/payment/fakegateway"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/redisx"
	commercestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/commerce"
	identitystore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/identity"
	instancestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/instance"
	operationstore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/operation"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/storage/usersurface"
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

	// The identity collaborator graph is assembled here, in the process that serves
	// HTTP, rather than inside the router. The router receives an already-built service,
	// which is what keeps it a description of the surface rather than a place where
	// dependencies are chosen.
	//
	// The directory and the audit recorder are both built over the pool. A future phase
	// that has to write a change and its audit entry in one transaction builds both from
	// the transaction instead; that is why the constructors take sqlcgen.DBTX rather than
	// a pool.
	hasher, err := identity.NewPasswordHasher(identity.DefaultPasswordParams())
	if err != nil {
		return err
	}
	identityService, err := identity.NewService(identity.Deps{
		Directory:   identitystore.NewDirectory(pool),
		Permissions: identitystore.NewDirectory(pool),
		Sessions:    redisx.NewSessionStore(redisClient),
		Hasher:      hasher,
		Auditor:     identitystore.NewRecorder(pool),
	})
	if err != nil {
		return err
	}

	// The commercial collaborator graph, assembled for the same reason the identity one
	// is: the router receives built services rather than choosing dependencies.
	//
	// The store is bound to the pool, and every write the service makes goes through
	// WithinTransaction, which rebinds the store and the ledger poster to one
	// transaction. That is where docs/04's "Payment + Order + Ledger + Outbox" lives.
	commerceStore := commercestore.New(pool)
	fakeGateway := fakegateway.New(cfg.PaymentFakeSecret)
	commerceService, err := commerce.NewService(commerce.Deps{
		Store: commerceStore,
		Gateways: commerce.Gateways{
			fakeGateway.Name(): fakeGateway,
		},
	})
	if err != nil {
		return err
	}

	sessions := authmw.New(identityService, logger, authmw.SessionCookie{Secure: cfg.SecureCookies()})
	attempts := func(scope authmw.Scope, perIP, perAccount int) authmw.Attempts {
		return authmw.Attempts{
			Counter:    redisx.NewAttemptCounter(redisClient),
			Logger:     logger,
			Scope:      scope,
			Window:     cfg.RateLimitWindow,
			PerIP:      perIP,
			PerAccount: perAccount,
		}
	}

	router := httpapi.NewRouter(httpapi.Deps{
		Config: cfg,
		Logger: logger,
		// The customer's instance surface and their own surface (ADR-011): the
		// reads are store-backed, the actions write queued operations the
		// worker's registered runners execute.
		Instances:  &httpapi.InstanceDeps{Store: instancestore.New(pool)},
		Operations: operationstore.New(pool),
		User: &httpapi.UserDeps{
			Store:      usersurface.New(pool),
			Operations: operationstore.New(pool),
			Instances:  instancestore.New(pool),
		},
		Auth: &httpapi.Auth{
			Service:  identityService,
			Sessions: sessions,
			// Registration is counted per client address only: the submitted address
			// usually has no account yet, so counting it would let anyone lock out the
			// person who owns it without there being a secret to guess in the first place.
			Login:      attempts(authmw.ScopeLogin, cfg.RateLimitLoginPerIP, cfg.RateLimitLoginPerAccount),
			AdminLogin: attempts(authmw.ScopeAdminLogin, cfg.RateLimitLoginPerIP, cfg.RateLimitLoginPerAccount),
			Register:   attempts(authmw.ScopeRegister, cfg.RateLimitRegisterPerIP, 0),
		},
		Commerce: &httpapi.CommerceDeps{
			Service: commerceService,
			Gateways: commerce.Gateways{
				fakeGateway.Name(): fakeGateway,
			},
		},
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
