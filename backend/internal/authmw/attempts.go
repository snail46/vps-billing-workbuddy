package authmw

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/middleware"
)

// maxAuthBodyBytes bounds the body the throttling middleware will read.
//
// A registration or sign-in body is a couple of hundred bytes; anything larger is not a
// request this middleware has to understand, and reading it unbounded would let an
// unauthenticated caller choose how much memory the server allocates. The bound is the
// contract's rather than this package's, so it cannot disagree with the one the handler
// applies to the same body.
const maxAuthBodyBytes = httpx.MaxRequestBodyBytes

// Counter counts attempts within a window.
type Counter interface {
	// Incr increments key and returns its new value, setting the window.
	Incr(ctx context.Context, key string, window time.Duration) (int64, error)
	// Reset removes keys.
	//
	// A successful attempt clears its own budget: the limit exists to slow guessing,
	// not to punish someone who remembered their password.
	Reset(ctx context.Context, keys ...string) error
}

// Attempts throttles authentication attempts for one scope.
//
// It is deliberately about authentication rather than being a general-purpose rate
// limiter. What has to be counted is not "requests" but "attempts against an address",
// and that value exists only inside the request body — so a limiter that could not read
// the body would be counting the wrong thing.
type Attempts struct {
	Counter Counter
	Logger  *slog.Logger
	// Scope names the budget, so a sign-in attempt and a registration attempt do not
	// share a counter.
	Scope Scope
	// Window is how long a budget lasts.
	Window time.Duration
	// PerIP and PerAccount are the budgets.
	//
	// PerAccount is expected to be zero for registration, where the submitted address
	// usually has no account yet. Counting an unregistered address per account would let
	// anyone lock out the person who actually owns it, while buying nothing: there is no
	// secret at registration to guess.
	PerIP      int
	PerAccount int
}

func (a Attempts) logger() *slog.Logger {
	if a.Logger == nil {
		return slog.Default()
	}
	return a.Logger
}

// Scope names an endpoint group's budget, so a sign-in attempt and a registration
// attempt do not share a counter.
type Scope string

// Scopes.
//
// Administrative sign-in has one of its own. The two credential spaces hold separate
// account tables, so the same address can exist in both, and a shared scope would let an
// attempt against one space spend the other space's budget. Separating them costs nothing
// and removes the question.
const (
	ScopeLogin      Scope = "login"
	ScopeAdminLogin Scope = "admin_login"
	ScopeRegister   Scope = "register"
)

// Middleware throttles the attempts it is mounted on.
func (a Attempts) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys := []string{ipAttemptKey(a.Scope, middleware.ClientIP(r))}
		budgets := []int{a.PerIP}

		if a.PerAccount > 0 {
			if account := normalizeSubmittedEmail(w, r); account != "" {
				keys = append(keys, accountAttemptKey(a.Scope, account))
				budgets = append(budgets, a.PerAccount)
			}
		}

		for i, key := range keys {
			count, err := a.Counter.Incr(r.Context(), key, a.Window)
			if err != nil {
				// The limit is allowed through, and the reason is worth stating: a
				// failed counter means Redis is unreachable, and Redis also holds the
				// sessions. Authentication cannot succeed in that state anyway, so
				// refusing here would answer with a 429 that hides the real fault
				// behind a rate limit. Which counter failed is what the log is for.
				a.logger().WarnContext(r.Context(),
					"authentication rate-limit counter unavailable; the attempt is allowed",
					slog.String("scope", string(a.Scope)),
					slog.String("error", err.Error()),
				)
				continue
			}
			if count > int64(budgets[i]) {
				// The conservative answer: the remaining window is at most this long.
				// Reading the true remainder would cost another round trip to the store
				// that has just been shown to be under load, and telling a client to
				// wait slightly too long is not a failure.
				w.Header().Set("Retry-After", strconv.Itoa(int(a.Window.Seconds())))
				httpx.WriteError(w, r, a.logger(), httpx.ErrRateLimited())
				return
			}
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), attemptKeysKey{}, keys)))
	})
}

// Succeeded clears the counters this request was charged against.
//
// A handler calls it once an attempt has been accepted. Without it the budget would
// limit sign-ins rather than failed sign-ins, and someone signing in twice a day would
// eventually be locked out of their own account.
func (a Attempts) Succeeded(ctx context.Context) {
	keys, ok := ctx.Value(attemptKeysKey{}).([]string)
	if !ok || len(keys) == 0 {
		return
	}
	if err := a.Counter.Reset(ctx, keys...); err != nil {
		a.logger().WarnContext(ctx, "could not clear the attempt counter after a successful attempt",
			slog.String("error", err.Error()))
	}
}

type attemptKeysKey struct{}

// CounterKeyLayout documents the keys the limiter writes.
//
// Exported only so a test can assert the layout, because two properties depend on it:
// the scope is part of the key, so a budget cannot be spent across endpoints, and the
// account key is derived from the normalized address, so "Ada@example.com" and
// "ada@example.com" cannot be charged to two different budgets.
func CounterKeyLayout(scope Scope, kind, value string) string {
	return "auth:attempt:" + string(scope) + ":" + kind + ":" + value
}

func ipAttemptKey(scope Scope, address string) string {
	return CounterKeyLayout(scope, "ip", address)
}

func accountAttemptKey(scope Scope, account string) string {
	return CounterKeyLayout(scope, "account", account)
}

// normalizeSubmittedEmail reads the submitted address out of the request body.
//
// The body is replaced rather than consumed, because the handler still needs it. A body
// that is too large or is not JSON yields no key: the handler will reject the request on
// its own terms, and the address budget is per account rather than per request, so an
// unreadable body is simply not charged to one. The client address budget still applies.
func normalizeSubmittedEmail(w http.ResponseWriter, r *http.Request) string {
	if r.Body == nil || r.ContentLength == 0 {
		return ""
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAuthBodyBytes))
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return ""
	}

	var payload struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return identity.NormalizeEmail(payload.Email)
}
