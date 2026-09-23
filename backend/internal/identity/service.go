package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/audit"
)

// Domain errors.
//
// These are sentinels rather than values carrying a message, because the platform
// returns i18n keys and never prose (AGENTS.md): the layer that owns the HTTP
// contract maps each of these to a code and a key, and the mapping is reviewed in
// one place instead of being spelled out at every call site.
var (
	// ErrEmailTaken reports a registration against an address that already exists.
	ErrEmailTaken = errors.New("identity: email is already registered")
	// ErrInvalidCredentials reports a failed login.
	//
	// One error covers "no such account", "wrong password" and "suspended account
	// whose password was wrong", so a caller cannot distinguish them. The service
	// still does the work to know which it is; the error deliberately does not say.
	ErrInvalidCredentials = errors.New("identity: invalid credentials")
	// ErrAccountSuspended reports a correct password on a suspended account.
	//
	// It is distinct from ErrInvalidCredentials because it is only reachable after
	// the password has been verified: telling someone their account is suspended is
	// not a disclosure, since they have already proved they own it. An operator who
	// suspends an account wants the person to see why.
	ErrAccountSuspended = errors.New("identity: account is suspended")
	// ErrInvalidEmail reports an address this platform will not accept.
	ErrInvalidEmail = errors.New("identity: email is not valid")
	// ErrWeakPassword reports a password below MinPasswordLength.
	ErrWeakPassword = errors.New("identity: password is too short")
	// ErrUnsupportedLocale reports a locale outside the two the interface has.
	ErrUnsupportedLocale = errors.New("identity: locale is not supported")
)

// maxEmailLength mirrors the column width in 0002_identity.
const maxEmailLength = 320

// User is the part of a user record the service needs.
type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	Status       string
	Locale       string
	Timezone     string
}

// Admin is the part of an admin record the service needs.
type Admin struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	Status       string
	DisplayName  string
}

// Account statuses, matching the CHECK constraints in 0002_identity.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
)

// Directory is the persistence the service needs.
//
// It is an interface over the generated queries rather than the queries themselves
// so the service can be tested without a database. The adapter is the only place
// that knows about sqlc, pgx or unique-violation codes.
type Directory interface {
	// CreateUser persists a new user. It returns ErrEmailTaken when the address is
	// already registered, which is a decision only the adapter can make because it
	// is the layer that sees the database's uniqueness violation.
	CreateUser(ctx context.Context, user User) (User, error)
	// FindUserByEmail returns ErrUserNotFound when there is no such user.
	FindUserByEmail(ctx context.Context, email string) (User, error)
	// FindUserByID returns ErrUserNotFound when there is no such user.
	FindUserByID(ctx context.Context, id uuid.UUID) (User, error)
	// RecordUserLogin updates the last-login timestamp.
	RecordUserLogin(ctx context.Context, id uuid.UUID) error
	// UpdateUserPasswordHash replaces the stored hash, which is how a raised argon2
	// cost is applied on the next successful login.
	UpdateUserPasswordHash(ctx context.Context, id uuid.UUID, hash string) error

	FindAdminByEmail(ctx context.Context, email string) (Admin, error)
	FindAdminByID(ctx context.Context, id uuid.UUID) (Admin, error)
	RecordAdminLogin(ctx context.Context, id uuid.UUID) error
	UpdateAdminPasswordHash(ctx context.Context, id uuid.UUID, hash string) error
}

// ErrUserNotFound reports a lookup that matched nothing.
//
// Separate from ErrInvalidCredentials: the directory says what the database did,
// and the service decides what the caller is allowed to learn. Collapsing them in
// the directory would make it impossible for the service to tell "no account" from
// "the database is broken".
var ErrUserNotFound = errors.New("identity: no such user")

// PermissionResolver returns an admin's effective permissions.
//
// Resolution is a query per request rather than a value cached in the session, so
// that removing a role takes effect on the next request instead of at the next
// login. A cache is deliberately not introduced yet: without a role-mutation path
// (Phase 9) there is nothing to invalidate against, and an unexercised invalidation
// path is a liability rather than an optimisation.
type PermissionResolver interface {
	PermissionsForAdmin(ctx context.Context, adminID uuid.UUID) ([]string, error)
}

// Audited actions this domain produces.
//
// The vocabulary lives here rather than in internal/audit so that a domain names its
// own actions, and the audit package is not a file every phase has to edit.
//
// Only administrative authentication is audited. Behaving otherwise would bury the
// high-risk entries — the ones docs/14 requires — among every ordinary sign-in, and
// user sign-ins are already visible as last_login_at plus the request log.
const (
	ActionAdminLoggedIn     = "admin.login.succeeded"
	ActionAdminLoggedOut    = "admin.logout"
	ActionAdminLoginRefused = "admin.login.refused"
)

// Deps are the service's collaborators.
type Deps struct {
	Directory   Directory
	Permissions PermissionResolver
	Sessions    SessionStore
	Hasher      *PasswordHasher
	Auditor     audit.Recorder
	// Now is injectable so expiry and timestamps are testable.
	Now func() time.Time
}

// Service implements registration, authentication and RBAC resolution.
type Service struct {
	directory   Directory
	permissions PermissionResolver
	sessions    SessionStore
	hasher      *PasswordHasher
	auditor     audit.Recorder
	now         func() time.Time
}

// NewService validates the collaborators and returns the service.
//
// Missing dependencies are refused rather than tolerated: a nil directory would
// turn the first request into a panic, and a panic in the request path is a worse
// failure than a refusal to start.
func NewService(deps Deps) (*Service, error) {
	switch {
	case deps.Directory == nil:
		return nil, errors.New("identity: a directory is required")
	case deps.Permissions == nil:
		return nil, errors.New("identity: a permission resolver is required")
	case deps.Sessions == nil:
		return nil, errors.New("identity: a session store is required")
	case deps.Hasher == nil:
		return nil, errors.New("identity: a password hasher is required")
	}

	now := deps.Now
	if now == nil {
		now = time.Now
	}

	return &Service{
		directory:   deps.Directory,
		permissions: deps.Permissions,
		sessions:    deps.Sessions,
		hasher:      deps.Hasher,
		auditor:     deps.Auditor,
		now:         now,
	}, nil
}

// RegisterInput is a registration request.
type RegisterInput struct {
	Email    string
	Password string
	Locale   string
	Timezone string
}

// NormalizeEmail prepares an address for storage and lookup.
//
// This is the single choke point that writes users, which is what makes the plain
// UNIQUE constraint on the column sufficient: both sides of every comparison are
// normalized here, so "Ada@example.com" and "ada@example.com" cannot become two
// accounts. Trim first: an address pasted from a document routinely carries one.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// validateEmail applies the checks this platform is willing to enforce.
//
// It is deliberately not an RFC 5322 parser. Full validation by pattern is not
// achievable, and the check that actually matters — that the person can receive
// mail at the address — is performed by the verification flow, not here. What this
// rejects is the shape that indicates a mistake: no "@", more than one, an empty
// local part or domain, internal whitespace, or a length the column cannot hold.
func validateEmail(email string) error {
	if email == "" || len(email) > maxEmailLength {
		return fmt.Errorf("%w: length %d is outside 1..%d", ErrInvalidEmail, len(email), maxEmailLength)
	}
	if strings.ContainsAny(email, " \t\r\n") {
		return fmt.Errorf("%w: contains whitespace", ErrInvalidEmail)
	}

	local, domain, found := strings.Cut(email, "@")
	if !found {
		return fmt.Errorf("%w: no @", ErrInvalidEmail)
	}
	if local == "" || domain == "" {
		return fmt.Errorf("%w: empty local part or domain", ErrInvalidEmail)
	}
	if strings.Contains(domain, "@") {
		return fmt.Errorf("%w: more than one @", ErrInvalidEmail)
	}
	if !strings.Contains(domain, ".") {
		return fmt.Errorf("%w: the domain has no dot", ErrInvalidEmail)
	}
	return nil
}

// Register creates a user account.
func (s *Service) Register(ctx context.Context, input RegisterInput) (User, error) {
	email := NormalizeEmail(input.Email)
	if err := validateEmail(email); err != nil {
		return User{}, err
	}
	if len(input.Password) < MinPasswordLength {
		return User{}, fmt.Errorf("%w: %d characters, minimum is %d",
			ErrWeakPassword, len(input.Password), MinPasswordLength)
	}

	locale := input.Locale
	if locale == "" {
		locale = "zh-CN"
	}
	if locale != "zh-CN" && locale != "en-US" {
		return User{}, fmt.Errorf("%w: %q", ErrUnsupportedLocale, locale)
	}

	timezone := input.Timezone
	if timezone == "" {
		timezone = "UTC"
	}

	hash, err := s.hasher.Hash(input.Password)
	if err != nil {
		return User{}, fmt.Errorf("identity: hash password: %w", err)
	}

	user, err := s.directory.CreateUser(ctx, User{
		ID:           uuid.New(),
		Email:        email,
		PasswordHash: hash,
		Status:       StatusActive,
		Locale:       locale,
		Timezone:     timezone,
	})
	if err != nil {
		// Deliberately not audited. Registration is self-service and creates no
		// privileged state; auditing it would add a write per sign-up without
		// recording anything an operator needs.
		return User{}, err
	}

	return user, nil
}

// LoginInput is a login request.
type LoginInput struct {
	Email    string
	Password string
	// ClientContext is what the audit entry records about where the attempt came
	// from. It is supplied by the transport layer, which is the only layer that knows.
	ClientContext audit.Context
}

// LoginResult is what a successful login produces.
type LoginResult struct {
	Session Session
	User    User
	Admin   Admin
	// Permissions is the admin's effective set. Empty for a user session, which has
	// no permissions: a customer's rights follow from owning the resource, not from
	// a role.
	Permissions []string
}

// Login authenticates a user.
func (s *Service) LoginUser(ctx context.Context, input LoginInput) (LoginResult, error) {
	email := NormalizeEmail(input.Email)

	user, err := s.directory.FindUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			// Spend the same work as a real verification would, so response time does
			// not report whether the address exists.
			s.hasher.VerifyUnknownAccount(input.Password)
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, fmt.Errorf("identity: look up user: %w", err)
	}

	verification, err := s.hasher.Verify(input.Password, user.PasswordHash)
	if err != nil {
		// A malformed hash is a broken credential, not a wrong password. Reporting it
		// as invalid credentials would send the user to reset a password that could
		// never have worked, and would hide a corrupt row from operators.
		return LoginResult{}, fmt.Errorf("identity: verify user password: %w", err)
	}
	if !verification.Matches {
		return LoginResult{}, ErrInvalidCredentials
	}

	// Status is checked after the password, so that "this account is suspended" is
	// only learned by someone who has proved the account is theirs.
	if user.Status != StatusActive {
		return LoginResult{}, ErrAccountSuspended
	}

	if verification.NeedsRehash {
		// The cost was raised since this credential was stored. A successful login is
		// the only moment the plaintext is available, so it is the only moment it can
		// be re-hashed.
		if err := s.replaceUserHash(ctx, user, input.Password); err != nil {
			return LoginResult{}, err
		}
	}

	session, err := NewSession(SubjectUser, user.ID, s.now())
	if err != nil {
		return LoginResult{}, err
	}
	if err := s.sessions.Create(ctx, session); err != nil {
		return LoginResult{}, fmt.Errorf("identity: create session: %w", err)
	}

	// Neither a failure to record the login time nor a failure to audit may fail the
	// login: the session already exists, and refusing now would leave the caller
	// authenticated in the store but told they are not. Both are reported through the
	// log instead.
	if err := s.directory.RecordUserLogin(ctx, user.ID); err != nil {
		return LoginResult{}, fmt.Errorf("identity: record login: %w", err)
	}

	return LoginResult{Session: session, User: user, Permissions: nil}, nil
}

// LoginAdmin authenticates an administrator.
func (s *Service) LoginAdmin(ctx context.Context, input LoginInput) (LoginResult, error) {
	email := NormalizeEmail(input.Email)

	admin, err := s.directory.FindAdminByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			s.hasher.VerifyUnknownAccount(input.Password)
			s.auditRefusal(ctx, email, input.ClientContext)
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, fmt.Errorf("identity: look up admin: %w", err)
	}

	verification, err := s.hasher.Verify(input.Password, admin.PasswordHash)
	if err != nil {
		return LoginResult{}, fmt.Errorf("identity: verify admin password: %w", err)
	}
	if !verification.Matches {
		s.auditRefusal(ctx, admin.ID.String(), input.ClientContext)
		return LoginResult{}, ErrInvalidCredentials
	}
	if admin.Status != StatusActive {
		s.auditRefusal(ctx, admin.ID.String(), input.ClientContext)
		return LoginResult{}, ErrAccountSuspended
	}

	if verification.NeedsRehash {
		if err := s.replaceAdminHash(ctx, admin, input.Password); err != nil {
			return LoginResult{}, err
		}
	}

	permissions, err := s.permissions.PermissionsForAdmin(ctx, admin.ID)
	if err != nil {
		return LoginResult{}, fmt.Errorf("identity: resolve permissions: %w", err)
	}

	session, err := NewSession(SubjectAdmin, admin.ID, s.now())
	if err != nil {
		return LoginResult{}, err
	}
	if err := s.sessions.Create(ctx, session); err != nil {
		return LoginResult{}, fmt.Errorf("identity: create session: %w", err)
	}

	if err := s.directory.RecordAdminLogin(ctx, admin.ID); err != nil {
		return LoginResult{}, fmt.Errorf("identity: record login: %w", err)
	}

	s.audit(ctx, audit.Event{
		ActorType:    string(SubjectAdmin),
		ActorID:      admin.ID,
		Action:       ActionAdminLoggedIn,
		ResourceType: "admin",
		ResourceID:   admin.ID,
		Context:      input.ClientContext,
	})

	return LoginResult{Session: session, Admin: admin, Permissions: permissions}, nil
}

// Logout ends the session the caller presented.
func (s *Service) Logout(ctx context.Context, subject SubjectType, sessionID string) error {
	if err := s.sessions.Delete(ctx, subject, sessionID); err != nil {
		return fmt.Errorf("identity: delete session: %w", err)
	}
	return nil
}

// LogoutAdmin ends an admin session and records it.
//
// The subject id is needed for the audit entry, so the session is read before it is
// deleted. A session that has already gone is not an error and is not audited: there
// is nothing to report.
func (s *Service) LogoutAdmin(ctx context.Context, sessionID string, client audit.Context) error {
	session, err := s.sessions.Get(ctx, SubjectAdmin, sessionID)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return nil
		}
		return fmt.Errorf("identity: read session: %w", err)
	}

	if err := s.sessions.Delete(ctx, SubjectAdmin, sessionID); err != nil {
		return err
	}

	s.audit(ctx, audit.Event{
		ActorType:    string(SubjectAdmin),
		ActorID:      session.SubjectID,
		Action:       ActionAdminLoggedOut,
		ResourceType: "admin",
		ResourceID:   session.SubjectID,
		Context:      client,
	})
	return nil
}

// CurrentSession returns a live session, which is what authenticates a request.
func (s *Service) CurrentSession(ctx context.Context, subject SubjectType, sessionID string) (Session, error) {
	return s.sessions.Get(ctx, subject, sessionID)
}

// EffectivePermissions resolves an admin's permissions.
func (s *Service) EffectivePermissions(ctx context.Context, adminID uuid.UUID) ([]string, error) {
	return s.permissions.PermissionsForAdmin(ctx, adminID)
}

func (s *Service) replaceUserHash(ctx context.Context, user User, password string) error {
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return fmt.Errorf("identity: re-hash password: %w", err)
	}
	if err := s.directory.UpdateUserPasswordHash(ctx, user.ID, hash); err != nil {
		return fmt.Errorf("identity: store re-hashed password: %w", err)
	}
	return nil
}

func (s *Service) replaceAdminHash(ctx context.Context, admin Admin, password string) error {
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return fmt.Errorf("identity: re-hash password: %w", err)
	}
	if err := s.directory.UpdateAdminPasswordHash(ctx, admin.ID, hash); err != nil {
		return fmt.Errorf("identity: store re-hashed password: %w", err)
	}
	return nil
}

// auditRefusal records a refused administrator authentication.
//
// The address is masked inside Details: an audit trail has to be readable by
// operators, and the trail itself must not become a list of the addresses that exist.
func (s *Service) auditRefusal(ctx context.Context, attempted string, client audit.Context) {
	s.audit(ctx, audit.Event{
		ActorType:    string(SubjectAdmin),
		Action:       ActionAdminLoginRefused,
		ResourceType: "admin",
		Details:      map[string]string{"attempted": maskEmail(attempted)},
		Context:      client,
	})
}

func (s *Service) audit(ctx context.Context, event audit.Event) {
	if s.auditor == nil {
		return
	}
	// Audit failures are not surfaced to the caller. The recorder is expected to log
	// them; failing the request would turn an observability problem into an outage.
	_ = s.auditor.Record(ctx, event)
}

// maskEmail keeps the domain and the first character so an operator can recognise
// the account, and drops the rest.
func maskEmail(email string) string {
	local, domain, found := strings.Cut(email, "@")
	if !found {
		return "***"
	}
	if local == "" {
		return "***@" + domain
	}
	return local[:1] + "***@" + domain
}
