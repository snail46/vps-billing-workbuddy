package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/audit"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/logging"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/middleware"
)

// The endpoints here implement the /auth and /admin/auth paths of
// docs/openapi/openapi.yaml. They are transport only: every rule they appear to apply
// — what a refusal reveals, when a password is re-hashed, which attempts are audited —
// belongs to internal/identity, and a handler that re-decided one of them would be a
// second place for it to be wrong.

// registerRequest is the body of POST /auth/register.
type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	// Locale and Timezone are optional. The service defaults the locale rather than
	// this layer, so a client that omits it and a client that sends an empty string are
	// treated the same way.
	Locale   string `json:"locale"`
	Timezone string `json:"timezone"`
}

// loginRequest is the body of a sign-in. totp_code is what an administrator
// whose account enabled the second factor must present (ADR-015 §1).
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TOTPCode string `json:"totp_code"`
}

// userPayload is a user as the API exposes it.
//
// It is a view of its own rather than the domain record with JSON tags, and the reason is
// the password hash: a struct tag is not what keeps a credential out of a response, and
// building the view explicitly means a field added to the domain record cannot be
// published by accident.
type userPayload struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Status   string `json:"status"`
	Locale   string `json:"locale"`
	Timezone string `json:"timezone"`
}

// adminPayload is an administrator as the API exposes it.
type adminPayload struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	// Permissions is the effective set, resolved for this request. The client uses it to
	// decide what to offer; it is not the enforcement, which stays on the backend
	// (docs/14).
	Permissions []string `json:"permissions"`
}

type userSessionPayload struct {
	User userPayload `json:"user"`
	// CSRFToken is issued with the session. A client cannot read it from the cookie —
	// that cookie is HttpOnly — so it is returned in the body, which is also what makes
	// the response to GET /auth/me the way a reloaded page recovers the token.
	CSRFToken string    `json:"csrf_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type adminSessionPayload struct {
	Admin     adminPayload `json:"admin"`
	CSRFToken string       `json:"csrf_token"`
	ExpiresAt time.Time    `json:"expires_at"`
}

// registerUser creates a user account.
func (a *api) registerUser(w http.ResponseWriter, r *http.Request) {
	var body registerRequest
	if !a.decodeBody(w, r, &body) {
		return
	}

	user, err := a.auth.Service.Register(r.Context(), identity.RegisterInput{
		Email:    body.Email,
		Password: body.Password,
		Locale:   body.Locale,
		Timezone: body.Timezone,
	})
	if err != nil {
		httpx.WriteError(w, r, a.logger, authFailure(err))
		return
	}

	// Registration deliberately does not sign the account in. The session is created by
	// the sign-in endpoint, so there is one place that issues credentials and one place
	// that decides whether an address may have them.
	httpx.WriteData(w, r, http.StatusCreated, map[string]any{"user": newUserPayload(user)})
}

// loginUser authenticates a user and issues a session cookie.
func (a *api) loginUser(w http.ResponseWriter, r *http.Request) {
	var body loginRequest
	if !a.decodeBody(w, r, &body) {
		return
	}

	result, err := a.auth.Service.LoginUser(r.Context(), identity.LoginInput{
		Email:         body.Email,
		Password:      body.Password,
		ClientContext: auditContext(r),
	})
	if err != nil {
		httpx.WriteError(w, r, a.logger, authFailure(err))
		return
	}

	// The counters are cleared only after the credential has been accepted, which is what
	// makes the budget a limit on guessing rather than on signing in.
	a.auth.Login.Succeeded(r.Context())
	a.auth.Sessions.IssueSession(w, identity.SubjectUser, result.Session)

	httpx.WriteData(w, r, http.StatusOK, userSessionPayload{
		User:      newUserPayload(result.User),
		CSRFToken: result.Session.CSRFSecret,
		ExpiresAt: result.Session.ExpiresAt,
	})
}

// logoutUser ends the session the request carries.
func (a *api) logoutUser(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return
	}

	if err := a.auth.Service.Logout(r.Context(), identity.SubjectUser, principal.Session.ID); err != nil {
		httpx.WriteError(w, r, a.logger, authFailure(err))
		return
	}

	// The cookie is cleared even though the store has already dropped the record: a
	// browser left holding an identifier for a session that no longer exists would repeat
	// a failing lookup on every subsequent request.
	a.auth.Sessions.ClearSession(w, identity.SubjectUser)
	httpx.WriteNoContent(w)
}

// meUser returns the authenticated user.
func (a *api) meUser(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return
	}

	current, err := a.auth.Service.CurrentUser(r.Context(), principal.Session.ID)
	if err != nil {
		// The middleware resolved the same session moments ago, so a failure here means
		// the account changed underneath the request or a dependency failed. Both are
		// answered by the middleware's own mapping.
		httpx.WriteError(w, r, a.logger, authFailure(err))
		return
	}

	httpx.WriteData(w, r, http.StatusOK, userSessionPayload{
		User:      newUserPayload(current.User),
		CSRFToken: current.Session.CSRFSecret,
		ExpiresAt: current.Session.ExpiresAt,
	})
}

// loginAdmin authenticates an administrator and issues an admin session cookie.
func (a *api) loginAdmin(w http.ResponseWriter, r *http.Request) {
	var body loginRequest
	if !a.decodeBody(w, r, &body) {
		return
	}

	result, err := a.auth.Service.LoginAdmin(r.Context(), identity.LoginInput{
		Email:         body.Email,
		Password:      body.Password,
		TOTPCode:      body.TOTPCode,
		ClientContext: auditContext(r),
	})
	if err != nil {
		httpx.WriteError(w, r, a.logger, authFailure(err))
		return
	}

	a.auth.AdminLogin.Succeeded(r.Context())
	a.auth.Sessions.IssueSession(w, identity.SubjectAdmin, result.Session)

	httpx.WriteData(w, r, http.StatusOK, adminSessionPayload{
		Admin:     newAdminPayload(result.Admin, result.Permissions),
		CSRFToken: result.Session.CSRFSecret,
		ExpiresAt: result.Session.ExpiresAt,
	})
}

// logoutAdmin ends the administrator session the request carries.
func (a *api) logoutAdmin(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return
	}

	if err := a.auth.Service.LogoutAdmin(r.Context(), principal.Session.ID, auditContext(r)); err != nil {
		httpx.WriteError(w, r, a.logger, authFailure(err))
		return
	}

	a.auth.Sessions.ClearSession(w, identity.SubjectAdmin)
	httpx.WriteNoContent(w)
}

// meAdmin returns the authenticated administrator.
func (a *api) meAdmin(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, a.logger, httpx.ErrUnauthorized())
		return
	}

	current, err := a.auth.Service.CurrentAdmin(r.Context(), principal.Session.ID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, authFailure(err))
		return
	}

	httpx.WriteData(w, r, http.StatusOK, adminSessionPayload{
		Admin:     newAdminPayload(current.Admin, current.Permissions),
		CSRFToken: current.Session.CSRFSecret,
		ExpiresAt: current.Session.ExpiresAt,
	})
}

// decodeBody reads a JSON request body into target.
//
// A malformed or oversized body is reported as a validation failure rather than as a
// payload-too-large status: docs/08-API-CONTRACT.md enumerates the statuses the platform
// emits and does not include 413, and a caller that sends something we cannot read has
// made a mistake in the request either way.
func (a *api) decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.MaxRequestBodyBytes))
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrBadRequest())
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 || !json.Valid(body) {
		httpx.WriteError(w, r, a.logger, httpx.ErrBadRequest())
		return false
	}
	if err := json.Unmarshal(body, target); err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrBadRequest())
		return false
	}
	return true
}

// authFailure maps a domain failure onto the transport contract.
//
// The mapping is the whole of what a caller learns, so it is written once. Two
// distinctions are load-bearing and easy to lose: a failed sign-in is not the same
// failure as an expired session — the first means "check your password", the second means
// "sign in again" — and a validation failure names the field, so the client can point at
// the input rather than at the form.
func authFailure(err error) *httpx.APIError {
	switch {
	case errors.Is(err, identity.ErrInvalidCredentials):
		return withMessageKey(httpx.ErrUnauthorized(), "errors.invalid_credentials")
	case errors.Is(err, identity.ErrAccountSuspended):
		return withMessageKey(httpx.ErrForbidden(), "errors.account_suspended")
	case errors.Is(err, identity.ErrEmailTaken):
		return withMessageKey(httpx.ErrConflict(), "errors.email_taken")
	case errors.Is(err, identity.ErrInvalidEmail):
		return invalidField("email")
	case errors.Is(err, identity.ErrWeakPassword):
		return invalidField("password")
	case errors.Is(err, identity.ErrUnsupportedLocale):
		return invalidField("locale")
	case errors.Is(err, identity.ErrSessionNotFound):
		return httpx.ErrUnauthorized()
	default:
		return httpx.ErrInternal().WithCause(err)
	}
}

// invalidField reports a validation failure against one field.
//
// The generic validation message would tell the client the request was rejected but not
// where, and the field name is a stable identifier rather than a sentence, so the client
// localises it like every other message.
func invalidField(field string) *httpx.APIError {
	err := httpx.ErrValidation().WithDetails(map[string]string{"field": field})
	return withMessageKey(err, "errors.invalid_"+field)
}

func withMessageKey(err *httpx.APIError, key string) *httpx.APIError {
	err.MessageKey = key
	return err
}

// auditContext records where a request came from.
//
// The source address is the one the access log and the rate limiter use, so an operator
// correlating the three is comparing the same value rather than three derivations of it.
func auditContext(r *http.Request) audit.Context {
	return audit.Context{
		IP:        middleware.ClientIP(r),
		UserAgent: r.UserAgent(),
		RequestID: logging.RequestIDFrom(r.Context()),
		TraceID:   logging.TraceIDFrom(r.Context()),
	}
}

func newUserPayload(user identity.User) userPayload {
	return userPayload{
		ID:       user.ID.String(),
		Email:    user.Email,
		Status:   user.Status,
		Locale:   user.Locale,
		Timezone: user.Timezone,
	}
}

func newAdminPayload(admin identity.Admin, permissions []string) adminPayload {
	if permissions == nil {
		// Encoded as [] rather than null: a client iterating the set should not have to
		// treat "no permissions" and "not reported" differently.
		permissions = []string{}
	}
	return adminPayload{
		ID:          admin.ID.String(),
		Email:       admin.Email,
		DisplayName: admin.DisplayName,
		Status:      admin.Status,
		Permissions: permissions,
	}
}
