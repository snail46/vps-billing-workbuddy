package redisx

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
)

// The integration tier. It needs a real Redis and is skipped without one, because what it
// covers is the store's behaviour: whether a record survives a round trip, whether an
// expired one is refused, whether revocation reaches every session of a subject, and whether
// the transaction that sets a counter and its expiry commits at all. None of that can be
// established against a fake, and all of it is load-bearing — every authenticated request
// reads a session from here.
//
// The CI integration job starts Redis and sets TEST_REDIS_URL. Locally the tests are skipped
// (ADR-003).
//
// Nothing is flushed and nothing is cleaned up at the end: every key these tests write has a
// TTL of its own, and the keys are namespaced by random identifiers, so a run cannot collide
// with another or leave anything behind. A FlushDB would be destructive on a developer's own
// Redis, which is exactly the kind of test that gets disabled after it eats someone's data.

// redisForIntegration connects, or skips the test.
func redisForIntegration(t *testing.T) *redis.Client {
	t.Helper()

	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL is not set; integration test skipped")
	}

	client, err := NewClient(context.Background(), Options{URL: url, DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("connect to Redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestSessionStoreRoundTrip(t *testing.T) {
	client := redisForIntegration(t)
	store := NewSessionStore(client)
	ctx := context.Background()

	subjectID := uuid.New()
	session, err := identity.NewSession(identity.SubjectUser, subjectID, time.Now())
	if err != nil {
		t.Fatalf("build session: %v", err)
	}

	if err := store.Create(ctx, session); err != nil {
		t.Fatalf("create: %v", err)
	}

	read, err := store.Get(ctx, identity.SubjectUser, session.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	// Every field has to survive the encoding, and the CSRF secret especially: a session
	// that came back without it would authenticate and then refuse every mutating request.
	if read.ID != session.ID || read.SubjectID != subjectID || read.Subject != identity.SubjectUser {
		t.Errorf("the session changed in storage: %+v", read)
	}
	if read.CSRFSecret != session.CSRFSecret {
		t.Error("the CSRF secret did not survive the round trip")
	}
	if !read.IssuedAt.Truncate(time.Second).Equal(session.IssuedAt.Truncate(time.Second)) {
		t.Errorf("issued_at = %s, expected %s", read.IssuedAt, session.IssuedAt)
	}

	// The other credential space must not find it. The subject type is part of the key, so
	// this is a miss rather than a record whose subject is then compared.
	if _, err := store.Get(ctx, identity.SubjectAdmin, session.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Errorf("an admin lookup found a user session: %v", err)
	}

	if err := store.Delete(ctx, identity.SubjectUser, session.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, identity.SubjectUser, session.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Error("the session survived deletion")
	}
}

func TestSessionStoreRefusesToStoreAnExpiredSession(t *testing.T) {
	store := NewSessionStore(redisForIntegration(t))
	ctx := context.Background()

	session, err := identity.NewSession(identity.SubjectUser, uuid.New(), time.Now().Add(-2*identity.UserSessionTTL))
	if err != nil {
		t.Fatalf("build session: %v", err)
	}

	// Refused rather than stored with a TTL that has already elapsed. Writing it would
	// create a record that is dead on arrival, and a caller that did that has a bug worth
	// hearing about instead of a silently useless credential.
	if err := store.Create(ctx, session); err == nil {
		t.Fatal("an already-expired session was stored")
	}
	if _, err := store.Get(ctx, identity.SubjectUser, session.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Errorf("the refused session is readable: %v", err)
	}
}

func TestSessionStoreRefusesAnExpiredRecordOnRead(t *testing.T) {
	client := redisForIntegration(t)
	store := NewSessionStore(client)
	ctx := context.Background()

	// Placed directly in Redis, bypassing Create, because the case being covered is a
	// record the store did not write: one left behind by a failed eviction, or restored
	// from a backup. The key has no expiry of its own here, which is the point — the read
	// path has to enforce the lifetime rather than trust the store to have done it.
	expired, err := identity.NewSession(identity.SubjectUser, uuid.New(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("build session: %v", err)
	}
	encoded, err := json.Marshal(expired)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := client.Set(ctx, sessionKey(identity.SubjectUser, expired.ID), string(encoded), 0).Err(); err != nil {
		t.Fatalf("place the record: %v", err)
	}
	t.Cleanup(func() { _ = client.Del(context.Background(), sessionKey(identity.SubjectUser, expired.ID)).Err() })

	if _, err := store.Get(ctx, identity.SubjectUser, expired.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Errorf("an expired record was accepted: %v", err)
	}
}

func TestSessionStoreRefusesARecordFromTheOtherSpace(t *testing.T) {
	client := redisForIntegration(t)
	store := NewSessionStore(client)
	ctx := context.Background()

	// A user session written to an admin key. The layout makes this unreachable through the
	// store, so the check is defence in depth against the two ever disagreeing — and the
	// safe outcome when they do is to deny.
	session, err := identity.NewSession(identity.SubjectUser, uuid.New(), time.Now())
	if err != nil {
		t.Fatalf("build session: %v", err)
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	key := sessionKey(identity.SubjectAdmin, session.ID)
	if err := client.Set(ctx, key, string(encoded), time.Minute).Err(); err != nil {
		t.Fatalf("place the record: %v", err)
	}
	t.Cleanup(func() { _ = client.Del(context.Background(), key).Err() })

	if _, err := store.Get(ctx, identity.SubjectAdmin, session.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Errorf("a user session was accepted as an admin session: %v", err)
	}
}

func TestSessionStoreRevokesEverySessionOfASubject(t *testing.T) {
	store := NewSessionStore(redisForIntegration(t))
	ctx := context.Background()

	target := uuid.New()
	other := uuid.New()

	first, _ := identity.NewSession(identity.SubjectUser, target, time.Now())
	second, _ := identity.NewSession(identity.SubjectUser, target, time.Now())
	unrelated, _ := identity.NewSession(identity.SubjectUser, other, time.Now())
	admin, _ := identity.NewSession(identity.SubjectAdmin, target, time.Now())

	for _, session := range []identity.Session{first, second, unrelated, admin} {
		if err := store.Create(ctx, session); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	// A suspension, a password change or an administrative sign-out all take this path.
	if err := store.DeleteForSubject(ctx, identity.SubjectUser, target); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	for _, id := range []string{first.ID, second.ID} {
		if _, err := store.Get(ctx, identity.SubjectUser, id); !errors.Is(err, identity.ErrSessionNotFound) {
			t.Errorf("a session of the revoked subject survived: %s", id)
		}
	}
	// Scoped to one subject in one space: another customer's session, and the same person's
	// administrator session, are untouched.
	if _, err := store.Get(ctx, identity.SubjectUser, unrelated.ID); err != nil {
		t.Errorf("revoking one subject ended another's session: %v", err)
	}
	if _, err := store.Get(ctx, identity.SubjectAdmin, admin.ID); err != nil {
		t.Errorf("revoking a user's sessions ended their admin session: %v", err)
	}
}

func TestSessionStoreDeleteIsIdempotent(t *testing.T) {
	store := NewSessionStore(redisForIntegration(t))
	ctx := context.Background()

	session, _ := identity.NewSession(identity.SubjectUser, uuid.New(), time.Now())
	if err := store.Create(ctx, session); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Delete(ctx, identity.SubjectUser, session.ID); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	// A logout that races the expiry, or a retried request, is not an error.
	if err := store.Delete(ctx, identity.SubjectUser, session.ID); err != nil {
		t.Errorf("second delete returned %v", err)
	}
}

func TestAttemptCounterCountsResetsAndExpires(t *testing.T) {
	counter := NewAttemptCounter(redisForIntegration(t))
	ctx := context.Background()

	// A scope unique to this run, so a previous run's keys cannot affect the count.
	scope := authmw.Scope("test_" + randomToken())
	key := authmw.CounterKeyLayout(scope, "account", "ada@example.com")

	for want := int64(1); want <= 3; want++ {
		got, err := counter.Incr(ctx, key, time.Minute)
		if err != nil {
			t.Fatalf("increment: %v", err)
		}
		if got != want {
			t.Fatalf("count = %d, expected %d", got, want)
		}
	}

	if err := counter.Reset(ctx, key); err != nil {
		t.Fatalf("reset: %v", err)
	}

	// Cleared rather than decremented: the budget covers the attempts since the last
	// successful one, so the next attempt starts from one.
	got, err := counter.Incr(ctx, key, time.Minute)
	if err != nil {
		t.Fatalf("increment after reset: %v", err)
	}
	if got != 1 {
		t.Errorf("count after reset = %d, expected 1", got)
	}

	// The expiry is applied by the same transaction as the count, which is what keeps an
	// exhausted budget from becoming permanent. A one-second window is the only way to
	// observe that without waiting minutes.
	shortKey := authmw.CounterKeyLayout(scope, "ip", "203.0.113.7")
	if _, err := counter.Incr(ctx, shortKey, time.Second); err != nil {
		t.Fatalf("increment: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	got, err = counter.Incr(ctx, shortKey, time.Second)
	if err != nil {
		t.Fatalf("increment after the window elapsed: %v", err)
	}
	if got != 1 {
		t.Errorf("count after the window elapsed = %d, expected 1; the key had no expiry", got)
	}
}

// randomToken keeps one run's keys out of another's way.
func randomToken() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
