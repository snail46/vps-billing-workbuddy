package identity

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrSessionNotFound reports a session that does not exist, has expired, or belongs
// to the other credential space.
//
// The three cases deliberately share one error. Distinguishing "no such session"
// from "this is an admin session and you presented it as a user" would tell a
// caller which of the two credential spaces a token belongs to, which is exactly
// the information the separation exists to withhold.
var ErrSessionNotFound = errors.New("identity: session not found")

// SubjectType names the credential space a session belongs to.
//
// Two values rather than one "authenticated" flag: docs/14 requires user, admin and
// provider credentials to be completely isolated, and the isolation is enforced by
// each space having its own cookie, its own storage namespace and its own subject
// type. A user session is therefore not a weaker admin session — it is a session
// that admin code never looks at.
type SubjectType string

const (
	SubjectUser  SubjectType = "user"
	SubjectAdmin SubjectType = "admin"
)

// Valid reports whether the subject type is one this platform issues.
func (s SubjectType) Valid() bool { return s == SubjectUser || s == SubjectAdmin }

// Session lifetimes.
//
// docs/14 requires a session cookie but fixes no lifetime, so these are stated
// defaults. The admin lifetime is much shorter than the user's because of what the
// credential protects: a stolen user session exposes one customer's account, while
// a stolen admin session exposes every customer's data and the controls over it.
//
// Renewal is deliberately not sliding. Extending on activity would mean a write per
// request and would let a session live indefinitely, which is the property a fixed
// expiry exists to bound.
const (
	UserSessionTTL  = 30 * 24 * time.Hour
	AdminSessionTTL = 24 * time.Hour
)

// SessionTTL returns the lifetime for a credential space.
func SessionTTL(subject SubjectType) (time.Duration, error) {
	switch subject {
	case SubjectUser:
		return UserSessionTTL, nil
	case SubjectAdmin:
		return AdminSessionTTL, nil
	default:
		return 0, fmt.Errorf("identity: unknown subject type %q", subject)
	}
}

// Session is the server-side state behind a session cookie.
type Session struct {
	// ID is the opaque value carried in the cookie. It is not a UUID: it carries no
	// meaning and no ordering, so it discloses nothing to whoever holds the cookie.
	ID string `json:"id"`
	// Subject and SubjectID say who the session authenticates.
	Subject   SubjectType `json:"subject"`
	SubjectID uuid.UUID   `json:"subject_id"`
	// IssuedAt and ExpiresAt are recorded so a verifier can check expiry itself
	// rather than trusting that the store has already evicted the record.
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// CSRFSecret is the synchroniser token for this session (ADR-004). It lives with
	// the session rather than in a second cookie because sessions are server-side
	// already, and a second cookie would be a second, weaker channel to protect.
	CSRFSecret string `json:"csrf_secret"`
}

// NewSession issues a session for a subject.
//
// The identifiers are generated here rather than by the store so that a session
// created outside Redis — in a test, for instance — is indistinguishable from one
// created through it.
func NewSession(subject SubjectType, subjectID uuid.UUID, now time.Time) (Session, error) {
	if !subject.Valid() {
		return Session{}, fmt.Errorf("identity: cannot issue a session for subject type %q", subject)
	}
	if subjectID == uuid.Nil {
		return Session{}, errors.New("identity: cannot issue a session without a subject id")
	}

	ttl, err := SessionTTL(subject)
	if err != nil {
		return Session{}, err
	}

	return Session{
		ID:         newOpaqueToken(),
		Subject:    subject,
		SubjectID:  subjectID,
		IssuedAt:   now.UTC(),
		ExpiresAt:  now.UTC().Add(ttl),
		CSRFSecret: newOpaqueToken(),
	}, nil
}

// IsExpired reports whether the session has passed its expiry at the given time.
//
// Expiry is checked rather than assumed even though the store applies a TTL to the
// same instant: a store that fails to evict — or a session reconstructed from a
// backup — must not become a permanently valid credential.
func (s Session) IsExpired(now time.Time) bool {
	return !s.ExpiresAt.After(now.UTC())
}

// SessionStore persists sessions.
//
// The interface is expressed in terms of this package's types and holds no Redis
// concept, so the rules above are testable without a Redis server. The Redis-backed
// implementation lives in internal/redisx, which is the package that owns Redis.
type SessionStore interface {
	// Create stores a session until its expiry.
	Create(ctx context.Context, session Session) error
	// Get returns a live session, or ErrSessionNotFound.
	Get(ctx context.Context, subject SubjectType, id string) (Session, error)
	// Delete ends one session.
	Delete(ctx context.Context, subject SubjectType, id string) error
	// DeleteForSubject ends every session belonging to a subject, which is what a
	// suspension, a password change or an administrative sign-out needs.
	DeleteForSubject(ctx context.Context, subject SubjectType, subjectID uuid.UUID) error
}

// newOpaqueToken returns 32 random bytes, URL-safe and unpadded.
//
// 256 bits is far beyond guessable, and an unpadded URL-safe alphabet means the
// value can be put in a cookie without escaping.
func newOpaqueToken() string {
	return base64.RawURLEncoding.EncodeToString(randomBytes(32))
}
