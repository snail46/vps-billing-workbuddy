package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/audit"
)

// The fakes below stand in for the database and Redis, which this machine has
// neither of (ADR-003). The service's rules are what the tests are about; the
// adapter that talks to Postgres has its own tests in the integration tier.

type fakeDirectory struct {
	users  []User
	admins []Admin

	createdUsers   []User
	loginRecords   []uuid.UUID
	adminLogins    []uuid.UUID
	rehashedUsers  map[uuid.UUID]string
	rehashedAdmins map[uuid.UUID]string

	createUserErr error
	findUserErr   error
	recordErr     error
}

func newFakeDirectory() *fakeDirectory {
	return &fakeDirectory{rehashedUsers: map[uuid.UUID]string{}, rehashedAdmins: map[uuid.UUID]string{}}
}

func (f *fakeDirectory) CreateUser(_ context.Context, user User) (User, error) {
	if f.createUserErr != nil {
		return User{}, f.createUserErr
	}
	for _, existing := range f.users {
		if existing.Email == user.Email {
			return User{}, ErrEmailTaken
		}
	}
	f.users = append(f.users, user)
	f.createdUsers = append(f.createdUsers, user)
	return user, nil
}

func (f *fakeDirectory) FindUserByEmail(_ context.Context, email string) (User, error) {
	if f.findUserErr != nil {
		return User{}, f.findUserErr
	}
	for _, user := range f.users {
		if user.Email == email {
			return user, nil
		}
	}
	return User{}, ErrUserNotFound
}

func (f *fakeDirectory) FindUserByID(_ context.Context, id uuid.UUID) (User, error) {
	for _, user := range f.users {
		if user.ID == id {
			return user, nil
		}
	}
	return User{}, ErrUserNotFound
}

func (f *fakeDirectory) RecordUserLogin(_ context.Context, id uuid.UUID) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	f.loginRecords = append(f.loginRecords, id)
	return nil
}

func (f *fakeDirectory) UpdateUserPasswordHash(_ context.Context, id uuid.UUID, hash string) error {
	f.rehashedUsers[id] = hash
	return nil
}

func (f *fakeDirectory) FindAdminByEmail(_ context.Context, email string) (Admin, error) {
	if f.findUserErr != nil {
		return Admin{}, f.findUserErr
	}
	for _, admin := range f.admins {
		if admin.Email == email {
			return admin, nil
		}
	}
	return Admin{}, ErrUserNotFound
}

func (f *fakeDirectory) FindAdminByID(_ context.Context, id uuid.UUID) (Admin, error) {
	for _, admin := range f.admins {
		if admin.ID == id {
			return admin, nil
		}
	}
	return Admin{}, ErrUserNotFound
}

func (f *fakeDirectory) RecordAdminLogin(_ context.Context, id uuid.UUID) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	f.adminLogins = append(f.adminLogins, id)
	return nil
}

func (f *fakeDirectory) UpdateAdminPasswordHash(_ context.Context, id uuid.UUID, hash string) error {
	f.rehashedAdmins[id] = hash
	return nil
}

type fakePermissions struct {
	keys []string
	err  error
}

func (f *fakePermissions) PermissionsForAdmin(context.Context, uuid.UUID) ([]string, error) {
	return f.keys, f.err
}

type fakeSessions struct {
	sessions map[string]Session
	nextErr  error
}

func newFakeSessions() *fakeSessions { return &fakeSessions{sessions: map[string]Session{}} }

func sessionMapKey(subject SubjectType, id string) string { return string(subject) + "|" + id }

func (f *fakeSessions) Create(_ context.Context, session Session) error {
	if f.nextErr != nil {
		return f.nextErr
	}
	f.sessions[sessionMapKey(session.Subject, session.ID)] = session
	return nil
}

func (f *fakeSessions) Get(_ context.Context, subject SubjectType, id string) (Session, error) {
	session, ok := f.sessions[sessionMapKey(subject, id)]
	if !ok {
		return Session{}, ErrSessionNotFound
	}
	return session, nil
}

func (f *fakeSessions) Delete(_ context.Context, subject SubjectType, id string) error {
	delete(f.sessions, sessionMapKey(subject, id))
	return nil
}

func (f *fakeSessions) DeleteForSubject(_ context.Context, subject SubjectType, subjectID uuid.UUID) error {
	for key, session := range f.sessions {
		if session.Subject == subject && session.SubjectID == subjectID {
			delete(f.sessions, key)
		}
	}
	return nil
}

type fakeAuditor struct {
	events []audit.Event
	err    error
}

func (f *fakeAuditor) Record(_ context.Context, event audit.Event) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, event)
	return nil
}

// testHasher is cheap: argon2id at production cost makes a table of login cases
// slow enough that the unit tier stops being run.
func testHasher(t *testing.T) *PasswordHasher {
	t.Helper()
	hasher, err := NewPasswordHasher(PasswordParams{
		Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("NewPasswordHasher: %v", err)
	}
	return hasher
}

type harness struct {
	service     *Service
	directory   *fakeDirectory
	permissions *fakePermissions
	sessions    *fakeSessions
	auditor     *fakeAuditor
	hasher      *PasswordHasher
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	directory := newFakeDirectory()
	permissions := &fakePermissions{keys: []string{"operations.read"}}
	sessions := newFakeSessions()
	auditor := &fakeAuditor{}
	hasher := testHasher(t)

	service, err := NewService(Deps{
		Directory:   directory,
		Permissions: permissions,
		Sessions:    sessions,
		Hasher:      hasher,
		Auditor:     auditor,
		Now:         func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	return &harness{service, directory, permissions, sessions, auditor, hasher}
}

func (h *harness) addUser(t *testing.T, email, password, status string) User {
	t.Helper()
	hash, err := h.hasher.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	user := User{
		ID: uuid.New(), Email: NormalizeEmail(email), PasswordHash: hash,
		Status: status, Locale: "zh-CN", Timezone: "UTC",
	}
	h.directory.users = append(h.directory.users, user)
	return user
}

func (h *harness) addAdmin(t *testing.T, email, password, status string) Admin {
	t.Helper()
	hash, err := h.hasher.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	admin := Admin{ID: uuid.New(), Email: NormalizeEmail(email), PasswordHash: hash, Status: status}
	h.directory.admins = append(h.directory.admins, admin)
	return admin
}

func TestNewServiceRequiresEveryCollaborator(t *testing.T) {
	directory := newFakeDirectory()
	permissions := &fakePermissions{}
	sessions := newFakeSessions()
	hasher := testHasher(t)

	// A missing collaborator is refused at construction rather than becoming a panic
	// in the request path.
	cases := map[string]Deps{
		"no directory":   {Permissions: permissions, Sessions: sessions, Hasher: hasher},
		"no permissions": {Directory: directory, Sessions: sessions, Hasher: hasher},
		"no sessions":    {Directory: directory, Permissions: permissions, Hasher: hasher},
		"no hasher":      {Directory: directory, Permissions: permissions, Sessions: sessions},
	}
	for name, deps := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewService(deps); err == nil {
				t.Error("expected the missing collaborator to be refused")
			}
		})
	}
}

func TestNormalizeEmail(t *testing.T) {
	cases := map[string]string{
		"  Ada@Example.COM ": "ada@example.com",
		"ada@example.com":    "ada@example.com",
		"\tada@example.com":  "ada@example.com",
		"":                   "",
	}
	for input, want := range cases {
		if got := NormalizeEmail(input); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidateEmail(t *testing.T) {
	valid := []string{
		"ada@example.com",
		"ada.lovelace+tag@mail.example.co.uk",
		"a@b.co",
	}
	for _, email := range valid {
		if err := validateEmail(email); err != nil {
			t.Errorf("validateEmail(%q) rejected a valid address: %v", email, err)
		}
	}

	invalid := map[string]string{
		"empty":             "",
		"no at":             "ada.example.com",
		"two at":            "ada@@example.com",
		"empty local":       "@example.com",
		"empty domain":      "ada@",
		"no dot in domain":  "ada@example",
		"whitespace inside": "ada @example.com",
		"too long":          strings.Repeat("a", maxEmailLength) + "@example.com",
	}
	for name, email := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := validateEmail(NormalizeEmail(email)); !errors.Is(err, ErrInvalidEmail) {
				t.Errorf("expected ErrInvalidEmail for %q, got %v", email, err)
			}
		})
	}
}

func TestRegisterRejectsUnacceptableInput(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.service.Register(ctx, RegisterInput{Email: "not-an-email", Password: strings.Repeat("x", 16)}); !errors.Is(err, ErrInvalidEmail) {
		t.Errorf("expected ErrInvalidEmail, got %v", err)
	}
	if _, err := h.service.Register(ctx, RegisterInput{Email: "ada@example.com", Password: "short"}); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("expected ErrWeakPassword, got %v", err)
	}
	if _, err := h.service.Register(ctx, RegisterInput{
		Email: "ada@example.com", Password: strings.Repeat("x", MinPasswordLength), Locale: "fr-FR",
	}); !errors.Is(err, ErrUnsupportedLocale) {
		t.Errorf("expected ErrUnsupportedLocale, got %v", err)
	}

	if len(h.directory.createdUsers) != 0 {
		t.Errorf("a rejected registration still created %d users", len(h.directory.createdUsers))
	}
}

func TestRegisterNormalizesAndHashes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	user, err := h.service.Register(ctx, RegisterInput{
		Email: "  Ada@Example.COM ", Password: "a sufficiently long password",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if user.Email != "ada@example.com" {
		t.Errorf("the stored address is %q; normalisation is what makes the plain UNIQUE "+
			"constraint sufficient", user.Email)
	}
	if strings.Contains(user.PasswordHash, "a sufficiently long password") {
		t.Fatal("the password was stored in the clear")
	}
	verification, err := h.hasher.Verify("a sufficiently long password", user.PasswordHash)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !verification.Matches {
		t.Error("the stored hash does not verify the password that was registered")
	}

	if user.Status != StatusActive {
		t.Errorf("a new account starts %q, want %q", user.Status, StatusActive)
	}
	// docs/13 defines two locales; a registration without one gets the default
	// rather than an empty locale that would render as raw keys.
	if user.Locale != "zh-CN" {
		t.Errorf("default locale = %q, want zh-CN", user.Locale)
	}
	if user.Timezone != "UTC" {
		t.Errorf("default timezone = %q, want UTC", user.Timezone)
	}
}

func TestRegisterReportsAnAddressAlreadyInUse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addUser(t, "ada@example.com", "an existing password", StatusActive)

	_, err := h.service.Register(ctx, RegisterInput{
		Email: "ADA@example.com", Password: strings.Repeat("x", MinPasswordLength),
	})
	if !errors.Is(err, ErrEmailTaken) {
		t.Errorf("expected ErrEmailTaken, got %v", err)
	}
}

func TestLoginUserIssuesASession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user := h.addUser(t, "ada@example.com", "a sufficiently long password", StatusActive)

	result, err := h.service.LoginUser(ctx, LoginInput{Email: " Ada@Example.com ", Password: "a sufficiently long password"})
	if err != nil {
		t.Fatalf("LoginUser: %v", err)
	}

	if result.Session.Subject != SubjectUser || result.Session.SubjectID != user.ID {
		t.Errorf("the session authenticates the wrong subject: %+v", result.Session)
	}
	if result.Session.ExpiresAt.Sub(result.Session.IssuedAt) != UserSessionTTL {
		t.Errorf("the session lifetime is %v", result.Session.ExpiresAt.Sub(result.Session.IssuedAt))
	}
	if result.Permissions != nil {
		t.Error("a user session must carry no permissions: a customer's rights follow from owning " +
			"the resource, not from a role")
	}

	if _, err := h.sessions.Get(ctx, SubjectUser, result.Session.ID); err != nil {
		t.Errorf("the session was not stored: %v", err)
	}
	if len(h.directory.loginRecords) != 1 || h.directory.loginRecords[0] != user.ID {
		t.Errorf("the login was not recorded: %v", h.directory.loginRecords)
	}
	// Ordinary sign-ins are not audited: auditing them would bury the high-risk
	// entries docs/14 requires.
	if len(h.auditor.events) != 0 {
		t.Errorf("a user login produced %d audit entries", len(h.auditor.events))
	}
}

func TestLoginUserRefusesWrongPasswordAndUnknownAddressAlike(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addUser(t, "ada@example.com", "a sufficiently long password", StatusActive)

	_, wrongPasswordErr := h.service.LoginUser(ctx, LoginInput{Email: "ada@example.com", Password: "the wrong password"})
	if !errors.Is(wrongPasswordErr, ErrInvalidCredentials) {
		t.Errorf("a wrong password returned %v", wrongPasswordErr)
	}
	_, unknownAddressErr := h.service.LoginUser(ctx, LoginInput{Email: "nobody@example.com", Password: "the wrong password"})
	if !errors.Is(unknownAddressErr, ErrInvalidCredentials) {
		t.Errorf("an unknown address returned %v", unknownAddressErr)
	}

	// Both refusals must be indistinguishable to a caller, because distinguishing
	// them turns the login endpoint into a membership oracle. `errors.Is` above
	// proved both are the same sentinel; comparing what the caller would render is
	// what expresses "these cannot be told apart", and errorlint is right that `!=`
	// on errors is not that check — a wrapped error would compare unequal.
	if wrongPasswordErr.Error() != unknownAddressErr.Error() {
		t.Errorf("the two refusals differ: %v vs %v", wrongPasswordErr, unknownAddressErr)
	}
	if len(h.sessions.sessions) != 0 {
		t.Error("a refused login created a session")
	}
}

func TestLoginUserChecksStatusAfterThePassword(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addUser(t, "ada@example.com", "a sufficiently long password", StatusSuspended)

	// Someone without the password learns only that the credentials are wrong, which
	// keeps the account's existence and its state out of reach.
	if _, err := h.service.LoginUser(ctx, LoginInput{Email: "ada@example.com", Password: "the wrong password"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a wrong password on a suspended account returned %v", err)
	}

	// Someone who has proved they own the account is told why they cannot get in.
	if _, err := h.service.LoginUser(ctx, LoginInput{Email: "ada@example.com", Password: "a sufficiently long password"}); !errors.Is(err, ErrAccountSuspended) {
		t.Errorf("a suspended account returned %v", err)
	}
}

func TestLoginUserRehashesWhenTheCostHasRisen(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	oldHasher, err := NewPasswordHasher(PasswordParams{
		Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("NewPasswordHasher: %v", err)
	}
	staleHash, err := oldHasher.Hash("a sufficiently long password")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	user := User{
		ID: uuid.New(), Email: "ada@example.com", PasswordHash: staleHash,
		Status: StatusActive, Locale: "zh-CN", Timezone: "UTC",
	}
	h.directory.users = append(h.directory.users, user)

	// The service's hasher uses different parameters, so the stored credential is
	// behind. A successful login is the only moment the plaintext exists, and
	// therefore the only moment the cost can be raised.
	h.hasher, err = NewPasswordHasher(PasswordParams{
		Memory: 2048, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("NewPasswordHasher: %v", err)
	}
	h.service.hasher = h.hasher

	if _, err := h.service.LoginUser(ctx, LoginInput{Email: "ada@example.com", Password: "a sufficiently long password"}); err != nil {
		t.Fatalf("LoginUser: %v", err)
	}

	updated, ok := h.directory.rehashedUsers[user.ID]
	if !ok {
		t.Fatal("the stored hash was not replaced; the raised cost would never take effect")
	}
	verification, err := h.hasher.Verify("a sufficiently long password", updated)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !verification.Matches || verification.NeedsRehash {
		t.Errorf("the replacement hash does not match the current parameters: %+v", verification)
	}
}

func TestLoginUserReportsACorruptStoredHash(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.directory.users = append(h.directory.users, User{
		ID: uuid.New(), Email: "ada@example.com", PasswordHash: "not-a-hash", Status: StatusActive,
	})

	_, err := h.service.LoginUser(ctx, LoginInput{Email: "ada@example.com", Password: "any password at all"})
	// Reported, not treated as a wrong password: a corrupt credential sends the user
	// to reset a password that could never have worked, and hides the row from
	// operators.
	if !errors.Is(err, ErrPasswordHashInvalid) {
		t.Errorf("expected ErrPasswordHashInvalid, got %v", err)
	}
}

func TestLoginAdminReturnsPermissionsAndAudits(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	admin := h.addAdmin(t, "ops@example.com", "a sufficiently long password", StatusActive)
	h.permissions.keys = []string{"instances.read", "instances.restart"}

	result, err := h.service.LoginAdmin(ctx, LoginInput{Email: "ops@example.com", Password: "a sufficiently long password"})
	if err != nil {
		t.Fatalf("LoginAdmin: %v", err)
	}

	if result.Session.Subject != SubjectAdmin {
		t.Errorf("the session subject is %q", result.Session.Subject)
	}
	if len(result.Permissions) != 2 {
		t.Errorf("expected two permissions, got %v", result.Permissions)
	}
	if len(h.auditor.events) != 1 || h.auditor.events[0].Action != ActionAdminLoggedIn {
		t.Fatalf("expected one login audit entry, got %+v", h.auditor.events)
	}
	if h.auditor.events[0].ActorID != admin.ID {
		t.Errorf("the audit entry names the wrong actor: %s", h.auditor.events[0].ActorID)
	}
}

func TestLoginAdminRefusalIsAuditedWithoutTheFullAddress(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, err := h.service.LoginAdmin(ctx, LoginInput{
		Email: "ops@example.com", Password: "the wrong password",
		ClientContext: audit.Context{IP: "203.0.113.7", UserAgent: "test"},
	})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}

	if len(h.auditor.events) != 1 || h.auditor.events[0].Action != ActionAdminLoginRefused {
		t.Fatalf("expected one refusal audit entry, got %+v", h.auditor.events)
	}
	attempted := h.auditor.events[0].Details["attempted"]
	// The trail has to be readable by operators without becoming a list of the
	// addresses that exist.
	if attempted != "o***@example.com" {
		t.Errorf("masked address = %q", attempted)
	}
	// The source address is a first-class field, so an operator filtering by it does
	// not depend on a JSON key being spelled the same way at every call site.
	if h.auditor.events[0].Context.IP != "203.0.113.7" {
		t.Errorf("the source address was not recorded: %+v", h.auditor.events[0].Context)
	}
}

func TestLogoutAdminAuditsOnceAndToleratesAMissingSession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	admin := h.addAdmin(t, "ops@example.com", "a sufficiently long password", StatusActive)

	result, err := h.service.LoginAdmin(ctx, LoginInput{Email: "ops@example.com", Password: "a sufficiently long password"})
	if err != nil {
		t.Fatalf("LoginAdmin: %v", err)
	}

	if err := h.service.LogoutAdmin(ctx, result.Session.ID, audit.Context{IP: "203.0.113.7"}); err != nil {
		t.Fatalf("LogoutAdmin: %v", err)
	}
	if _, err := h.sessions.Get(ctx, SubjectAdmin, result.Session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Error("the session survived the logout")
	}

	actions := map[string]int{}
	for _, event := range h.auditor.events {
		actions[event.Action]++
	}
	if actions[ActionAdminLoggedOut] != 1 {
		t.Errorf("expected exactly one logout entry, got %d", actions[ActionAdminLoggedOut])
	}
	if h.auditor.events[len(h.auditor.events)-1].ActorID != admin.ID {
		t.Error("the logout entry names the wrong actor")
	}

	// A logout of a session that has already gone is not an error, and produces no
	// second entry: there is nothing to report.
	if err := h.service.LogoutAdmin(ctx, result.Session.ID, audit.Context{IP: "203.0.113.7"}); err != nil {
		t.Errorf("a repeated logout returned %v", err)
	}
	actions = map[string]int{}
	for _, event := range h.auditor.events {
		actions[event.Action]++
	}
	if actions[ActionAdminLoggedOut] != 1 {
		t.Errorf("the repeated logout produced another entry (total %d)", actions[ActionAdminLoggedOut])
	}
}

func TestLogoutUserRemovesTheSession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addUser(t, "ada@example.com", "a sufficiently long password", StatusActive)

	result, err := h.service.LoginUser(ctx, LoginInput{Email: "ada@example.com", Password: "a sufficiently long password"})
	if err != nil {
		t.Fatalf("LoginUser: %v", err)
	}
	if err := h.service.Logout(ctx, SubjectUser, result.Session.ID); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := h.service.CurrentSession(ctx, SubjectUser, result.Session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Error("the session survived the logout")
	}
}

func TestAuditFailureDoesNotFailTheRequest(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addAdmin(t, "ops@example.com", "a sufficiently long password", StatusActive)
	h.auditor.err = errors.New("audit store unavailable")

	// An observability problem must not become an outage: the operator is
	// authenticated, and refusing them would be a bigger failure than a missing
	// audit row.
	if _, err := h.service.LoginAdmin(ctx, LoginInput{Email: "ops@example.com", Password: "a sufficiently long password"}); err != nil {
		t.Errorf("LoginAdmin failed because the auditor was unavailable: %v", err)
	}
}

func TestEffectivePermissionsIsDelegated(t *testing.T) {
	h := newHarness(t)
	h.permissions.keys = []string{"audit.read"}

	keys, err := h.service.EffectivePermissions(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("EffectivePermissions: %v", err)
	}
	if len(keys) != 1 || keys[0] != "audit.read" {
		t.Errorf("EffectivePermissions returned %v", keys)
	}
}
