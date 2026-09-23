package authmw

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// counterFake counts in memory.
type counterFake struct {
	counts map[string]int
	// failWith makes every increment fail, which is how a Redis outage is reproduced.
	failWith error
	// resets records what a successful attempt cleared.
	resets []string
}

func newCounterFake() *counterFake {
	return &counterFake{counts: map[string]int{}}
}

func (c *counterFake) Incr(_ context.Context, key string, _ time.Duration) (int64, error) {
	if c.failWith != nil {
		return 0, c.failWith
	}
	c.counts[key]++
	return int64(c.counts[key]), nil
}

func (c *counterFake) Reset(_ context.Context, keys ...string) error {
	c.resets = append(c.resets, keys...)
	for _, key := range keys {
		delete(c.counts, key)
	}
	return nil
}

// attempt sends a sign-in request through the limiter and reports the status.
func attempt(t *testing.T, middleware func(http.Handler) http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The body must still be readable by the handler: the middleware reads it to find
		// the account, and a middleware that consumed it would break every endpoint behind
		// it in a way no status code would reveal.
		var payload struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("the handler could not read the body the middleware had already read: %v", err)
		}
		if payload.Email == "" {
			t.Errorf("the handler saw an empty address for %q", body)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAttemptsStopsRepeatedAttemptsAgainstOneAccount(t *testing.T) {
	counter := newCounterFake()
	limiter := Attempts{
		Counter: counter, Scope: ScopeLogin, Window: 15 * time.Minute,
		PerIP: 100, PerAccount: 3,
	}.Middleware

	for i := 1; i <= 3; i++ {
		if rec := attempt(t, limiter, `{"email":"ada@example.com","password":"x"}`); rec.Code != http.StatusOK {
			t.Fatalf("attempt %d was refused: %d", i, rec.Code)
		}
	}

	rec := attempt(t, limiter, `{"email":"ada@example.com","password":"x"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the budget was not enforced: %d", rec.Code)
	}

	var body struct {
		Success bool `json:"success"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the refusal is not the envelope: %v", err)
	}
	if body.Error.Code != "RATE_LIMITED" {
		t.Errorf("error code = %q", body.Error.Code)
	}
	// A client needs to know how long to wait, and the value is the conservative one: the
	// exact remainder would cost another round trip to the store.
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After header")
	}
}

func TestAttemptsChargesTheAddressIndependentlyOfTheClient(t *testing.T) {
	counter := newCounterFake()
	limiter := Attempts{
		Counter: counter, Scope: ScopeLogin, Window: 15 * time.Minute,
		PerIP: 100, PerAccount: 1,
	}.Middleware

	if rec := attempt(t, limiter, `{"email":"ada@example.com","password":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("the first attempt was refused: %d", rec.Code)
	}
	// A different address is a different budget, so one account being attacked does not
	// block another account. That separation is the reason the account key exists at all.
	if rec := attempt(t, limiter, `{"email":"grace@example.com","password":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("an unrelated account was blocked: %d", rec.Code)
	}
}

func TestAttemptsNormalizesTheAddressBeforeCounting(t *testing.T) {
	counter := newCounterFake()
	limiter := Attempts{
		Counter: counter, Scope: ScopeLogin, Window: 15 * time.Minute,
		PerIP: 100, PerAccount: 1,
	}.Middleware

	if rec := attempt(t, limiter, `{"email":"Ada@Example.com","password":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("the first attempt was refused: %d", rec.Code)
	}
	// Without normalization the same account would be reachable under as many keys as the
	// case and spacing variations an attacker cares to try, and the budget would be
	// meaningless.
	if rec := attempt(t, limiter, `{"email":"  ada@example.com  ","password":"x"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("case and spacing produced a fresh budget: %d", rec.Code)
	}
}

func TestAttemptsScopesDoNotShareABudget(t *testing.T) {
	counter := newCounterFake()
	login := Attempts{Counter: counter, Scope: ScopeLogin, Window: time.Minute, PerIP: 1}.Middleware
	register := Attempts{Counter: counter, Scope: ScopeRegister, Window: time.Minute, PerIP: 1}.Middleware

	if rec := attempt(t, login, `{"email":"ada@example.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("the sign-in was refused: %d", rec.Code)
	}
	// A separate scope means exhausting one endpoint's budget does not close another. The
	// alternative would let an attacker deny registration to everyone behind one address.
	if rec := attempt(t, register, `{"email":"ada@example.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("the registration budget was spent by a sign-in: %d", rec.Code)
	}

	// The two spaces are separate scopes, and each credential space has its own accounts.
	// A shared scope would let an attempt against one spend the other's budget.
	admin := Attempts{Counter: counter, Scope: ScopeAdminLogin, Window: time.Minute, PerIP: 1}.Middleware
	if rec := attempt(t, admin, `{"email":"ada@example.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("the admin sign-in budget was spent by a user sign-in: %d", rec.Code)
	}
}

func TestAttemptsClearsTheBudgetAfterASuccessfulAttempt(t *testing.T) {
	counter := newCounterFake()
	attempts := Attempts{
		Counter: counter, Scope: ScopeLogin, Window: 15 * time.Minute,
		PerIP: 100, PerAccount: 2,
	}

	var succeeded bool
	handler := attempts.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Succeeded(r.Context())
		succeeded = true
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"email":"ada@example.com"}`))
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d was refused even though each one succeeded: %d", i+1, rec.Code)
		}
	}

	if !succeeded {
		t.Fatal("the handler never ran")
	}
	// Without this the budget would limit sign-ins rather than failed sign-ins, and someone
	// signing in repeatedly would eventually be locked out of their own account.
	if len(counter.resets) == 0 {
		t.Fatal("a successful attempt did not clear its budget")
	}
}

func TestAttemptsAllowsTheAttemptWhenTheCounterIsUnavailable(t *testing.T) {
	counter := newCounterFake()
	counter.failWith = errors.New("dial tcp: connection refused")

	limiter := Attempts{
		Counter: counter, Scope: ScopeLogin, Window: time.Minute, PerIP: 1, PerAccount: 1,
	}.Middleware

	// Redis also holds the sessions, so authentication cannot succeed while it is down. A
	// refusal here would answer with a 429 and hide the real fault behind a rate limit.
	for i := 0; i < 3; i++ {
		if rec := attempt(t, limiter, `{"email":"ada@example.com"}`); rec.Code != http.StatusOK {
			t.Fatalf("a counter failure was reported as %d, expected the attempt to be allowed", rec.Code)
		}
	}
}

func TestAttemptsDoesNotChargeAnUnreadableBodyToAnAccount(t *testing.T) {
	counter := newCounterFake()
	limiter := Attempts{
		Counter: counter, Scope: ScopeLogin, Window: time.Minute, PerIP: 100, PerAccount: 1,
	}.Middleware

	// A body that cannot be parsed yields no account key, so the request is charged to the
	// client address only and the handler rejects it on its own terms.
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader("not json"))
		limiter(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
		})).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("a malformed body was answered with %d", rec.Code)
		}
	}

	for key := range counter.counts {
		if strings.Contains(key, ":account:") {
			t.Errorf("a malformed body was charged to an account: %s", key)
		}
	}
}

func TestCounterKeyLayoutSeparatesScopeKindAndValue(t *testing.T) {
	// The layout is asserted because two properties rest on it: the scope is part of the key,
	// so a budget cannot be spent across endpoints, and the account key is derived from the
	// normalized address, so one account cannot hold two budgets.
	if got := ipAttemptKey(ScopeLogin, "203.0.113.7"); got != "auth:attempt:login:ip:203.0.113.7" {
		t.Errorf("ip key = %q", got)
	}
	if got := accountAttemptKey(ScopeLogin, "ada@example.com"); got != "auth:attempt:login:account:ada@example.com" {
		t.Errorf("account key = %q", got)
	}
	if ipAttemptKey(ScopeLogin, "x") == ipAttemptKey(ScopeRegister, "x") {
		t.Error("two scopes produced the same key")
	}
	if ipAttemptKey(ScopeLogin, "x") == accountAttemptKey(ScopeLogin, "x") {
		t.Error("an address and an account produced the same key")
	}
}
