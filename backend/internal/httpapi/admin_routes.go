package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// AdminPrefix is where the administrative surface is mounted.
//
// It is exported rather than private because the registry keys its declarations on the full
// path and a test compares those keys against the paths chi reports for the assembled
// router. A prefix written twice would eventually be written two different ways, and the
// test would fail for a reason that had nothing to do with authorisation.
const AdminPrefix = BasePath + "/admin"

// Requirement markers.
//
// A route's declaration is either a permission key from docs/15 or one of these. They
// are not permission keys, because they are not permissions: requiring the sign-in
// endpoint to hold one would make it unreachable, and a permission meaning "any
// authenticated administrator" would be a key in the seed that grants nothing.
const (
	requirementPublic        = "public"
	requirementAuthenticated = "authenticated"
)

// adminRoutes mounts the administrative surface, recording what each endpoint requires.
//
// Every endpoint is registered through one of the methods below and each names its
// requirement. That is the point of the type. With plain router.Get/router.Post, "does
// this admin endpoint check a permission" is answered by reading the handler — and the
// endpoint that forgot is the one nobody reads. Here the answer is given where the route
// is declared, and TestEveryAdminRouteDeclaresItsRequirement walks the assembled router to
// confirm that nothing was mounted without going through this type.
//
// There is no `guarded` method yet because Phase 1 owns no permission-gated endpoint: the
// surface so far is sign-in, sign-out and the caller's own profile. The first guarded
// endpoint arrives with the phase that owns it, and it registers here rather than on the
// router directly.
type adminRoutes struct {
	api *api
	// requirements maps "METHOD /full/path" to its declaration.
	requirements map[string]string
}

func newAdminRoutes(api *api) *adminRoutes {
	return &adminRoutes{api: api, requirements: map[string]string{}}
}

// mountOn installs the endpoints on the administrative sub-router.
//
// The endpoints are declared here rather than by the caller so that the router cannot
// mount an administrative route by some other route: whoever assembles the surface calls
// this, and everything it installs has a recorded requirement by construction.
func (ar *adminRoutes) mountOn(mux chi.Router) {
	ar.public(mux, http.MethodPost, "/auth/login", ar.api.loginAdmin)
	ar.authenticated(mux, http.MethodPost, "/auth/logout", ar.api.logoutAdmin)
	ar.authenticated(mux, http.MethodGet, "/auth/me", ar.api.meAdmin)

	// Terminating a customer's subscription is the platform's hand, not a
	// session's own business: it needs the permission the seed gives, and the
	// registration below is what states that requirement where the route lives.
	ar.guarded(mux, http.MethodPost, "/subscriptions/{subscriptionID}/terminate",
		"subscriptions.terminate", ar.api.terminateSubscription)

	// The infrastructure surface is read-only (ADR-007 §6): the scheduler and the
	// workflows write it, and the operator reads it here.
	ar.guarded(mux, http.MethodGet, "/providers", "providers.read", ar.api.listProviders)
	ar.guarded(mux, http.MethodGet, "/node-groups", "nodes.read", ar.api.listNodeGroups)
	ar.guarded(mux, http.MethodGet, "/nodes", "nodes.read", ar.api.listNodes)

	// The operation system's readers: the record and its stream.
	ar.guarded(mux, http.MethodGet, "/operations/{operationID}", "operations.read", ar.api.getOperation)
	ar.guarded(mux, http.MethodGet, "/operations/events", "operations.read", ar.api.streamEvents)

	// The operator's console (ADR-012). Each screen names its permission where
	// the route lives; a screen that cannot be granted cannot be reached.
	if ar.api.admin != nil {
		ar.guarded(mux, http.MethodGet, "/overview", "operations.read", ar.api.adminOverview)
		ar.guarded(mux, http.MethodGet, "/operations", "operations.read", ar.api.adminListOperations)

		ar.guarded(mux, http.MethodGet, "/users", "users.read", ar.api.adminListUsers)
		ar.guarded(mux, http.MethodGet, "/users/{userID}", "users.read", ar.api.adminGetUser)
		ar.guarded(mux, http.MethodPost, "/users/{userID}/suspend", "users.suspend", ar.api.adminSetUserStatus("suspended"))
		ar.guarded(mux, http.MethodPost, "/users/{userID}/activate", "users.suspend", ar.api.adminSetUserStatus("active"))

		ar.guarded(mux, http.MethodGet, "/products", "products.read", ar.api.adminListProducts)

		ar.guarded(mux, http.MethodGet, "/orders", "orders.read", ar.api.adminListOrders)
		ar.guarded(mux, http.MethodGet, "/orders/{orderID}", "orders.read", ar.api.adminGetOrder)
		ar.guarded(mux, http.MethodGet, "/payments", "payments.read", ar.api.adminListPayments)
		ar.guarded(mux, http.MethodGet, "/ledger", "ledger.read", ar.api.adminLedger)

		ar.guarded(mux, http.MethodGet, "/subscriptions", "subscriptions.read", ar.api.adminListSubscriptions)

		ar.guarded(mux, http.MethodGet, "/instances", "instances.read", ar.api.adminListInstances)
		ar.guarded(mux, http.MethodGet, "/instances/{instanceID}", "instances.read", ar.api.adminGetInstance)

		ar.guarded(mux, http.MethodGet, "/tickets", "tickets.read", ar.api.adminListTickets)
		ar.guarded(mux, http.MethodGet, "/tickets/{ticketID}", "tickets.read", ar.api.adminGetTicket)
		ar.guarded(mux, http.MethodPost, "/tickets/{ticketID}/messages", "tickets.reply", ar.api.adminReplyTicket)
		ar.guarded(mux, http.MethodPost, "/tickets/{ticketID}/close", "tickets.manage", ar.api.adminCloseTicket)

		ar.guarded(mux, http.MethodGet, "/audit", "audit.read", ar.api.adminListAudit)

		ar.guarded(mux, http.MethodGet, "/admins", "admins.manage", ar.api.adminListAdmins)
		ar.guarded(mux, http.MethodGet, "/roles", "roles.manage", ar.api.adminListRoles)
		ar.guarded(mux, http.MethodGet, "/settings", "settings.manage", ar.api.adminListSettings)
	}
}

// public registers an endpoint reachable without a session.
//
// Only the sign-in endpoint qualifies: it is how an administrator obtains a session, so
// it cannot require one. It is throttled, because it is the one endpoint here that can be
// attacked by someone who has nothing.
func (ar *adminRoutes) public(mux chi.Router, method, path string, handler http.HandlerFunc) {
	ar.mount(mux, method, path, requirementPublic, []func(http.Handler) http.Handler{
		ar.api.auth.AdminLogin.Middleware,
	}, handler)
}

// authenticated registers an endpoint that requires an administrator session but no
// particular permission.
//
// It is for endpoints about the session itself — reading one's own profile, ending one's
// own session — where a permission would have to be invented, and would answer a question
// nobody is asking.
func (ar *adminRoutes) authenticated(mux chi.Router, method, path string, handler http.HandlerFunc) {
	ar.mount(mux, method, path, requirementAuthenticated, []func(http.Handler) http.Handler{
		ar.api.auth.Sessions.RequireAdmin,
		ar.api.auth.Sessions.RequireCSRF,
	}, handler)
}

// guarded registers an endpoint that requires an administrator holding a specific
// permission. The permission is declared here, as the literal the seed holds, so
// the test that reads the seed can confirm the key exists and the test that walks
// the router can confirm the route declared it.
func (ar *adminRoutes) guarded(mux chi.Router, method, path, permission string, handler http.HandlerFunc) {
	ar.mount(mux, method, path, permission, []func(http.Handler) http.Handler{
		ar.api.auth.Sessions.RequireAdmin,
		ar.api.auth.Sessions.RequireCSRF,
		ar.api.auth.Sessions.RequirePermission(permission),
	}, handler)
}

func (ar *adminRoutes) mount(mux chi.Router, method, path, requirement string, chain []func(http.Handler) http.Handler, handler http.HandlerFunc) {
	ar.requirements[method+" "+AdminPrefix+path] = requirement
	mux.With(chain...).Method(method, path, handler)
}
