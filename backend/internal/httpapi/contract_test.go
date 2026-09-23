package httpapi_test

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpapi"
)

// docs/08-API-CONTRACT.md requires the contract and the implementation to agree, and an
// OpenAPI document that is maintained by hand beside a router drifts in one direction only:
// routes get added and the document is not updated, because nothing fails when that happens.
//
// The check is therefore against the router that was actually assembled. It is one-directional
// on purpose — every served route must be described, while the document legitimately
// describes paths a later phase has not mounted yet. Saying the reverse today would fail on
// /orders and /instances, which is a roadmap rather than a defect.

// contractPath is the path of the contract document, relative to this package.
const contractPath = "../../../docs/openapi/openapi.yaml"

// documentPaths reads the path keys out of the contract document.
//
// A pattern rather than a YAML parser, because the one thing being read — the keys of the
// top-level `paths` mapping — has a fixed shape in this file, and adding a dependency to a
// test for it would be a dependency the whole module then carries.
func documentPaths(t *testing.T) map[string]struct{} {
	t.Helper()

	contents, err := os.ReadFile(filepath.FromSlash(contractPath))
	if err != nil {
		t.Fatalf("read the contract: %v", err)
	}

	// Path keys are indented by exactly two spaces and begin with a slash. Anything deeper
	// is a member of a path item, and anything shallower is a different top-level section.
	keys := regexp.MustCompile(`(?m)^  (/[A-Za-z0-9/{}_\-]*):`).FindAllStringSubmatch(string(contents), -1)

	paths := make(map[string]struct{}, len(keys))
	for _, match := range keys {
		paths[match[1]] = struct{}{}
	}

	// A guard against the test passing because the parse found nothing, which is what a
	// reformatted document would produce.
	if len(paths) < 10 {
		t.Fatalf("parsed %d paths from the contract; the parse is wrong or the document lost "+
			"paths", len(paths))
	}
	return paths
}

// servedRoutes walks the assembled router and returns every endpoint it holds.
func servedRoutes(t *testing.T, router chi.Routes) []string {
	t.Helper()

	var routes []string
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
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

func TestEveryServedRouteIsInTheContract(t *testing.T) {
	f := newAuthFixture(t)

	documented := documentPaths(t)
	served := servedRoutes(t, f.router)

	if len(served) == 0 {
		t.Fatal("the router serves nothing; the walk is not being exercised")
	}

	for _, route := range served {
		_, path, _ := strings.Cut(route, " ")

		// The document's server is the product API, so a product path is recorded without
		// its prefix while a root-mounted path — a probe — is recorded as it is served.
		key := strings.TrimPrefix(path, httpapi.BasePath)
		if _, ok := documented[key]; !ok {
			t.Errorf("%s is served but %s is not described in %s; the contract and the "+
				"implementation have to agree, and the document is the one that gets "+
				"forgotten", route, key, contractPath)
		}
	}
}

func TestTheContractDescribesTheAuthenticationSurface(t *testing.T) {
	documented := documentPaths(t)

	// The endpoints this phase implemented, named explicitly so that a rename in the
	// document is a failing test rather than a silently narrower contract.
	want := []string{
		"/auth/register",
		"/auth/login",
		"/auth/logout",
		"/auth/me",
		"/admin/auth/login",
		"/admin/auth/logout",
		"/admin/auth/me",
	}
	for _, path := range want {
		if _, ok := documented[path]; !ok {
			t.Errorf("%s is not in the contract", path)
		}
	}
}
