// Package httpapi assembles the HTTP surface.
//
// docs/02-ARCHITECTURE.md fixes the dependency direction as
//
//	HTTP Handler -> Application Service -> Domain -> Repository/Provider
//
// so a handler must never reach a database or a Provider directly. Phase 0
// establishes the middleware chain, the probe endpoints and the error contract;
// business routes are added by the phase that owns them, through a router that
// receives its already-constructed application services.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/config"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/health"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	bmw "github.com/snail46/vps-billing-workbuddy/backend/internal/middleware"
)

// BasePath is the prefix of the product API, per docs/08-API-CONTRACT.md.
const BasePath = "/api/v1"

// Deps are the collaborators the router needs.
type Deps struct {
	Config config.Config
	Logger *slog.Logger
	Health *health.Handler
}

// NewRouter builds the HTTP handler.
//
// Middleware order is load-bearing and the reasoning is recorded here so it is
// not silently rearranged:
//
//  1. RequestID / TraceID — establish correlation before anything can log.
//  2. Observability — wrap the response writer and own the access log record.
//     Everything after this point can be observed, including a panic.
//  3. Recover — inside Observability so a recovered panic is still logged as a
//     500 rather than escaping the access log.
//  4. Timeout — innermost of the timing middleware so a timeout is reported
//     through the same envelope and is recorded by the access log.
//  5. SecurityHeaders / CORS — cheap header work, applied to every response
//     including errors.
//
// Note the deliberate absence of a client-IP middleware; the reasoning is
// recorded inline below.
func NewRouter(deps Deps) http.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	r := chi.NewRouter()

	r.Use(bmw.RequestID)
	r.Use(bmw.TraceID)
	// Forwarded-IP headers are deliberately NOT trusted here.
	//
	// chi's middleware.RealIP is deprecated for good reason: it rewrites
	// RemoteAddr from X-Forwarded-For / X-Real-IP / True-Client-IP whether or not
	// the deployment actually sets them, so any client can spoof the address that
	// access logs, audit records and rate limiting depend on (GHSA-3fxj-6jh8-hvhx,
	// GHSA-rjr7-jggh-pgcp, GHSA-9g5q-2w5x-hmxf). Trusting those headers is only
	// safe when the exact proxy topology is known, so it is introduced together
	// with the reverse proxy in Phase 12 and configured with the proxy's address
	// range. Until then the direct peer address in RemoteAddr is the truth.
	r.Use(bmw.Observability(logger))
	r.Use(bmw.Recover(logger))
	r.Use(bmw.Timeout(deps.Config.HTTPRequestTimeout, logger))
	r.Use(bmw.SecurityHeaders(bmw.SecurityHeadersOptions{
		// HSTS only where TLS terminates, per the note on the option.
		EnableHSTS: deps.Config.IsProduction(),
	}))
	r.Use(bmw.CORS(deps.Config.AllowedOrigins()))

	api := &api{logger: logger}

	// Router-level handlers cover paths that match no route at all, so the
	// envelope holds even for a malformed URL.
	r.NotFound(api.notFound)
	r.MethodNotAllowed(api.methodNotAllowed)

	if deps.Health != nil {
		r.Get("/health/live", deps.Health.Live())
		r.Get("/health/ready", deps.Health.Ready())
	}

	r.Route(BasePath, func(v1 chi.Router) {
		// Phase 0 intentionally exposes no business routes. Registering a
		// placeholder endpoint would mean shipping an API that the roadmap has
		// not specified yet, and the surface is added per phase behind this
		// sub-router so that the middleware chain and the envelope are already
		// proven by the foundation.
		v1.NotFound(api.notFound)
		v1.MethodNotAllowed(api.methodNotAllowed)
	})

	return r
}

// api carries the collaborators shared by the contract-level handlers.
type api struct {
	logger *slog.Logger
}

func (a *api) notFound(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
}

func (a *api) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, a.logger, httpx.ErrMethodNotAllowed())
}
