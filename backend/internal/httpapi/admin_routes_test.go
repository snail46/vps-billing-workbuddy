package httpapi_test

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpapi"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
	"github.com/snail46/vps-billing-workbuddy/backend/migrations"
)

// This is the test the route registry exists for. "Every administrative endpoint declares
// what it requires" is the kind of rule that holds at review time and quietly stops holding
// later, so it is checked against the router that was actually assembled rather than against
// a list maintained beside it.

// mountedAdminRoutes walks the assembled router and returns the administrative endpoints it
// really holds, as "METHOD /path".
func mountedAdminRoutes(t *testing.T, router chi.Routes) []string {
	t.Helper()

	var routes []string
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, httpapi.AdminPrefix) {
			return nil
		}
		// A mount point rather than an endpoint. chi reports the sub-router as a wildcard
		// pattern, and it has no method and no handler of its own to declare anything about.
		if strings.Contains(route, "*") || method == "" {
			return nil
		}
		routes = append(routes, method+" "+route)
		return nil
	})
	if err != nil {
		t.Fatalf("walk the router: %v", err)
	}
	sort.Strings(routes)
	return routes
}

func TestEveryAdminRouteDeclaresItsRequirement(t *testing.T) {
	f := newAuthFixture(t)

	mounted := mountedAdminRoutes(t, f.router)
	declared := f.router.AdminRequirements()

	// A guard against the test passing because nothing was mounted. If the fixture stopped
	// wiring the admin surface, every assertion below would hold vacuously.
	if len(mounted) == 0 {
		t.Fatal("no administrative endpoints are mounted; the registry is not being exercised")
	}

	for _, route := range mounted {
		if _, ok := declared[route]; !ok {
			t.Errorf("administrative endpoint %s is mounted without a recorded requirement; "+
				"it must be registered through adminRoutes so that what it needs is stated "+
				"where it is declared rather than left to be read out of the handler", route)
		}
	}
	for route := range declared {
		if !contains(mounted, route) {
			t.Errorf("%s is recorded as an administrative endpoint but is not mounted", route)
		}
	}
}

// contains reports whether the sorted slice holds the value.
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// seededPermissions reads the permission keys out of the RBAC seed migrations.
//
// Reading the migrations rather than a list in this file is the point: the keys a route may
// declare are the keys the database will actually hold, and a list maintained here would
// agree with the routes while disagreeing with the seed. Migrations, plural: 0003 seeded
// the original vocabulary, and a later phase's migration — 0005's subscriptions.terminate —
// adds keys the same way, so every up migration is read and each is allowed to contribute.
func seededPermissions(t *testing.T) map[string]struct{} {
	t.Helper()

	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatalf("list the migrations: %v", err)
	}

	// The permission keys are the quoted strings in the statement that inserts them. The
	// generated identifiers beside them are not quoted, so the two cannot be confused.
	statement := regexp.MustCompile(`(?s)INSERT INTO permissions\b[^;]*;`)
	quoted := regexp.MustCompile(`'([a-z_]+(?:\.[a-z_]+)+)'`)

	keys := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		contents, err := migrations.FS.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, insert := range statement.FindAll(contents, -1) {
			for _, match := range quoted.FindAllStringSubmatch(string(insert), -1) {
				keys[match[1]] = struct{}{}
			}
		}
	}
	return keys
}

func TestDeclaredAdminPermissionsExistInTheSeed(t *testing.T) {
	f := newAuthFixture(t)
	seeded := seededPermissions(t)

	// 27 keys today. The number is asserted so the test cannot pass on an empty parse,
	// which is what a changed migration format would produce.
	if len(seeded) < 27 {
		t.Fatalf("parsed %d permission keys from the seed; the parse is wrong or the seed "+
			"lost keys", len(seeded))
	}

	// Every permission a route declares must be one an administrator can actually hold.
	// A typo here is otherwise invisible until an administrator is refused something they
	// were granted, which is a support ticket rather than a failing build.
	declared := f.router.AdminRequirements()
	for route, requirement := range declared {
		if requirement == "public" || requirement == "authenticated" {
			continue
		}
		if _, ok := seeded[requirement]; !ok {
			t.Errorf("%s declares permission %q, which the RBAC seed does not grant to anyone",
				route, requirement)
		}
	}
}

func TestAdminSurfaceHasExactlyTheDeclaredEndpoints(t *testing.T) {
	f := newAuthFixture(t)
	declared := f.router.AdminRequirements()

	want := map[string]string{
		"POST /api/v1/admin/auth/login":  "public",
		"POST /api/v1/admin/auth/logout": "authenticated",
		"GET /api/v1/admin/auth/me":      "authenticated",
		// The first permission-gated endpoint: ending a customer's subscription is
		// the platform's hand, and the permission it needs is seeded (0005).
		"POST /api/v1/admin/subscriptions/{subscriptionID}/terminate": "subscriptions.terminate",
		// The infrastructure surface is read-only (0006, ADR-007 §6).
		"GET /api/v1/admin/providers":   "providers.read",
		"GET /api/v1/admin/node-groups": "nodes.read",
		"GET /api/v1/admin/nodes":       "nodes.read",
	}

	for route, requirement := range want {
		got, ok := declared[route]
		if !ok {
			t.Errorf("%s is not mounted", route)
			continue
		}
		if got != requirement {
			t.Errorf("%s requires %q, expected %q", route, got, requirement)
		}
	}
	if len(declared) != len(want) {
		t.Errorf("the administrative surface holds %d endpoints, expected %d: %v",
			len(declared), len(want), declared)
	}
}

func TestTheUserSurfaceIsNotMountedUnderTheAdminPrefix(t *testing.T) {
	f := newAuthFixture(t)

	// The two credential spaces have separate sign-in endpoints and separate cookies, and
	// the separation is what keeps an administrator's session from being reachable through
	// the customer surface. A user sign-in reaching an admin endpoint would collapse it.
	rec := f.do(t, http.MethodPost, "/api/v1/admin/auth/login",
		`{"email":"ada@example.com","password":"`+testPassword+`"}`)

	// The account exists, and the password is correct — for the user table. The admin
	// table has no such address, so the refusal is the same one an unknown address gets.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a user account signed in through the admin endpoint: %d (%s)", rec.Code, rec.Body.String())
	}
	if body := decodeEnvelope(t, rec); body.Error.MessageKey != "errors.invalid_credentials" {
		t.Errorf("message key = %q", body.Error.MessageKey)
	}
	if sessionCookie(rec, authmw.AdminCookieName) != nil {
		t.Error("an admin cookie was issued for a user account")
	}
}

// A compile-time assertion that the fake still satisfies everything the identity service
// requires. If an interface gains a method, this fails here rather than in every test.
var (
	_ identity.Directory          = (*identityFake)(nil)
	_ identity.PermissionResolver = (*identityFake)(nil)
	_ identity.SessionStore       = (*identityFake)(nil)
)
