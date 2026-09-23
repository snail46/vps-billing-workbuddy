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

	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/config"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/health"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
	bmw "github.com/snail46/vps-billing-workbuddy/backend/internal/middleware"
)

// BasePath is the prefix of the product API, per docs/08-API-CONTRACT.md.
const BasePath = "/api/v1"

// Auth is the identity surface the router mounts.
//
// It is a pointer field so that a build without an identity service — Phase 0, and any
// test that exercises only the foundation — mounts no authentication routes at all. The
// alternative, mounting routes that would fail on every request, would be a surface that
// looks present and is not.
type Auth struct {
	Service  *identity.Service
	Sessions *authmw.Middleware
	// Login and Register carry the throttling policy. They are separate instances
	// because their budgets genuinely differ: see the note on Attempts.PerAccount.
	Login      authmw.Attempts
	AdminLogin authmw.Attempts
	Register   authmw.Attempts
}

// Deps are the collaborators the router needs.
type Deps struct {
	Config config.Config
	Logger *slog.Logger
	Health *health.Handler
	Auth   *Auth
	// Commerce is a pointer for the same reason Auth is: a build without a commerce
	// service mounts no commercial routes rather than ones that would fail.
	Commerce *CommerceDeps
}

// Handler is the assembled HTTP surface.
//
// It embeds chi.Router rather than http.Handler so that a test can enumerate what is
// mounted — the route registry test compares its declarations against the routes chi
// actually holds, and a test cannot do that through an http.Handler. Embedding the router
// keeps that honest: there is one router, not a router and a copy of its route table.
type Handler struct {
	chi.Router
	// requirements maps "METHOD /path" to what the administrative endpoint declares it
	// needs.
	requirements map[string]string
}

// AdminRequirements reports what each administrative endpoint declares it requires.
//
// The map is copied, so a test cannot accidentally change what the router enforces.
func (h *Handler) AdminRequirements() map[string]string {
	out := make(map[string]string, len(h.requirements))
	for path, requirement := range h.requirements {
		out[path] = requirement
	}
	return out
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
func NewRouter(deps Deps) *Handler {
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

	api := &api{logger: logger, auth: deps.Auth, commerce: deps.Commerce}

	// Router-level handlers cover paths that match no route at all, so the
	// envelope holds even for a malformed URL.
	r.NotFound(api.notFound)
	r.MethodNotAllowed(api.methodNotAllowed)

	if deps.Health != nil {
		r.Get("/health/live", deps.Health.Live())
		r.Get("/health/ready", deps.Health.Ready())
	}

	requirements := map[string]string{}

	r.Route(BasePath, func(v1 chi.Router) {
		v1.NotFound(api.notFound)
		v1.MethodNotAllowed(api.methodNotAllowed)

		// Each phase mounts its own routes behind this sub-router, so the middleware
		// chain and the envelope are shared rather than rebuilt per phase.
		if deps.Auth != nil {
			requirements = mountAuth(v1, api)
		}
		if deps.Commerce != nil {
			mountCommerce(v1, api)
		}
	})

	return &Handler{Router: r, requirements: requirements}
}

// mountAuth installs the authentication surface.
//
// A route's middleware is given at registration rather than on the group it belongs to,
// because the throttling budgets differ per endpoint: registration counts attempts per
// client address only, while sign-in counts them per address and per submitted account.
func mountAuth(v1 chi.Router, api *api) map[string]string {
	sessions := api.auth.Sessions

	mount(v1, http.MethodPost, "/auth/register", api.registerUser,
		api.auth.Register.Middleware)
	mount(v1, http.MethodPost, "/auth/login", api.loginUser,
		api.auth.Login.Middleware)
	mount(v1, http.MethodPost, "/auth/logout", api.logoutUser,
		sessions.RequireUser, sessions.RequireCSRF)
	mount(v1, http.MethodGet, "/auth/me", api.meUser,
		sessions.RequireUser)

	admin := newAdminRoutes(api)
	v1.Route("/admin", func(ar chi.Router) {
		admin.mountOn(ar)
	})

	return admin.requirements
}

// mountCommerce installs the commercial surface.
//
// The catalogue is public, because it is what a customer reads before they have an
// account. Everything that commits money is behind the customer's own session and CSRF
// token, and the one route a gateway calls is authenticated by the gateway's signature
// instead — which is why it is mounted here rather than under the admin prefix: it is
// not an administrative act, and holding it to a session would mean holding a gateway
// to a session it cannot have.
func mountCommerce(v1 chi.Router, api *api) {
	sessions := api.auth.Sessions

	// The catalogue.
	mount(v1, http.MethodGet, "/products", api.listCatalog)

	// Orders, always scoped to the session's own customer.
	mount(v1, http.MethodPost, "/orders", api.createOrder,
		sessions.RequireUser, sessions.RequireCSRF)
	mount(v1, http.MethodGet, "/orders", api.listOrders,
		sessions.RequireUser)
	mount(v1, http.MethodGet, "/orders/{orderID}", api.getOrder,
		sessions.RequireUser)
	mount(v1, http.MethodPost, "/orders/{orderID}/payments", api.startPayment,
		sessions.RequireUser, sessions.RequireCSRF)

	// One route for every gateway, named by the URL parameter the handler reads. The
	// gateways map is still what validates the name at request time — an unknown one
	// is refused there — so the table does not have to be rebuilt per deployment.
	mount(v1, http.MethodPost, "/webhooks/payments/{gateway}", api.paymentWebhook)
}

// mount registers a handler behind the given middleware.
func mount(r chi.Router, method, pattern string, handler http.HandlerFunc, chain ...func(http.Handler) http.Handler) {
	r.With(chain...).Method(method, pattern, handler)
}

// api carries the collaborators shared by the contract-level handlers.
type api struct {
	logger   *slog.Logger
	auth     *Auth
	commerce *CommerceDeps
}

func (a *api) notFound(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
}

func (a *api) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, a.logger, httpx.ErrMethodNotAllowed())
}
