package authmw

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
)

// These tests cover the rules that only exist at the transport layer: which cookie is
// read for which credential space, what a refusal reveals, when the CSRF token is
// required, and what attributes a session cookie carries. The rules themselves — who may
// sign in, what a session is — belong to internal/identity and are covered there.

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// sessionStoreFake is a session store in memory.
type sessionStoreFake struct {
	sessions map[string]identity.Session
	failWith error
}

func newSessionStoreFake() *sessionStoreFake {
	return &sessionStoreFake{sessions: map[string]identity.Session{}}
}

func (f *sessionStoreFake) key(subject identity.SubjectType, id string) string {
	return string(subject) + ":" + id
}

func (f *sessionStoreFake) Create(_ context.Context, session identity.Session) error {
	f.sessions[f.key(session.Subject, session.ID)] = session
	return nil
}

func (f *sessionStoreFake) Get(_ context.Context, subject identity.SubjectType, id string) (identity.Session, error) {
	if f.failWith != nil {
		return identity.Session{}, f.failWith
	}
	session, ok := f.sessions[f.key(subject, id)]
	if !ok {
		// The subject type is part of the key, so a session issued in one space is not
		// found by a lookup in the other — which is the property under test rather than
		// an implementation detail of this fake.
		return identity.Session{}, identity.ErrSessionNotFound
	}
	return session, nil
}

func (f *sessionStoreFake) Delete(_ context.Context, subject identity.SubjectType, id string) error {
	delete(f.sessions, f.key(subject, id))
	return nil
}

func (f *sessionStoreFake) DeleteForSubject(_ context.Context, subject identity.SubjectType, subjectID uuid.UUID) error {
	for key, session := range f.sessions {
		if session.Subject == subject && session.SubjectID == subjectID {
			delete(f.sessions, key)
		}
	}
	return nil
}

// directoryFake serves accounts from memory.
type directoryFake struct {
	users  map[uuid.UUID]identity.User
	admins map[uuid.UUID]identity.Admin
}

func (f *directoryFake) CreateUser(context.Context, identity.User) (identity.User, error) {
	return identity.User{}, nil
}

func (f *directoryFake) FindUserByEmail(context.Context, string) (identity.User, error) {
	return identity.User{}, identity.ErrUserNotFound
}

func (f *directoryFake) FindUserByID(_ context.Context, id uuid.UUID) (identity.User, error) {
	user, ok := f.users[id]
	if !ok {
		return identity.User{}, identity.ErrUserNotFound
	}
	return user, nil
}

func (f *directoryFake) RecordUserLogin(context.Context, uuid.UUID) error { return nil }

func (f *directoryFake) UpdateUserPasswordHash(context.Context, uuid.UUID, string) error {
	return nil
}

func (f *directoryFake) FindAdminByEmail(context.Context, string) (identity.Admin, error) {
	return identity.Admin{}, identity.ErrAdminNotFound
}

func (f *directoryFake) FindAdminByID(_ context.Context, id uuid.UUID) (identity.Admin, error) {
	admin, ok := f.admins[id]
	if !ok {
		return identity.Admin{}, identity.ErrAdminNotFound
	}
	return admin, nil
}

func (f *directoryFake) RecordAdminLogin(context.Context, uuid.UUID) error { return nil }

func (f *directoryFake) UpdateAdminPasswordHash(context.Context, uuid.UUID, string) error {
	return nil
}

// permissionsFake returns a fixed set per administrator.
type permissionsFake struct{ sets map[uuid.UUID][]string }

func (f *permissionsFake) PermissionsForAdmin(_ context.Context, adminID uuid.UUID) ([]string, error) {
	return f.sets[adminID], nil
}

// harness is a service plus the accounts it can authenticate.
type harness struct {
	service  *identity.Service
	sessions *sessionStoreFake
	mw       *Middleware
	userID   uuid.UUID
	adminID  uuid.UUID
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	// Cheap argon2 parameters: the tests here are about the transport, and the default
	// cost would make every case slow without testing anything more. The parameters that
	// matter for these assertions are exercised by the password tests.
	hasher, err := identity.NewPasswordHasher(identity.PasswordParams{
		Memory:      1024,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	})
	if err != nil {
		t.Fatalf("build hasher: %v", err)
	}

	userID := uuid.MustParse("0198f1c2-0000-7000-8000-000000000001")
	adminID := uuid.MustParse("0198f1c2-0000-7000-8000-000000000002")

	directory := &directoryFake{
		users: map[uuid.UUID]identity.User{
			userID: {ID: userID, Email: "ada@example.com", Status: identity.StatusActive},
		},
		admins: map[uuid.UUID]identity.Admin{
			adminID: {ID: adminID, Email: "root@example.com", Status: identity.StatusActive},
		},
	}
	store := newSessionStoreFake()

	service, err := identity.NewService(identity.Deps{
		Directory:   directory,
		Permissions: &permissionsFake{sets: map[uuid.UUID][]string{adminID: {"users.read", "instances.read"}}},
		Sessions:    store,
		Hasher:      hasher,
	})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}

	return &harness{
		service:  service,
		sessions: store,
		mw:       New(service, discardLogger(), SessionCookie{Secure: false}),
		userID:   userID,
		adminID:  adminID,
	}
}

// issue creates a session and returns it.
func (h *harness) issue(t *testing.T, subject identity.SubjectType, id uuid.UUID) identity.Session {
	t.Helper()
	session, err := identity.NewSession(subject, id, time.Now())
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	if err := h.sessions.Create(context.Background(), session); err != nil {
		t.Fatalf("store session: %v", err)
	}
	return session
}

// probe calls the middleware and reports the status the request produced.
func (h *harness) probe(middleware func(http.Handler) http.Handler, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	reached := false
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if reached && rec.Code != http.StatusOK {
		panic("the handler ran but the response was not 200")
	}
	return rec
}

func TestRequireUserRefusesAUserSessionPresentedAsAnAdmin(t *testing.T) {
	h := newHarness(t)
	userSession := h.issue(t, identity.SubjectUser, h.userID)

	// The admin middleware reads the admin cookie only, so a user session arriving in the
	// user cookie is not looked up at all. This is the isolation docs/14 asks for, and it
	// is the reason the two spaces have different cookie names rather than one name with a
	// subject field inside.
	rec := h.probe(h.mw.RequireAdmin, &http.Cookie{Name: UserCookieName, Value: userSession.ID})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a user session authenticated an admin request: %d (%s)", rec.Code, rec.Body.String())
	}

	// And the same session in the right cookie is accepted, so the refusal above is about
	// the cookie and not about the session being unknown.
	rec = h.probe(h.mw.RequireUser, &http.Cookie{Name: UserCookieName, Value: userSession.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("a user session did not authenticate its own space: %d", rec.Code)
	}
}

func TestRequireUserRefusesAnAdminSessionPresentedAsAUser(t *testing.T) {
	h := newHarness(t)
	adminSession := h.issue(t, identity.SubjectAdmin, h.adminID)

	rec := h.probe(h.mw.RequireUser, &http.Cookie{Name: UserCookieName, Value: adminSession.ID})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an admin session authenticated a user request: %d", rec.Code)
	}
}

func TestRequireUserClearsACookieForASessionThatNoLongerExists(t *testing.T) {
	h := newHarness(t)

	rec := h.probe(h.mw.RequireUser, &http.Cookie{Name: UserCookieName, Value: "not-a-real-session"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	// Without this the browser would present the same dead identifier on every request,
	// repeating a lookup that can only fail.
	cleared := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == UserCookieName && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("the dead session cookie was not cleared: %v", rec.Result().Cookies())
	}
}

func TestRequireUserReportsADependencyFailureAsSuch(t *testing.T) {
	h := newHarness(t)
	h.sessions.failWith = context.DeadlineExceeded

	rec := h.probe(h.mw.RequireUser, &http.Cookie{Name: UserCookieName, Value: "anything"})

	// 503, not 401. Telling a user their session is invalid because Redis was briefly
	// unreachable would send them to a sign-in page that cannot work either, and would hide
	// the outage from the operator who is looking at error rates.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a store failure was reported as %d, expected 503", rec.Code)
	}
}

func TestRequireUserRefusesASuspendedAccountOnAnExistingSession(t *testing.T) {
	h := newHarness(t)
	session := h.issue(t, identity.SubjectUser, h.userID)

	// The account is suspended after the session was issued. That is the case the check
	// exists for: a suspension has to take effect on the next request, not whenever the
	// session happens to expire.
	suspendedDirectory := &directoryFake{
		users: map[uuid.UUID]identity.User{
			h.userID: {ID: h.userID, Email: "ada@example.com", Status: identity.StatusSuspended},
		},
		admins: map[uuid.UUID]identity.Admin{},
	}
	service, err := identity.NewService(identity.Deps{
		Directory:   suspendedDirectory,
		Permissions: &permissionsFake{},
		Sessions:    h.sessions,
		Hasher:      mustCheapHasher(t),
	})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}

	suspended := New(service, discardLogger(), SessionCookie{})
	rec := h.probe(suspended.RequireUser, &http.Cookie{Name: UserCookieName, Value: session.ID})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a suspended account was allowed through: %d (%s)", rec.Code, rec.Body.String())
	}
}

func mustCheapHasher(t *testing.T) *identity.PasswordHasher {
	t.Helper()
	hasher, err := identity.NewPasswordHasher(identity.PasswordParams{
		Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("build hasher: %v", err)
	}
	return hasher
}

// sessionThenCSRF composes the chain the way the router mounts it: the session check
// outermost, then the CSRF check, then the handler.
//
// The order is not cosmetic. chi applies middleware in the order given, so mounting the
// two the other way round would run the CSRF check before a principal exists, and every
// mutating request would be refused. Composing it here the same way the router does means
// a divergence between the two shows up as a failing test rather than as a
// production-only refusal.
func (h *harness) sessionThenCSRF(next http.Handler) http.Handler {
	return h.mw.RequireUser(h.mw.RequireCSRF(next))
}

func (h *harness) adminThenCSRFThenPermission(permission string, next http.Handler) http.Handler {
	return h.mw.RequireAdmin(h.mw.RequireCSRF(h.mw.RequirePermission(permission)(next)))
}

func TestRequireCSRFExemptsSafeMethodsAndDemandsTheTokenOtherwise(t *testing.T) {
	h := newHarness(t)
	session := h.issue(t, identity.SubjectUser, h.userID)

	call := func(method, token string) *httptest.ResponseRecorder {
		var seen Principal
		var present bool
		chain := h.sessionThenCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen, present = PrincipalFrom(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(method, "/", nil)
		req.AddCookie(&http.Cookie{Name: UserCookieName, Value: session.ID})
		if token != "" {
			req.Header.Set(CSRFHeaderName, token)
		}
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK && (!present || seen.Session.ID != session.ID) {
			t.Errorf("the principal was not on the context")
		}
		return rec
	}

	// A safe method must not need a token: requiring one on every read would mean issuing
	// and fetching one before a page can load.
	if rec := call(http.MethodGet, ""); rec.Code != http.StatusOK {
		t.Fatalf("a GET required a CSRF token: %d", rec.Code)
	}

	if rec := call(http.MethodPost, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("a POST without a token was accepted: %d", rec.Code)
	}
	if rec := call(http.MethodPost, "wrong-token"); rec.Code != http.StatusForbidden {
		t.Fatalf("a POST with the wrong token was accepted: %d", rec.Code)
	}
	if rec := call(http.MethodPost, session.CSRFSecret); rec.Code != http.StatusOK {
		t.Fatalf("a POST with the session's token was refused: %d", rec.Code)
	}

	// A token issued for another session is not accepted, which is what makes a leaked
	// token useless against a different account.
	other := h.issue(t, identity.SubjectUser, uuid.MustParse("0198f1c2-0000-7000-8000-000000000003"))
	if rec := call(http.MethodPost, other.CSRFSecret); rec.Code != http.StatusForbidden {
		t.Fatalf("another session's token was accepted: %d", rec.Code)
	}
}

func TestRequireCSRFMountedOutsideTheSessionCheckStillRefuses(t *testing.T) {
	h := newHarness(t)
	session := h.issue(t, identity.SubjectUser, h.userID)

	// A mis-ordered chain must fail closed. Mounting the token check before the session
	// check finds no principal, and refusing is the only safe reading — a check that
	// silently passed when it could not evaluate would be a hole, not a bug.
	chain := h.mw.RequireCSRF(h.mw.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: UserCookieName, Value: session.ID})
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("a mis-ordered chain let a mutating request through")
	}
}

func TestRequirePermissionUsesTheResolvedSet(t *testing.T) {
	h := newHarness(t)

	call := func(permission string) int {
		chain := h.adminThenCSRFThenPermission(permission, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: AdminCookieName, Value: h.issue(t, identity.SubjectAdmin, h.adminID).ID})
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := call("users.read"); code != http.StatusOK {
		t.Fatalf("a held permission was refused: %d", code)
	}
	// Not held, so refused — and refused with 403 rather than 404: the administrator has
	// proved who they are, and hiding the route would only make the interface harder to
	// build without protecting anything.
	if code := call("ledger.adjust"); code != http.StatusForbidden {
		t.Fatalf("an unheld permission was allowed: %d", code)
	}
}

func TestIssueSessionAttributes(t *testing.T) {
	h := newHarness(t)
	session := h.issue(t, identity.SubjectAdmin, h.adminID)

	rec := httptest.NewRecorder()
	h.mw.IssueSession(rec, identity.SubjectAdmin, session)

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie, got %d", len(cookies))
	}
	cookie := cookies[0]

	if cookie.Name != AdminCookieName {
		t.Errorf("cookie name = %q", cookie.Name)
	}
	// HttpOnly is what keeps the identifier out of reach of any script on the page, and
	// the CSRF token is returned in the response body precisely because this is set.
	if !cookie.HttpOnly {
		t.Error("the session cookie is readable by scripts")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v", cookie.SameSite)
	}
	if cookie.Path != SessionCookiePath {
		t.Errorf("Path = %q", cookie.Path)
	}
	if cookie.Secure {
		t.Error("the cookie was marked Secure in a configuration that did not ask for it")
	}
	// The browser must stop presenting the cookie at the same moment the platform stops
	// accepting it. The comparison is per second because an HTTP cookie date has no
	// sub-second field: the platform's own store is what enforces the exact instant.
	if !cookie.Expires.Truncate(time.Second).Equal(session.ExpiresAt.Truncate(time.Second)) {
		t.Errorf("cookie expires at %s, session expires at %s", cookie.Expires, session.ExpiresAt)
	}
	if cookie.MaxAge <= 0 {
		t.Errorf("MaxAge = %d; a session cookie without one is discarded when the browser closes", cookie.MaxAge)
	}

	// Secure is honoured when configured, because a production deployment is refused the
	// insecure setting by config.Validate rather than silently downgraded.
	secure := New(h.service, discardLogger(), SessionCookie{Secure: true})
	rec = httptest.NewRecorder()
	secure.IssueSession(rec, identity.SubjectAdmin, session)
	if !rec.Result().Cookies()[0].Secure {
		t.Error("a Secure configuration produced a cookie without Secure")
	}
}
