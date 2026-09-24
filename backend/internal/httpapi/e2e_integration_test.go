package httpapi_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/commerce"
	sqlcgen "github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/health"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpapi"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/operation"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/outbox"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/payment/fakegateway"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider/mockprovider"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provision"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/redisx"
	commercestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/commerce"
	identitystore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/identity"
	infrastore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/infra"
	instancestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/instance"
	operationstore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/operation"
)

// The end-to-end identity test: the assembled router, the real identity service, PostgreSQL
// and Redis, exercised through HTTP.
//
// Everything else about authentication is covered against fakes, and those are the tests that
// localise a defect. This one exists for what a fake cannot establish at all: that the
// statements are valid SQL, that a session survives a round trip through Redis, that the cookie
// a sign-in returns is accepted by the next request, that a suspension takes effect
// immediately, and that the audit row is really in the table. It is gated on both
// TEST_DATABASE_URL and TEST_REDIS_URL and so runs only in CI (ADR-003).

// e2eEnv is the real environment, or a skipped test.
type e2eEnv struct {
	router   *httpapi.Handler
	pool     *pgxpool.Pool
	hasher   *identity.PasswordHasher
	password string
	// gateway is the same instance the router verifies callbacks with, so a test can
	// build one that is indistinguishable from a real delivery.
	gateway *fakegateway.Fake
	// engine and publisher are the worker's halves, driven in-process.
	engine    *operation.Engine
	publisher *outbox.Publisher
	instances *instancestore.Store
	infra     *infrastore.Store
}

func newE2E(t *testing.T) *e2eEnv {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	redisURL := os.Getenv("TEST_REDIS_URL")
	if databaseURL == "" || redisURL == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL are both required; integration test skipped")
	}

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("postgres is not reachable: %v", err)
	}

	redisClient, err := redisx.NewClient(ctx, redisx.Options{URL: redisURL, DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("connect to redis: %v", err)
	}
	t.Cleanup(func() { _ = redisClient.Close() })

	// Cheap argon2 parameters: this file is about the wiring, and the production cost is
	// covered by the password tests.
	hasher, err := identity.NewPasswordHasher(identity.PasswordParams{
		Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("build hasher: %v", err)
	}

	fake := fakegateway.New(testGatewaySecret)

	commerceService, err := commerce.NewService(commerce.Deps{
		Store:    commercestore.New(pool),
		Gateways: commerce.Gateways{fake.Name(): fake},
	})
	if err != nil {
		t.Fatalf("build the commerce service: %v", err)
	}

	service, err := identity.NewService(identity.Deps{
		Directory:   identitystore.NewDirectory(pool),
		Permissions: identitystore.NewDirectory(pool),
		Sessions:    redisx.NewSessionStore(redisClient),
		Hasher:      hasher,
		Auditor:     identitystore.NewRecorder(pool),
	})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}

	cfg := testConfig()
	sessions := authmw.New(service, testLogger(), authmw.SessionCookie{Secure: cfg.SecureCookies()})
	attempts := func(scope authmw.Scope, perIP, perAccount int) authmw.Attempts {
		return authmw.Attempts{
			// The real Redis counter, so the throttle is proven against the store it uses.
			Counter: redisx.NewAttemptCounter(redisClient),
			Logger:  testLogger(), Scope: scope,
			Window: cfg.RateLimitWindow, PerIP: perIP, PerAccount: perAccount,
		}
	}

	gateways := commerce.Gateways{
		fake.Name(): fake,
	}

	// The provision machinery (ADR-009): the engine and the outbox publisher
	// the test drives in-process, the way the worker does, so the Gate's
	// journey runs against the real stores.
	opStore := operationstore.New(pool)
	infraStore := infrastore.New(pool)
	instancesStore := instancestore.New(pool)
	engine := operation.NewEngine(opStore, infraStore, testLogger(), operation.DefaultMaxRetries)
	publisher := outbox.New(sqlcgen.New(pool), testLogger())
	mockProvider := mockprovider.New("mock")
	provisionRunner := provision.NewRunner(provision.Deps{
		Subscriptions: commerceService,
		Nodes:         infraStore,
		Instances:     instancesStore,
		Providers:     map[string]provider.Provider{mockProvider.Name(): mockProvider},
		Outbox:        commercestore.New(pool),
		Logger:        testLogger(),
	})
	if err := engine.Register(provision.OperationType, provisionRunner); err != nil {
		t.Fatalf("register the provision runner: %v", err)
	}
	if err := publisher.Register(commerce.EventSubscriptionActivated,
		provision.HandleSubscriptionActivated(provision.BridgeDeps{Engine: engine})); err != nil {
		t.Fatalf("register the provision bridge: %v", err)
	}

	return &e2eEnv{
		gateway:   fake,
		pool:      pool,
		engine:    engine,
		publisher: publisher,
		instances: instancesStore,
		infra:     infraStore,
		hasher:    hasher,
		password:  "correct horse battery staple",
		router: httpapi.NewRouter(httpapi.Deps{
			Config: cfg,
			Logger: testLogger(),
			Health: health.NewHandler(health.Options{Environment: "test", Logger: testLogger()}),
			Auth: &httpapi.Auth{
				Service:    service,
				Sessions:   sessions,
				Login:      attempts(authmw.ScopeLogin, cfg.RateLimitLoginPerIP, cfg.RateLimitLoginPerAccount),
				AdminLogin: attempts(authmw.ScopeAdminLogin, cfg.RateLimitLoginPerIP, cfg.RateLimitLoginPerAccount),
				Register:   attempts(authmw.ScopeRegister, cfg.RateLimitRegisterPerIP, 0),
			},
			Commerce: &httpapi.CommerceDeps{
				Service:  commerceService,
				Gateways: gateways,
			},
			Instances: &httpapi.InstanceDeps{Store: instancesStore},
		}),
	}
}

// do performs a request against the real stack.
//
// The source address is fixed, so the audit assertion has a value to compare and every request
// in a test is charged to the same address budget — which is what a browser would do.
func (e *e2eEnv) do(t *testing.T, method, path, body string, cookie *http.Cookie, token string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if token != "" {
		req.Header.Set(authmw.CSRFHeaderName, token)
	}
	req.RemoteAddr = "203.0.113.7:4321"

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// e2eAddress returns a fresh address in mixed case.
//
// Mixed case on purpose, and it is load-bearing rather than cosmetic. The service normalises
// what it stores, so every request in these tests sends an address that differs from the
// stored one, and signing in with it as given proves the lookup normalises too. That is the
// whole reason a plain UNIQUE constraint on the column is sufficient. The domain is upper
// case so the two forms always differ, whatever the random part happens to contain, which
// makes the assertion below deterministic rather than usually true.
func e2eAddress() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf) + "@Example.COM"
}

func TestIdentityEndToEnd(t *testing.T) {
	e := newE2E(t)
	ctx := context.Background()

	// Sent in mixed case, stored normalised. Every lookup in this test uses the normalised
	// form, and the sign-in requests use the mixed-case one, so both directions are exercised.
	address := e2eAddress()
	normalized := identity.NormalizeEmail(address)
	if normalized == address {
		t.Fatal("the fixture address is already normalised; it would prove nothing")
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", normalized)
	})

	// ---- register ---------------------------------------------------------------
	rec := e.do(t, http.MethodPost, "/api/v1/auth/register",
		`{"email":"`+address+`","password":"`+e.password+`","locale":"en-US","timezone":"UTC"}`, nil, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d (%s)", rec.Code, rec.Body.String())
	}

	var storedEmail, storedLocale string
	if err := e.pool.QueryRow(ctx,
		"SELECT email, locale FROM users WHERE email = $1", normalized).Scan(&storedEmail, &storedLocale); err != nil {
		t.Fatalf("read the created user: %v", err)
	}
	// Stored trimmed and lower-cased, which is what makes the plain UNIQUE constraint on the
	// column sufficient. A row stored as sent would be an account its owner could not reach,
	// because the sign-in path normalises the address it looks up.
	if storedEmail != normalized {
		t.Errorf("stored email = %q, expected %q", storedEmail, normalized)
	}
	if storedLocale != "en-US" {
		t.Errorf("locale = %q", storedLocale)
	}

	// Registering the same address again is a conflict, and that comes from a real SQLSTATE
	// being mapped rather than from a synthetic one. Sent in the opposite case, so this also
	// proves the conflict check normalises.
	if rec := e.do(t, http.MethodPost, "/api/v1/auth/register",
		`{"email":"`+strings.ToUpper(address)+`","password":"`+e.password+`"}`, nil, ""); rec.Code != http.StatusConflict {
		t.Errorf("a duplicate registration in different case returned %d (%s)", rec.Code, rec.Body.String())
	}

	// ---- sign in, with the address as the person would type it -------------------
	login := e.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"`+address+`","password":"`+e.password+`"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login: %d (%s)", login.Code, login.Body.String())
	}
	cookie := sessionCookie(login, authmw.UserCookieName)
	if cookie == nil || cookie.Value == "" {
		t.Fatal("no session cookie")
	}
	token := decodeEnvelope(t, login).Data.CSRFToken
	if token == "" {
		t.Fatal("no CSRF token")
	}

	// ---- the session survives a round trip through Redis -------------------------
	me := e.do(t, http.MethodGet, "/api/v1/auth/me", "", cookie, "")
	if me.Code != http.StatusOK {
		t.Fatalf("me: %d (%s)", me.Code, me.Body.String())
	}
	// Reported as stored, which is normalised: a client renders what the platform holds
	// rather than echoing back what it sent.
	if got := decodeEnvelope(t, me).Data.User.Email; got != normalized {
		t.Errorf("me returned %q, expected %q", got, normalized)
	}

	// ---- a suspension takes effect on the next request --------------------------
	if _, err := e.pool.Exec(ctx, "UPDATE users SET status = 'suspended' WHERE email = $1", normalized); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if rec := e.do(t, http.MethodGet, "/api/v1/auth/me", "", cookie, ""); rec.Code != http.StatusForbidden {
		t.Errorf("a suspended account was allowed through on an existing session: %d (%s)",
			rec.Code, rec.Body.String())
	}
	// And signing in again is refused too, with the message that says why.
	if rec := e.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"`+address+`","password":"`+e.password+`"}`, nil, ""); rec.Code != http.StatusForbidden {
		t.Errorf("a suspended account signed in: %d (%s)", rec.Code, rec.Body.String())
	}
	// Reactivated, so the rest of the test is about the session rather than the status.
	if _, err := e.pool.Exec(ctx, "UPDATE users SET status = 'active' WHERE email = $1", normalized); err != nil {
		t.Fatalf("reactivate: %v", err)
	}

	// ---- logout ends the session in the store, not only in the browser ----------
	logout := e.do(t, http.MethodPost, "/api/v1/auth/logout", "", cookie, token)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout: %d (%s)", logout.Code, logout.Body.String())
	}
	// The same cookie value, presented again. The browser was told to forget it; the store
	// has to have forgotten it too, or the credential stays live for anyone who kept a copy.
	if rec := e.do(t, http.MethodGet, "/api/v1/auth/me", "", cookie, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("the session survived logout: %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestAdminSignInEndToEndIsAudited(t *testing.T) {
	e := newE2E(t)
	ctx := context.Background()

	address := e2eAddress()
	// The administrators table carries the same normalisation constraint as users, so the
	// row has to be written the way the service writes one. The sign-in below uses the
	// mixed-case form, which is what proves the administrative path normalises too.
	normalized := identity.NormalizeEmail(address)
	adminID := uuid.New()

	// Written as SQL because no administrative creation path exists yet — that arrives with
	// the phase that owns admins management — and this file is not the place to invent one.
	encoded, err := e.hasher.Hash(e.password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := e.pool.Exec(ctx,
		`INSERT INTO admins (id, email, password_hash, status, display_name)
		 VALUES ($1, $2, $3, 'active', 'E2E Administrator')`,
		adminID, normalized, encoded); err != nil {
		t.Fatalf("insert administrator: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), "DELETE FROM admin_roles WHERE admin_id = $1", adminID)
		_, _ = e.pool.Exec(context.Background(), "DELETE FROM admins WHERE id = $1", adminID)
		_, _ = e.pool.Exec(context.Background(), "DELETE FROM audit_events WHERE actor_id = $1", adminID)
	})

	tag, err := e.pool.Exec(ctx,
		`INSERT INTO admin_roles (admin_id, role_id)
		 SELECT $1, r.id FROM roles r WHERE r.key = 'super_admin'`, adminID)
	if err != nil {
		t.Fatalf("grant role: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatal("the super_admin role does not exist; the RBAC seed did not run")
	}

	login := e.do(t, http.MethodPost, "/api/v1/admin/auth/login",
		`{"email":"`+address+`","password":"`+e.password+`"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("admin login: %d (%s)", login.Code, login.Body.String())
	}

	// The permissions come from the seeded grants through a real join over three tables,
	// which is the part a fake cannot check.
	body := decodeEnvelope(t, login)
	if len(body.Data.Admin.Permissions) < 20 {
		t.Errorf("super_admin resolved to %d permissions: %v",
			len(body.Data.Admin.Permissions), body.Data.Admin.Permissions)
	}

	adminCookie := sessionCookie(login, authmw.AdminCookieName)
	if adminCookie == nil || adminCookie.Value == "" {
		t.Fatal("no admin session cookie")
	}

	if rec := e.do(t, http.MethodGet, "/api/v1/admin/auth/me", "", adminCookie, ""); rec.Code != http.StatusOK {
		t.Fatalf("admin me: %d (%s)", rec.Code, rec.Body.String())
	}
	// The user cookie name does not authenticate an administrative request. The value here is
	// a live administrator session, so the refusal is about the cookie rather than about the
	// session being unknown.
	if rec := e.do(t, http.MethodGet, "/api/v1/admin/auth/me", "",
		&http.Cookie{Name: authmw.UserCookieName, Value: adminCookie.Value}, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("an administrator session in the user cookie authenticated an admin endpoint: %d", rec.Code)
	}

	// The sign-in really landed in the table, with the actor named and the source recorded.
	var (
		action    string
		actorType string
		ip        *string
	)
	if err := e.pool.QueryRow(ctx,
		`SELECT action, actor_type, host(ip_address) FROM audit_events
		 WHERE actor_id = $1 AND action = $2
		 ORDER BY created_at DESC LIMIT 1`,
		adminID, identity.ActionAdminLoggedIn).Scan(&action, &actorType, &ip); err != nil {
		t.Fatalf("no audit entry was written for the sign-in: %v", err)
	}
	if actorType != "admin" {
		t.Errorf("actor_type = %q", actorType)
	}
	// The request's own address, recorded as a column so an operator can filter on it.
	if ip == nil || *ip != "203.0.113.7" {
		t.Errorf("ip_address = %v, expected the request's source address", ip)
	}
}

func TestSignInsAreThrottledThroughRedis(t *testing.T) {
	e := newE2E(t)

	address := e2eAddress()
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", address)
	})

	if rec := e.do(t, http.MethodPost, "/api/v1/auth/register",
		`{"email":"`+address+`","password":"`+e.password+`"}`, nil, ""); rec.Code != http.StatusCreated {
		t.Fatalf("register: %d (%s)", rec.Code, rec.Body.String())
	}

	// The fixture allows three attempts per account. The counter is in Redis, so this also
	// proves that the middleware's transaction applies a window at all.
	for i := 1; i <= 3; i++ {
		if rec := e.do(t, http.MethodPost, "/api/v1/auth/login",
			`{"email":"`+address+`","password":"not the password"}`, nil, ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d returned %d, expected 401 (%s)", i, rec.Code, rec.Body.String())
		}
	}

	throttled := e.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"`+address+`","password":"not the password"}`, nil, "")
	if throttled.Code != http.StatusTooManyRequests {
		t.Fatalf("the budget was not enforced against Redis: %d (%s)",
			throttled.Code, throttled.Body.String())
	}
	if retry := throttled.Header().Get("Retry-After"); retry == "" {
		t.Error("no Retry-After header")
	}
}
