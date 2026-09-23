package redisx

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
)

// fakeKV is an in-memory stand-in for Redis.
//
// The store's own behaviour is what these tests are about — the key layout, the
// per-subject index, and the difference between ending one session and ending every
// session of a subject. None of that needs a server, and this machine has none
// (ADR-003), so the fake is what makes the logic verifiable at all.
type fakeKV struct {
	values map[string]string
	sets   map[string]map[string]struct{}
	ttls   map[string]time.Duration

	// failGet makes every read fail, so the error paths can be exercised.
	failGet error
}

func newFakeKV() *fakeKV {
	return &fakeKV{
		values: map[string]string{},
		sets:   map[string]map[string]struct{}{},
		ttls:   map[string]time.Duration{},
	}
}

func (f *fakeKV) Set(_ context.Context, key, value string, ttl time.Duration) error {
	f.values[key] = value
	f.ttls[key] = ttl
	return nil
}

func (f *fakeKV) Get(_ context.Context, key string) (string, error) {
	if f.failGet != nil {
		return "", f.failGet
	}
	value, ok := f.values[key]
	if !ok {
		return "", identity.ErrSessionNotFound
	}
	return value, nil
}

func (f *fakeKV) Delete(_ context.Context, keys ...string) error {
	for _, key := range keys {
		delete(f.values, key)
		delete(f.sets, key)
		delete(f.ttls, key)
	}
	return nil
}

func (f *fakeKV) Expire(_ context.Context, key string, ttl time.Duration) error {
	f.ttls[key] = ttl
	return nil
}

func (f *fakeKV) SetAdd(_ context.Context, key string, members ...string) error {
	if f.sets[key] == nil {
		f.sets[key] = map[string]struct{}{}
	}
	for _, member := range members {
		f.sets[key][member] = struct{}{}
	}
	return nil
}

func (f *fakeKV) SetRemove(_ context.Context, key string, members ...string) error {
	for _, member := range members {
		delete(f.sets[key], member)
	}
	return nil
}

func (f *fakeKV) SetMembers(_ context.Context, key string) ([]string, error) {
	members := make([]string, 0, len(f.sets[key]))
	for member := range f.sets[key] {
		members = append(members, member)
	}
	// Sorted so assertions are stable; Redis returns an unordered set.
	sort.Strings(members)
	return members, nil
}

func newTestStore(t *testing.T) (*SessionStore, *fakeKV) {
	t.Helper()
	kv := newFakeKV()
	return newSessionStoreWithKV(kv), kv
}

func mustSession(t *testing.T, subject identity.SubjectType, subjectID uuid.UUID) identity.Session {
	t.Helper()
	session, err := identity.NewSession(subject, subjectID, time.Now())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return session
}

func mustJSON(t *testing.T, session identity.Session) string {
	t.Helper()
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	return string(encoded)
}

func TestSessionRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, kv := newTestStore(t)
	subjectID := uuid.New()
	session := mustSession(t, identity.SubjectUser, subjectID)

	if err := store.Create(ctx, session); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Get(ctx, identity.SubjectUser, session.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != session.ID || got.SubjectID != subjectID || got.CSRFSecret != session.CSRFSecret {
		t.Errorf("the stored session does not match the issued one: %+v", got)
	}

	// The record must be reachable under the namespaced key, and the index must
	// exist, because revocation depends on it.
	if _, ok := kv.values[sessionKey(identity.SubjectUser, session.ID)]; !ok {
		t.Error("the session was not stored under its namespaced key")
	}
	members, _ := kv.SetMembers(ctx, subjectSessionsKey(identity.SubjectUser, subjectID))
	if len(members) != 1 || members[0] != session.ID {
		t.Errorf("the subject index does not contain the session: %v", members)
	}
}

func TestSessionIsNotReadableFromTheOtherCredentialSpace(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	subjectID := uuid.New()

	// A user session presented as an admin session. This is the isolation docs/14
	// requires, and it has to be refused by the lookup itself rather than by a
	// later permission check.
	userSession := mustSession(t, identity.SubjectUser, subjectID)
	if err := store.Create(ctx, userSession); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := store.Get(ctx, identity.SubjectAdmin, userSession.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Errorf("an admin lookup returned %v for a user session", err)
	}

	adminSession := mustSession(t, identity.SubjectAdmin, subjectID)
	if err := store.Create(ctx, adminSession); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Get(ctx, identity.SubjectUser, adminSession.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Errorf("a user lookup returned %v for an admin session", err)
	}

	// Both must remain valid in their own space, so the refusals above are about
	// the space and not about the sessions being unusable.
	if _, err := store.Get(ctx, identity.SubjectUser, userSession.ID); err != nil {
		t.Errorf("the user session stopped working: %v", err)
	}
	if _, err := store.Get(ctx, identity.SubjectAdmin, adminSession.ID); err != nil {
		t.Errorf("the admin session stopped working: %v", err)
	}
}

func TestGetRejectsExpiredAndMalformedInput(t *testing.T) {
	ctx := context.Background()
	store, kv := newTestStore(t)
	subjectID := uuid.New()

	expired := mustSession(t, identity.SubjectUser, subjectID)
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	// Written directly, because Create refuses an expired session: the point here is
	// that a record which is already dead cannot become a valid credential.
	kv.values[sessionKey(identity.SubjectUser, expired.ID)] = mustJSON(t, expired)

	if _, err := store.Get(ctx, identity.SubjectUser, expired.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Errorf("an expired session returned %v", err)
	}

	for _, id := range []string{"", "no-such-session"} {
		if _, err := store.Get(ctx, identity.SubjectUser, id); !errors.Is(err, identity.ErrSessionNotFound) {
			t.Errorf("Get(%q) returned %v", id, err)
		}
	}
	if _, err := store.Get(ctx, "provider", "anything"); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Errorf("an unknown subject type returned %v", err)
	}
}

func TestGetToleratesAnUndecodableRecord(t *testing.T) {
	ctx := context.Background()
	store, kv := newTestStore(t)
	subjectID := uuid.New()

	session := mustSession(t, identity.SubjectUser, subjectID)
	kv.values[sessionKey(identity.SubjectUser, session.ID)] = "{ not json"

	// Treated as absent rather than as an error: the caller's only sensible response
	// is to authenticate again, and an error would turn one corrupt row into a
	// failing request.
	if _, err := store.Get(ctx, identity.SubjectUser, session.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Errorf("a corrupt record returned %v", err)
	}
}

func TestCreateRejectsUnstorableSessions(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	subjectID := uuid.New()

	expired := mustSession(t, identity.SubjectUser, subjectID)
	expired.ExpiresAt = time.Now().Add(-time.Second)
	if err := store.Create(ctx, expired); err == nil {
		t.Error("expected an already expired session to be refused")
	}

	unknown := mustSession(t, identity.SubjectUser, subjectID)
	unknown.Subject = "provider"
	if err := store.Create(ctx, unknown); err == nil {
		t.Error("expected an unknown subject type to be refused")
	}
}

func TestDeleteEndsOneSessionAndDeindexesIt(t *testing.T) {
	ctx := context.Background()
	store, kv := newTestStore(t)
	subjectID := uuid.New()

	first := mustSession(t, identity.SubjectUser, subjectID)
	second := mustSession(t, identity.SubjectUser, subjectID)
	for _, session := range []identity.Session{first, second} {
		if err := store.Create(ctx, session); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	if err := store.Delete(ctx, identity.SubjectUser, first.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, identity.SubjectUser, first.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Error("the deleted session is still readable")
	}
	if _, err := store.Get(ctx, identity.SubjectUser, second.ID); err != nil {
		t.Errorf("deleting one session ended another: %v", err)
	}

	members, _ := kv.SetMembers(ctx, subjectSessionsKey(identity.SubjectUser, subjectID))
	if len(members) != 1 || members[0] != second.ID {
		t.Errorf("the index still lists the deleted session: %v", members)
	}

	// Deleting twice must succeed: a logout that races the expiry, or a retried
	// request, is not an error.
	if err := store.Delete(ctx, identity.SubjectUser, first.ID); err != nil {
		t.Errorf("deleting an already deleted session returned %v", err)
	}
}

func TestDeleteForSubjectEndsEverySessionOfThatSubjectOnly(t *testing.T) {
	ctx := context.Background()
	store, kv := newTestStore(t)
	target := uuid.New()
	other := uuid.New()

	var revoked []string
	for i := 0; i < 3; i++ {
		session := mustSession(t, identity.SubjectUser, target)
		if err := store.Create(ctx, session); err != nil {
			t.Fatalf("Create: %v", err)
		}
		revoked = append(revoked, session.ID)
	}

	// A session of a different subject, and one of the same subject in the other
	// credential space: neither may be affected by the revocation below.
	otherSession := mustSession(t, identity.SubjectUser, other)
	if err := store.Create(ctx, otherSession); err != nil {
		t.Fatalf("Create: %v", err)
	}
	adminSession := mustSession(t, identity.SubjectAdmin, target)
	if err := store.Create(ctx, adminSession); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.DeleteForSubject(ctx, identity.SubjectUser, target); err != nil {
		t.Fatalf("DeleteForSubject: %v", err)
	}

	for _, id := range revoked {
		if _, err := store.Get(ctx, identity.SubjectUser, id); !errors.Is(err, identity.ErrSessionNotFound) {
			t.Errorf("a session of the revoked subject survived: %v", err)
		}
	}
	if _, err := store.Get(ctx, identity.SubjectUser, otherSession.ID); err != nil {
		t.Errorf("revoking one subject ended another subject's session: %v", err)
	}
	if _, err := store.Get(ctx, identity.SubjectAdmin, adminSession.ID); err != nil {
		t.Errorf("revoking a user's sessions ended their admin session: %v", err)
	}

	if members := kv.sets[subjectSessionsKey(identity.SubjectUser, target)]; len(members) != 0 {
		t.Errorf("the revoked subject's index was not cleared: %v", members)
	}
}

func TestRevocationPropagatesAReadFailure(t *testing.T) {
	ctx := context.Background()
	store, kv := newTestStore(t)
	kv.failGet = errors.New("connection reset")

	// A revocation that cannot confirm the sessions must not report success: a
	// suspension that silently failed to sign anyone out is a security failure, and
	// the caller has to be able to tell.
	if _, err := store.Get(ctx, identity.SubjectUser, "any"); err == nil {
		t.Error("expected the read failure to be reported")
	}
	if err := store.Delete(ctx, identity.SubjectUser, "any"); err == nil {
		t.Error("expected Delete to report the read failure")
	}
}
