package identity

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewSessionIssuesDistinctCredentials(t *testing.T) {
	subject := uuid.MustParse("0198f1c2-0000-7000-8000-000000000001")
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	first, err := NewSession(SubjectUser, subject, now)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	second, err := NewSession(SubjectUser, subject, now)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if first.ID == "" || first.CSRFSecret == "" {
		t.Fatal("a session must carry both an id and a CSRF secret")
	}
	// Two sessions for the same subject must not share an identifier, or revoking
	// one would revoke the other and the cookie value would be guessable from a
	// previous session.
	if first.ID == second.ID {
		t.Error("two sessions were issued the same id")
	}
	if first.CSRFSecret == second.CSRFSecret {
		t.Error("two sessions were issued the same CSRF secret")
	}
	if first.ID == first.CSRFSecret {
		t.Error("the CSRF secret must not equal the session id")
	}

	if !first.IssuedAt.Equal(now) {
		t.Errorf("IssuedAt = %v, want %v", first.IssuedAt, now)
	}
	if first.Subject != SubjectUser || first.SubjectID != subject {
		t.Errorf("the session records the wrong subject: %+v", first)
	}
}

func TestNewSessionRejectsUnusableSubjects(t *testing.T) {
	subject := uuid.MustParse("0198f1c2-0000-7000-8000-000000000001")
	now := time.Now()

	// A provider credential is a subject type the platform has (docs/14 names three
	// isolated spaces) but that does not authenticate with a session cookie, so a
	// session must not be issuable for it. A real subject id is supplied so the
	// refusal is attributable to the subject type alone.
	for _, subjectType := range []SubjectType{"provider", "", "USER", "user "} {
		if _, err := NewSession(subjectType, subject, now); err == nil {
			t.Errorf("expected subject type %q to be rejected", subjectType)
		}
	}

	if _, err := NewSession(SubjectUser, uuid.Nil, now); err == nil {
		t.Error("expected a session without a subject id to be rejected")
	}
}

func TestSessionLifetimeIsShorterForAdmins(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	subject := uuid.New()

	user, err := NewSession(SubjectUser, subject, now)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	admin, err := NewSession(SubjectAdmin, subject, now)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if got := user.ExpiresAt.Sub(now); got != UserSessionTTL {
		t.Errorf("user session lifetime = %v, want %v", got, UserSessionTTL)
	}
	if got := admin.ExpiresAt.Sub(now); got != AdminSessionTTL {
		t.Errorf("admin session lifetime = %v, want %v", got, AdminSessionTTL)
	}
	// The credential that can reach every customer must not outlive the one that
	// reaches a single customer.
	if admin.ExpiresAt.After(user.ExpiresAt) {
		t.Error("an admin session outlives a user session")
	}

	if _, err := SessionTTL("provider"); err == nil {
		t.Error("expected an unknown subject type to have no lifetime")
	}
}

func TestSessionIsExpired(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	session, err := NewSession(SubjectUser, uuid.New(), now)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if session.IsExpired(now) {
		t.Error("a session must not be expired at the moment it is issued")
	}
	// The expiry instant itself counts as expired: a credential valid exactly at its
	// deadline would make the boundary depend on clock resolution.
	if !session.IsExpired(session.ExpiresAt) {
		t.Error("a session must be expired at its expiry instant")
	}
	if !session.IsExpired(session.ExpiresAt.Add(time.Nanosecond)) {
		t.Error("a session must be expired after its expiry instant")
	}
}
