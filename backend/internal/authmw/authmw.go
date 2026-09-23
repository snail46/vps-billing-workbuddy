// Package authmw turns a session cookie into an authenticated request.
//
// It sits between internal/middleware, which knows nothing about identity, and
// internal/httpapi, which knows nothing about cookies. Everything the two web
// applications have to agree on — the cookie names, the CSRF rule, the permission
// rule — is decided here once, so a client and a server cannot disagree about it
// through two implementations drifting apart.
package authmw

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
)

// Cookie names, one per credential space.
//
// Two names rather than one name in two scopes: the name is the only thing a browser
// uses to decide which cookie to attach, so two names is what makes "an admin endpoint
// cannot be authenticated by a user session" true at the transport layer, and not
// merely true of a check inside the handler.
const (
	UserCookieName  = "vps_session"
	AdminCookieName = "vps_admin_session"
)

// CSRFHeaderName carries the synchroniser token for a state-changing request.
//
// A header rather than a form field or a second cookie. A cross-site form post cannot
// set a custom header without provoking a preflight, and a token read from a cookie
// would be accepted from a channel the attacker can also write to.
const CSRFHeaderName = "X-CSRF-Token"

// SessionCookiePath is the path both session cookies are scoped to.
//
// Narrowing the path per credential space was considered and rejected. A cookie's path
// is a browser-side rule rather than a security boundary, and a cookie withheld because
// a path did not match is a quiet, hard-to-diagnose class of bug. The separation that
// matters is server-side: two names, two storage namespaces, two subject types.
const SessionCookiePath = "/"

// Principal is the authenticated subject behind a request.
type Principal struct {
	Session identity.Session
	// Permissions is an administrator's effective set, resolved for this request.
	// Empty for a user session: a customer's rights follow from owning the resource,
	// not from a role.
	Permissions []string
}

// Has reports whether the principal holds a permission.
func (p Principal) Has(permission string) bool {
	return slices.Contains(p.Permissions, permission)
}

type principalKey struct{}

// PrincipalFrom returns the principal a middleware placed on the context.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}

// Middleware builds the identity middleware chain for one server.
type Middleware struct {
	service *identity.Service
	logger  *slog.Logger
	cookie  SessionCookie
}

// SessionCookie is the configured cookie behaviour.
type SessionCookie struct {
	// Secure marks the cookie TLS-only. It is false in development, where the
	// applications are reached over plain HTTP and a Secure cookie would not be stored
	// at all — which would present as "login silently does nothing".
	Secure bool
}

// New builds the middleware.
func New(service *identity.Service, logger *slog.Logger, cookie SessionCookie) *Middleware {
	if logger == nil {
		logger = slog.Default()
	}
	return &Middleware{service: service, logger: logger, cookie: cookie}
}

// RequireUser authenticates the request as a user.
func (m *Middleware) RequireUser(next http.Handler) http.Handler {
	return m.require(next, identity.SubjectUser)
}

// RequireAdmin authenticates the request as an administrator.
func (m *Middleware) RequireAdmin(next http.Handler) http.Handler {
	return m.require(next, identity.SubjectAdmin)
}

func (m *Middleware) require(next http.Handler, subject identity.SubjectType) http.Handler {
	name := cookieName(subject)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only this space's cookie is read. A user session presented at an admin
		// endpoint is not "looked up and rejected" — it is never looked up, because the
		// lookup does not mention the cookie it arrives in.
		cookie, err := r.Cookie(name)
		if err != nil || cookie.Value == "" {
			httpx.WriteError(w, r, m.logger, httpx.ErrUnauthorized())
			return
		}

		var principal Principal
		if subject == identity.SubjectAdmin {
			result, err := m.service.CurrentAdmin(r.Context(), cookie.Value)
			if err != nil {
				m.refuse(w, r, subject, err)
				return
			}
			principal = Principal{Session: result.Session, Permissions: result.Permissions}
		} else {
			result, err := m.service.CurrentUser(r.Context(), cookie.Value)
			if err != nil {
				m.refuse(w, r, subject, err)
				return
			}
			principal = Principal{Session: result.Session}
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
	})
}

// refuse renders the failure for a session that could not be authenticated.
//
// The distinction that matters is between "this credential is not valid" and "I could
// not find out". Answering the second as the first would tell a user their session had
// expired during a dependency outage, and would hide the outage behind a sign-in page.
func (m *Middleware) refuse(w http.ResponseWriter, r *http.Request, subject identity.SubjectType, err error) {
	switch {
	case errors.Is(err, identity.ErrSessionNotFound):
		// The cookie names a session that no longer exists, so there is nothing for the
		// browser to keep. Clearing it makes the next request a plain unauthenticated
		// one instead of repeating the same lookup.
		m.ClearSession(w, subject)
		httpx.WriteError(w, r, m.logger, httpx.ErrUnauthorized())
	case errors.Is(err, identity.ErrAccountSuspended):
		// The password was proved at some point, but the account has been suspended
		// since. Rejecting here rather than only at sign-in is what makes a suspension
		// take effect on the next request rather than whenever the session expires.
		httpx.WriteError(w, r, m.logger, httpx.ErrForbidden())
	default:
		httpx.WriteError(w, r, m.logger, httpx.ErrServiceUnavailable().WithCause(err))
	}
}

// RequireCSRF enforces the synchroniser token from ADR-004.
//
// Safe methods are exempt: they must not change state, and demanding a token for them
// would mean issuing one on every page load. Everything else must present the token
// that was issued with the session.
//
// The token is compared against the session's stored secret, so it is valid only for
// the session it was issued for — one leaked from an account is useless against
// another. The comparison is constant time; the token length is fixed, so no length
// information is disclosed either.
//
// SameSite=Lax on the cookie is defence in depth, not the control: it is a browser
// behaviour, and a client that is not a browser is not bound by it.
func (m *Middleware) RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			// RequireCSRF is only ever mounted behind a session check, so reaching here
			// means the chain was assembled wrongly. Refusing is the safe reading.
			httpx.WriteError(w, r, m.logger, httpx.ErrUnauthorized())
			return
		}

		presented := r.Header.Get(CSRFHeaderName)
		if subtle.ConstantTimeCompare([]byte(presented), []byte(principal.Session.CSRFSecret)) != 1 {
			httpx.WriteError(w, r, m.logger, httpx.ErrForbidden())
			return
		}

		next.ServeHTTP(w, r)
	})
}

// RequirePermission enforces one of the permissions seeded from docs/15.
//
// The permission is compared against the set resolved for this request, so revoking a
// role takes effect on the next request rather than at the next sign-in.
func (m *Middleware) RequirePermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFrom(r.Context())
			if !ok {
				httpx.WriteError(w, r, m.logger, httpx.ErrUnauthorized())
				return
			}
			if !principal.Has(permission) {
				httpx.WriteError(w, r, m.logger, httpx.ErrForbidden())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// IssueSession writes the cookie for a freshly authenticated subject.
//
// Expiry matches the server-side record, so the browser stops presenting a credential
// at the same moment the platform stops accepting it. The two are still independent —
// the store and the expiry check on read are what actually enforce the lifetime, and a
// cookie edited in a browser cannot outlive them.
func (m *Middleware) IssueSession(w http.ResponseWriter, subject identity.SubjectType, session identity.Session) {
	maxAge := int(time.Until(session.ExpiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}

	// HttpOnly and SameSite are set unconditionally below, and the session identifier is
	// random rather than derived from anything. Secure is the one attribute that has to
	// vary: development is served over plain HTTP, where a Secure cookie is not stored at
	// all, and config.Validate refuses to let it be false when APP_ENV is production.
	//
	//nolint:gosec // G124 wants a literal and cannot see through the variable; see above.
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName(subject),
		Value:    session.ID,
		Path:     SessionCookiePath,
		Expires:  session.ExpiresAt,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   m.cookie.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSession removes a session cookie from the browser.
func (m *Middleware) ClearSession(w http.ResponseWriter, subject identity.SubjectType) {
	// A removal has to carry the attributes the cookie was set with, or a browser can
	// decline to replace it.
	//
	//nolint:gosec // G124, for the same reason as IssueSession above.
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName(subject),
		Value:    "",
		Path:     SessionCookiePath,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0).UTC(),
		HttpOnly: true,
		Secure:   m.cookie.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func cookieName(subject identity.SubjectType) string {
	if subject == identity.SubjectAdmin {
		return AdminCookieName
	}
	return UserCookieName
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}
