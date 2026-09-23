package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/audit"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/config"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/health"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpapi"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
)

// These tests exercise the authentication surface through the assembled router, so the
// middleware chain, the envelope and the handlers are proven together. Nothing here
// restates a rule that belongs to internal/identity: what is asserted is the transport —
// which status, which error code, which message key, which cookie.

const testPassword = "correct horse battery staple"

// identityFake is an in-memory identity backend.
//
// One type implements all four interfaces the service depends on — the directory, the
// permission resolver, the session store and the audit recorder — because in production
// they are built over the same database and splitting them here would only mean passing
// four pointers to the same thing.
type identityFake struct {
	users       map[string]identity.User
	usersByID   map[uuid.UUID]identity.User
	admins      map[string]identity.Admin
	adminsByID  map[uuid.UUID]identity.Admin
	permissions map[uuid.UUID][]string
	sessions    map[string]identity.Session
	audited     []audit.Event
}

func newIdentityFake() *identityFake {
	return &identityFake{
		users:       map[string]identity.User{},
		usersByID:   map[uuid.UUID]identity.User{},
		admins:      map[string]identity.Admin{},
		adminsByID:  map[uuid.UUID]identity.Admin{},
		permissions: map[uuid.UUID][]string{},
		sessions:    map[string]identity.Session{},
	}
}

func (f *identityFake) CreateUser(_ context.Context, user identity.User) (identity.User, error) {
	if _, exists := f.users[user.Email]; exists {
		return identity.User{}, identity.ErrEmailTaken
	}
	f.users[user.Email] = user
	f.usersByID[user.ID] = user
	return user, nil
}

func (f *identityFake) FindUserByEmail(_ context.Context, email string) (identity.User, error) {
	user, ok := f.users[email]
	if !ok {
		return identity.User{}, identity.ErrUserNotFound
	}
	return user, nil
}

func (f *identityFake) FindUserByID(_ context.Context, id uuid.UUID) (identity.User, error) {
	user, ok := f.usersByID[id]
	if !ok {
		return identity.User{}, identity.ErrUserNotFound
	}
	return user, nil
}

func (f *identityFake) RecordUserLogin(context.Context, uuid.UUID) error { return nil }

func (f *identityFake) UpdateUserPasswordHash(_ context.Context, id uuid.UUID, hash string) error {
	user, ok := f.usersByID[id]
	if !ok {
		return identity.ErrUserNotFound
	}
	user.PasswordHash = hash
	f.usersByID[id] = user
	f.users[user.Email] = user
	return nil
}

func (f *identityFake) FindAdminByEmail(_ context.Context, email string) (identity.Admin, error) {
	admin, ok := f.admins[email]
	if !ok {
		return identity.Admin{}, identity.ErrAdminNotFound
	}
	return admin, nil
}

func (f *identityFake) FindAdminByID(_ context.Context, id uuid.UUID) (identity.Admin, error) {
	admin, ok := f.adminsByID[id]
	if !ok {
		return identity.Admin{}, identity.ErrAdminNotFound
	}
	return admin, nil
}

func (f *identityFake) RecordAdminLogin(context.Context, uuid.UUID) error { return nil }

func (f *identityFake) UpdateAdminPasswordHash(_ context.Context, id uuid.UUID, hash string) error {
	admin, ok := f.adminsByID[id]
	if !ok {
		return identity.ErrAdminNotFound
	}
	admin.PasswordHash = hash
	f.adminsByID[id] = admin
	f.admins[admin.Email] = admin
	return nil
}

func (f *identityFake) PermissionsForAdmin(_ context.Context, adminID uuid.UUID) ([]string, error) {
	if set, ok := f.permissions[adminID]; ok {
		return set, nil
	}
	return []string{}, nil
}

func (f *identityFake) sessionKey(subject identity.SubjectType, id string) string {
	return string(subject) + ":" + id
}

func (f *identityFake) Create(_ context.Context, session identity.Session) error {
	f.sessions[f.sessionKey(session.Subject, session.ID)] = session
	return nil
}

func (f *identityFake) Get(_ context.Context, subject identity.SubjectType, id string) (identity.Session, error) {
	session, ok := f.sessions[f.sessionKey(subject, id)]
	if !ok {
		return identity.Session{}, identity.ErrSessionNotFound
	}
	return session, nil
}

func (f *identityFake) Delete(_ context.Context, subject identity.SubjectType, id string) error {
	delete(f.sessions, f.sessionKey(subject, id))
	return nil
}

func (f *identityFake) DeleteForSubject(_ context.Context, subject identity.SubjectType, subjectID uuid.UUID) error {
	for key, session := range f.sessions {
		if session.Subject == subject && session.SubjectID == subjectID {
			delete(f.sessions, key)
		}
	}
	return nil
}

func (f *identityFake) Record(_ context.Context, event audit.Event) error {
	f.audited = append(f.audited, event)
	return nil
}

// counterFake counts attempts in memory, so a test can exhaust a budget without Redis.
type counterFake struct{ counts map[string]int }

func (c *counterFake) Incr(_ context.Context, key string, _ time.Duration) (int64, error) {
	c.counts[key]++
	return int64(c.counts[key]), nil
}

func (c *counterFake) Reset(_ context.Context, keys ...string) error {
	for _, key := range keys {
		delete(c.counts, key)
	}
	return nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// testConfig is a configuration the identity surface will accept.
func testConfig() config.Config {
	return config.Config{
		AppEnv:                   config.EnvDevelopment,
		HTTPRequestTimeout:       5 * time.Second,
		UserWebOrigin:            "http://localhost:3000",
		AdminWebOrigin:           "http://localhost:3001",
		DatabaseURL:              "postgres://user:pass@localhost:5432/db?sslmode=disable",
		RedisURL:                 "redis://localhost:6379/0",
		DBMaxConns:               4,
		HTTPShutdownTimeout:      5 * time.Second,
		WorkerTickInterval:       30 * time.Second,
		RateLimitLoginPerIP:      10,
		RateLimitLoginPerAccount: 3,
		RateLimitRegisterPerIP:   5,
		RateLimitWindow:          15 * time.Minute,
	}
}

// authFixture is a router with the identity surface mounted over an in-memory backend.
type authFixture struct {
	router *httpapi.Handler
	store  *identityFake
	userID uuid.UUID
	// adminID is the administrator the fixture can sign in as.
	adminID uuid.UUID
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()

	// Cheap argon2 parameters. The default cost is what production uses and is covered by
	// the password tests; here it would only make every case slower.
	hasher, err := identity.NewPasswordHasher(identity.PasswordParams{
		Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("build hasher: %v", err)
	}
	hash, err := hasher.Hash(testPassword)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	userID := uuid.MustParse("0198f1c2-0000-7000-8000-0000000000a1")
	adminID := uuid.MustParse("0198f1c2-0000-7000-8000-0000000000b1")

	store := newIdentityFake()
	store.users["ada@example.com"] = identity.User{
		ID: userID, Email: "ada@example.com", PasswordHash: hash,
		Status: identity.StatusActive, Locale: "zh-CN", Timezone: "Asia/Shanghai",
	}
	store.usersByID[userID] = store.users["ada@example.com"]
	store.admins["root@example.com"] = identity.Admin{
		ID: adminID, Email: "root@example.com", PasswordHash: hash,
		Status: identity.StatusActive, DisplayName: "Root",
	}
	store.adminsByID[adminID] = store.admins["root@example.com"]
	store.permissions[adminID] = []string{"users.read", "audit.read"}

	service, err := identity.NewService(identity.Deps{
		Directory:   store,
		Permissions: store,
		Sessions:    store,
		Hasher:      hasher,
		Auditor:     store,
	})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}

	cfg := testConfig()
	sessions := authmw.New(service, testLogger(), authmw.SessionCookie{Secure: cfg.SecureCookies()})
	attempts := func(scope authmw.Scope, perIP, perAccount int) authmw.Attempts {
		return authmw.Attempts{
			Counter: &counterFake{counts: map[string]int{}},
			Logger:  testLogger(), Scope: scope,
			Window: cfg.RateLimitWindow, PerIP: perIP, PerAccount: perAccount,
		}
	}

	return &authFixture{
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
		}),
		store:   store,
		userID:  userID,
		adminID: adminID,
	}
}

// do performs a request against the router.
func (f *authFixture) do(t *testing.T, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	req.RemoteAddr = "203.0.113.7:4321"

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// envelope is the shape every response is asserted through.
type envelope struct {
	Success   bool   `json:"success"`
	RequestID string `json:"request_id"`
	Data      struct {
		User struct {
			ID       string `json:"id"`
			Email    string `json:"email"`
			Status   string `json:"status"`
			Locale   string `json:"locale"`
			Timezone string `json:"timezone"`
		} `json:"user"`
		Admin struct {
			ID          string   `json:"id"`
			Email       string   `json:"email"`
			DisplayName string   `json:"display_name"`
			Permissions []string `json:"permissions"`
		} `json:"admin"`
		CSRFToken string `json:"csrf_token"`
	} `json:"data"`
	Error struct {
		Code       string `json:"code"`
		MessageKey string `json:"message_key"`
		Details    struct {
			Field string `json:"field"`
		} `json:"details"`
	} `json:"error"`
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var out envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("the response is not the envelope: %v (%s)", err, rec.Body.String())
	}
	return out
}

// sessionCookie returns the named cookie from a response, or nil.
func sessionCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func TestRegisterReturnsTheUserAndNothingElse(t *testing.T) {
	f := newAuthFixture(t)

	rec := f.do(t, http.MethodPost, "/api/v1/auth/register",
		`{"email":"grace@example.com","password":"`+testPassword+`","locale":"en-US","timezone":"UTC"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeEnvelope(t, rec)
	if body.Data.User.Email != "grace@example.com" {
		t.Errorf("email = %q", body.Data.User.Email)
	}
	if body.Data.User.Status != identity.StatusActive {
		t.Errorf("status = %q", body.Data.User.Status)
	}

	// The stored hash must not be published. A view type rather than the domain record is
	// what guarantees it: a struct tag would not stop a field added later from being
	// encoded.
	if strings.Contains(rec.Body.String(), "argon2") {
		t.Error("the response carries a password hash")
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Error("the response mentions a password")
	}

	// Registration does not sign the account in, so no session is issued here.
	if cookie := sessionCookie(rec, authmw.UserCookieName); cookie != nil && cookie.Value != "" {
		t.Error("registration issued a session")
	}
}

func TestRegisterReportsValidationFailuresAgainstTheField(t *testing.T) {
	f := newAuthFixture(t)

	cases := map[string]struct {
		body  string
		field string
	}{
		"password too short": {
			body:  `{"email":"grace@example.com","password":"short"}`,
			field: "password",
		},
		"address not an address": {
			body:  `{"email":"not-an-address","password":"` + testPassword + `"}`,
			field: "email",
		},
		"locale not supported": {
			body:  `{"email":"grace@example.com","password":"` + testPassword + `","locale":"fr-FR"}`,
			field: "locale",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := f.do(t, http.MethodPost, "/api/v1/auth/register", tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422, got %d (%s)", rec.Code, rec.Body.String())
			}

			body := decodeEnvelope(t, rec)
			if body.Error.Code != "VALIDATION_FAILED" {
				t.Errorf("code = %q", body.Error.Code)
			}
			// The field travels as an identifier rather than as a sentence, so the client
			// points at the input and localises the message itself.
			if body.Error.Details.Field != tc.field {
				t.Errorf("field = %q, expected %q", body.Error.Details.Field, tc.field)
			}
			if !strings.HasPrefix(body.Error.MessageKey, "errors.invalid_") {
				t.Errorf("message key = %q", body.Error.MessageKey)
			}
		})
	}
}

func TestRegisterReportsAnExistingAddressAsAConflict(t *testing.T) {
	f := newAuthFixture(t)

	rec := f.do(t, http.MethodPost, "/api/v1/auth/register",
		`{"email":"ada@example.com","password":"`+testPassword+`"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeEnvelope(t, rec)
	if body.Error.MessageKey != "errors.email_taken" {
		t.Errorf("message key = %q; the generic conflict text would not tell the user what "+
			"to change", body.Error.MessageKey)
	}
}

func TestLoginIssuesASessionCookieAndTheCSRFToken(t *testing.T) {
	f := newAuthFixture(t)

	rec := f.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"ada@example.com","password":"`+testPassword+`"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeEnvelope(t, rec)
	if body.Data.User.ID != f.userID.String() {
		t.Errorf("user id = %q", body.Data.User.ID)
	}

	cookie := sessionCookie(rec, authmw.UserCookieName)
	if cookie == nil || cookie.Value == "" {
		t.Fatal("no session cookie was issued")
	}
	if !cookie.HttpOnly {
		t.Error("the session cookie is readable by scripts")
	}

	// The token is deliberately not in the cookie: the cookie is HttpOnly, so a script
	// could not read it, and the response body is where the client gets the token.
	if body.Data.CSRFToken == "" {
		t.Error("no CSRF token was returned")
	}
	if body.Data.CSRFToken == cookie.Value {
		t.Error("the CSRF token is the session identifier")
	}
}

func TestLoginRefusalsAreIndistinguishable(t *testing.T) {
	f := newAuthFixture(t)

	wrongPassword := f.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"ada@example.com","password":"not the password"}`)
	unknownAddress := f.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"nobody@example.com","password":"not the password"}`)

	if wrongPassword.Code != http.StatusUnauthorized || unknownAddress.Code != http.StatusUnauthorized {
		t.Fatalf("statuses: %d and %d", wrongPassword.Code, unknownAddress.Code)
	}

	a := decodeEnvelope(t, wrongPassword)
	b := decodeEnvelope(t, unknownAddress)

	// If these differed, the endpoint would answer "does this address have an account",
	// and the two would differ by exactly one thing the caller is not entitled to know.
	if a.Error != b.Error {
		t.Errorf("the refusals differ: %+v vs %+v", a.Error, b.Error)
	}
	// And they must not be the generic session-expiry message: that would tell a user whose
	// password simply did not match to look for a session they never had.
	if a.Error.MessageKey != "errors.invalid_credentials" {
		t.Errorf("message key = %q", a.Error.MessageKey)
	}

	// No cookie is issued on a refusal, so a failed sign-in cannot leave a half-session.
	if cookie := sessionCookie(wrongPassword, authmw.UserCookieName); cookie != nil && cookie.Value != "" {
		t.Error("a refused sign-in issued a session")
	}
}

func TestMeRequiresASessionAndReturnsItsOwner(t *testing.T) {
	f := newAuthFixture(t)

	if rec := f.do(t, http.MethodGet, "/api/v1/auth/me", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated read was allowed: %d", rec.Code)
	}

	login := f.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"ada@example.com","password":"`+testPassword+`"}`)
	cookie := sessionCookie(login, authmw.UserCookieName)
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	rec := f.do(t, http.MethodGet, "/api/v1/auth/me", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	body := decodeEnvelope(t, rec)
	if body.Data.User.Email != "ada@example.com" {
		t.Errorf("email = %q", body.Data.User.Email)
	}
	// A reloaded page recovers its CSRF token here, so it must be present.
	if body.Data.CSRFToken == "" {
		t.Error("the CSRF token is not recoverable after a reload")
	}
}

func TestLogoutNeedsTheCSRFTokenAndEndsTheSession(t *testing.T) {
	f := newAuthFixture(t)

	login := f.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"ada@example.com","password":"`+testPassword+`"}`)
	cookie := sessionCookie(login, authmw.UserCookieName)
	token := decodeEnvelope(t, login).Data.CSRFToken

	// Without the token a cross-site form post could sign the user out, which is a
	// nuisance rather than a breach — but the same rule covers every mutating endpoint, so
	// it is enforced uniformly rather than per endpoint.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a logout without the token was accepted: %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", token)
	rec = httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (%s)", rec.Code, rec.Body.String())
	}
	if cleared := sessionCookie(rec, authmw.UserCookieName); cleared == nil || cleared.MaxAge >= 0 {
		t.Error("the session cookie was not cleared")
	}

	// And the session is gone, not merely forgotten by the browser.
	if rec := f.do(t, http.MethodGet, "/api/v1/auth/me", "", cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the session survived logout: %d", rec.Code)
	}
}

func TestAdminLoginReturnsTheEffectivePermissions(t *testing.T) {
	f := newAuthFixture(t)

	rec := f.do(t, http.MethodPost, "/api/v1/admin/auth/login",
		`{"email":"root@example.com","password":"`+testPassword+`"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeEnvelope(t, rec)
	if body.Data.Admin.ID != f.adminID.String() {
		t.Errorf("admin id = %q", body.Data.Admin.ID)
	}
	if len(body.Data.Admin.Permissions) != 2 {
		t.Errorf("permissions = %v", body.Data.Admin.Permissions)
	}

	// The admin cookie is a different cookie from the user one, which is what makes the
	// two credential spaces separable at the transport layer.
	cookie := sessionCookie(rec, authmw.AdminCookieName)
	if cookie == nil || cookie.Value == "" {
		t.Fatal("no admin session cookie was issued")
	}
	if sessionCookie(rec, authmw.UserCookieName) != nil {
		t.Error("an admin sign-in issued a user cookie")
	}

	// The sign-in is audited; nothing else in this test is.
	if len(f.store.audited) != 1 || f.store.audited[0].Action != identity.ActionAdminLoggedIn {
		t.Errorf("audit trail = %+v", f.store.audited)
	}
}

func TestAUserSessionCannotReachAnAdminEndpoint(t *testing.T) {
	f := newAuthFixture(t)

	login := f.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"ada@example.com","password":"`+testPassword+`"}`)
	userCookie := sessionCookie(login, authmw.UserCookieName)

	// Presenting the user cookie under the admin cookie's name is the strongest form of the
	// attack: the value is real and the session is live, and it still must not work,
	// because the admin cookie names a namespace the user session never entered.
	rec := f.do(t, http.MethodGet, "/api/v1/admin/auth/me", "",
		&http.Cookie{Name: authmw.AdminCookieName, Value: userCookie.Value})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a user session authenticated an admin endpoint: %d (%s)", rec.Code, rec.Body.String())
	}

	// The same value in its own cookie is accepted for the user endpoint, so the refusal
	// above is about the space and not about the session being unknown.
	if rec := f.do(t, http.MethodGet, "/api/v1/auth/me", "", userCookie); rec.Code != http.StatusOK {
		t.Fatalf("the user session did not authenticate its own endpoint: %d", rec.Code)
	}
}

func TestRepeatedFailedSignInsAreThrottled(t *testing.T) {
	f := newAuthFixture(t)

	// The fixture allows three attempts per account.
	for i := 1; i <= 3; i++ {
		rec := f.do(t, http.MethodPost, "/api/v1/auth/login",
			`{"email":"ada@example.com","password":"not the password"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d returned %d, expected 401", i, rec.Code)
		}
	}

	rec := f.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"ada@example.com","password":"not the password"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the budget was not enforced: %d", rec.Code)
	}
	if body := decodeEnvelope(t, rec); body.Error.Code != "RATE_LIMITED" {
		t.Errorf("error code = %q", body.Error.Code)
	}

	// The correct password is refused too, and that is intended: the throttle is on
	// attempts, and it cannot know which one would have succeeded without checking — which
	// is the work it exists to bound.
	if rec := f.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"ada@example.com","password":"`+testPassword+`"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the throttled account could still attempt: %d", rec.Code)
	}
}

func TestASuccessfulSignInClearsTheBudget(t *testing.T) {
	f := newAuthFixture(t)

	for i := 0; i < 5; i++ {
		rec := f.do(t, http.MethodPost, "/api/v1/auth/login",
			`{"email":"ada@example.com","password":"`+testPassword+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("sign-in %d was refused: %d (%s)", i+1, rec.Code, rec.Body.String())
		}
	}
}

func TestAdminLogoutIsAudited(t *testing.T) {
	f := newAuthFixture(t)

	login := f.do(t, http.MethodPost, "/api/v1/admin/auth/login",
		`{"email":"root@example.com","password":"`+testPassword+`"}`)
	cookie := sessionCookie(login, authmw.AdminCookieName)
	token := decodeEnvelope(t, login).Data.CSRFToken

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/logout", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", token)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (%s)", rec.Code, rec.Body.String())
	}

	actions := make([]string, 0, len(f.store.audited))
	for _, event := range f.store.audited {
		actions = append(actions, event.Action)
	}
	if len(actions) != 2 || actions[0] != identity.ActionAdminLoggedIn || actions[1] != identity.ActionAdminLoggedOut {
		t.Errorf("audit actions = %v", actions)
	}

	// The subject is recorded, which is what makes the entry attributable rather than
	// merely present.
	if last := f.store.audited[1]; last.ActorID != f.adminID || last.ActorType != audit.ActorAdmin {
		t.Errorf("the logout entry does not name the administrator: %+v", last)
	}
}

func TestARefusedAdminSignInIsAuditedWithAMaskedAddress(t *testing.T) {
	f := newAuthFixture(t)

	f.do(t, http.MethodPost, "/api/v1/admin/auth/login",
		`{"email":"someone@example.com","password":"not the password"}`)

	if len(f.store.audited) != 1 {
		t.Fatalf("audit trail = %+v", f.store.audited)
	}
	event := f.store.audited[0]
	if event.Action != identity.ActionAdminLoginRefused {
		t.Errorf("action = %q", event.Action)
	}
	// A trail an operator can read, that is not itself a list of the addresses that exist.
	if got := event.Details["attempted"]; got != "s***@example.com" {
		t.Errorf("attempted address = %q; it must be masked", got)
	}
	// Where it came from is a column rather than a JSON entry, so an operator filtering by
	// address does not depend on a key being spelled the same way at every call site.
	if event.Context.IP != "203.0.113.7" {
		t.Errorf("source address = %q", event.Context.IP)
	}
}

func TestOrdinaryUserSignInsAreNotAudited(t *testing.T) {
	f := newAuthFixture(t)

	f.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"ada@example.com","password":"`+testPassword+`"}`)

	// Auditing every sign-in would bury the high-risk entries docs/14 requires among
	// ordinary traffic, and a user sign-in is already visible as last_login_at plus the
	// request log.
	if len(f.store.audited) != 0 {
		t.Errorf("a user sign-in was audited: %+v", f.store.audited)
	}
}
