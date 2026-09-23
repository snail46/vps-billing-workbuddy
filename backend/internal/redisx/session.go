package redisx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
)

// sessionKV is the Redis surface the session store needs.
//
// It is an interface rather than *redis.Client so that the parts which can be wrong
// — the key layout, the per-subject index and the revocation logic — are verified
// against an in-memory fake. Whether SET and DEL themselves behave is Redis's
// concern, and this machine has no Redis to ask (ADR-003).
type sessionKV interface {
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Get(ctx context.Context, key string) (string, error)
	Delete(ctx context.Context, keys ...string) error
	Expire(ctx context.Context, key string, ttl time.Duration) error
	SetAdd(ctx context.Context, key string, members ...string) error
	SetRemove(ctx context.Context, key string, members ...string) error
	SetMembers(ctx context.Context, key string) ([]string, error)
}

// SessionStore is the Redis-backed identity.SessionStore.
type SessionStore struct {
	kv sessionKV
	// now is a field so that expiry can be tested without sleeping. Expiry is
	// enforced on read as well as by the key's TTL, because a store that fails to
	// evict must not leave a valid credential behind.
	now func() time.Time
}

// NewSessionStore returns a store backed by a Redis client.
func NewSessionStore(client *redis.Client) *SessionStore {
	return newSessionStoreWithKV(&redisKV{client: client})
}

func newSessionStoreWithKV(kv sessionKV) *SessionStore {
	return &SessionStore{kv: kv, now: time.Now}
}

// Compile-time proof that the store satisfies the interface the identity package
// expects, so a signature change is caught here rather than at a call site.
var _ identity.SessionStore = (*SessionStore)(nil)

// Key layout.
//
// The subject type is part of every key, which is what makes the isolation between
// credential spaces structural rather than a check that could be forgotten: an
// admin lookup is not "a user lookup that then verifies the subject", it is a
// lookup in a keyspace a user session never writes to.
//
// `session:` holds one record; `sessions:` holds the set of ids belonging to one
// subject, so that revoking every session of a subject does not require scanning
// the keyspace.
func sessionKey(subject identity.SubjectType, id string) string {
	return "session:" + string(subject) + ":" + id
}

func subjectSessionsKey(subject identity.SubjectType, subjectID uuid.UUID) string {
	return "sessions:" + string(subject) + ":" + subjectID.String()
}

// Create stores a session until its expiry.
func (s *SessionStore) Create(ctx context.Context, session identity.Session) error {
	if !session.Subject.Valid() {
		return fmt.Errorf("redisx: cannot store a session for subject type %q", session.Subject)
	}

	ttl := session.ExpiresAt.Sub(s.now())
	if ttl <= 0 {
		// Storing it would create a record that is already dead, which is a caller
		// error rather than something to paper over.
		return fmt.Errorf("redisx: refusing to store an expired session for %s %s",
			session.Subject, session.SubjectID)
	}

	encoded, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("redisx: encode session: %w", err)
	}

	if err := s.kv.Set(ctx, sessionKey(session.Subject, session.ID), string(encoded), ttl); err != nil {
		return fmt.Errorf("redisx: store session: %w", err)
	}

	indexKey := subjectSessionsKey(session.Subject, session.SubjectID)
	if err := s.kv.SetAdd(ctx, indexKey, session.ID); err != nil {
		return fmt.Errorf("redisx: index session: %w", err)
	}

	// The index is refreshed to the full lifetime of the session just added. Every
	// session of one subject type shares that type's lifetime, so this only ever
	// extends the index — it can never expire while it still has a live member.
	if err := s.kv.Expire(ctx, indexKey, ttl); err != nil {
		return fmt.Errorf("redisx: expire session index: %w", err)
	}

	return nil
}

// Get returns a live session.
func (s *SessionStore) Get(ctx context.Context, subject identity.SubjectType, id string) (identity.Session, error) {
	if id == "" || !subject.Valid() {
		return identity.Session{}, identity.ErrSessionNotFound
	}

	raw, err := s.kv.Get(ctx, sessionKey(subject, id))
	if err != nil {
		if errors.Is(err, identity.ErrSessionNotFound) {
			return identity.Session{}, identity.ErrSessionNotFound
		}
		return identity.Session{}, fmt.Errorf("redisx: read session: %w", err)
	}

	var session identity.Session
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		// A record that cannot be decoded is treated as absent rather than as an
		// error: the caller's only sensible response is to authenticate again, and
		// returning an error would turn a corrupt row into a failed request.
		return identity.Session{}, identity.ErrSessionNotFound
	}

	// Defence in depth. The key layout already makes a cross-space read impossible,
	// and this checks the record as well: if the two ever disagreed, the safe
	// outcome is to deny.
	if session.Subject != subject {
		return identity.Session{}, identity.ErrSessionNotFound
	}
	if session.IsExpired(s.now()) {
		return identity.Session{}, identity.ErrSessionNotFound
	}

	return session, nil
}

// Delete ends one session.
//
// The session is read first so its index entry can be removed, which costs a second
// round trip. The alternative — leaving the id in the index — would make every
// revocation pass over ids that no longer resolve, and the index is the structure a
// suspension relies on being accurate.
func (s *SessionStore) Delete(ctx context.Context, subject identity.SubjectType, id string) error {
	session, err := s.Get(ctx, subject, id)
	if err != nil {
		if errors.Is(err, identity.ErrSessionNotFound) {
			// Already gone. Deleting twice is not an error: a logout that races the
			// expiry, or a retried request, must still succeed.
			return nil
		}
		return err
	}

	if err := s.kv.Delete(ctx, sessionKey(subject, id)); err != nil {
		return fmt.Errorf("redisx: delete session: %w", err)
	}
	if err := s.kv.SetRemove(ctx, subjectSessionsKey(subject, session.SubjectID), id); err != nil {
		return fmt.Errorf("redisx: deindex session: %w", err)
	}
	return nil
}

// DeleteForSubject ends every session belonging to a subject.
func (s *SessionStore) DeleteForSubject(ctx context.Context, subject identity.SubjectType, subjectID uuid.UUID) error {
	if !subject.Valid() {
		return fmt.Errorf("redisx: cannot revoke sessions for subject type %q", subject)
	}

	indexKey := subjectSessionsKey(subject, subjectID)
	ids, err := s.kv.SetMembers(ctx, indexKey)
	if err != nil {
		return fmt.Errorf("redisx: read session index: %w", err)
	}

	// The index key is deleted with its members, so a subject who is later
	// reinstated starts from an empty index rather than a list of stale ids.
	keys := make([]string, 0, len(ids)+1)
	for _, id := range ids {
		keys = append(keys, sessionKey(subject, id))
	}
	keys = append(keys, indexKey)

	if err := s.kv.Delete(ctx, keys...); err != nil {
		return fmt.Errorf("redisx: delete sessions for subject: %w", err)
	}
	return nil
}

// redisKV is the adapter from go-redis to sessionKV.
type redisKV struct{ client *redis.Client }

var _ sessionKV = (*redisKV)(nil)

func (r *redisKV) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}

func (r *redisKV) Get(ctx context.Context, key string) (string, error) {
	value, err := r.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		// Mapped here so the store deals in one not-found error rather than two.
		return "", identity.ErrSessionNotFound
	}
	return value, err
}

func (r *redisKV) Delete(ctx context.Context, keys ...string) error {
	return r.client.Del(ctx, keys...).Err()
}

func (r *redisKV) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return r.client.Expire(ctx, key, ttl).Err()
}

func (r *redisKV) SetAdd(ctx context.Context, key string, members ...string) error {
	// go-redis takes variadic interface{} rather than strings, so the values are
	// widened here. The conversion is confined to the adapter, which keeps the
	// interface the store is written against free of it.
	return r.client.SAdd(ctx, key, toArguments(members)...).Err()
}

func (r *redisKV) SetRemove(ctx context.Context, key string, members ...string) error {
	return r.client.SRem(ctx, key, toArguments(members)...).Err()
}

func toArguments(members []string) []interface{} {
	arguments := make([]interface{}, len(members))
	for i, member := range members {
		arguments[i] = member
	}
	return arguments
}

func (r *redisKV) SetMembers(ctx context.Context, key string) ([]string, error) {
	return r.client.SMembers(ctx, key).Result()
}
