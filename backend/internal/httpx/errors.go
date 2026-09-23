package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Error codes returned in the `error.code` field of the API envelope defined by
// docs/08-API-CONTRACT.md.
//
// Codes are transport-level. Domain and Provider failures (docs/06) keep their
// own vocabularies and are mapped onto these codes by the layer that owns them,
// so that a Provider's raw error string never reaches a client.
const (
	CodeInternalError      = "INTERNAL_ERROR"
	CodeValidationFailed   = "VALIDATION_FAILED"
	CodeUnauthorized       = "UNAUTHORIZED"
	CodeForbidden          = "FORBIDDEN"
	CodeNotFound           = "NOT_FOUND"
	CodeMethodNotAllowed   = "METHOD_NOT_ALLOWED"
	CodeConflict           = "CONFLICT"
	CodeRateLimited        = "RATE_LIMITED"
	CodeRequestTimeout     = "REQUEST_TIMEOUT"
	CodeServiceUnavailable = "SERVICE_UNAVAILABLE"
)

// APIError is an error that can be rendered directly as the failure envelope.
//
// MessageKey is an i18n key (docs/13), never a human-readable sentence: the
// client localises it, so no user-visible English or Chinese is baked into a
// backend response.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Code is a stable, machine-readable identifier.
	Code string
	// MessageKey is the localisable i18n key for the failure.
	MessageKey string
	// Details carries non-localisable, structured context (e.g. field errors).
	// It must never contain internal diagnostics, credentials or stack traces.
	Details any

	cause error
}

// Error implements the error interface.
func (e *APIError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s (%d): %s", e.Code, e.Status, e.cause.Error())
	}
	return fmt.Sprintf("%s (%d)", e.Code, e.Status)
}

// Unwrap exposes the wrapped cause to errors.Is / errors.As.
func (e *APIError) Unwrap() error { return e.cause }

// WithCause attaches an underlying error. The cause is logged server-side and
// is never sent to the client.
func (e *APIError) WithCause(err error) *APIError {
	clone := *e
	clone.cause = err
	return &clone
}

// WithDetails attaches structured, client-safe detail.
func (e *APIError) WithDetails(details any) *APIError {
	clone := *e
	clone.Details = details
	return &clone
}

func newAPIError(status int, code string) *APIError {
	return &APIError{
		Status:     status,
		Code:       code,
		MessageKey: MessageKeyFor(code),
	}
}

// MessageKeyFor maps an error code onto its i18n key, following the
// `errors.<code>` convention in docs/13-I18N-SPEC.md.
func MessageKeyFor(code string) string {
	return "errors." + strings.ToLower(code)
}

// ErrInternal reports an unexpected server-side failure.
func ErrInternal() *APIError {
	return newAPIError(http.StatusInternalServerError, CodeInternalError)
}

// ErrValidation reports a request that failed validation.
func ErrValidation() *APIError {
	return newAPIError(http.StatusUnprocessableEntity, CodeValidationFailed)
}

// ErrBadRequest reports a malformed request.
func ErrBadRequest() *APIError {
	return newAPIError(http.StatusBadRequest, CodeValidationFailed)
}

// ErrUnauthorized reports a missing or invalid credential.
func ErrUnauthorized() *APIError {
	return newAPIError(http.StatusUnauthorized, CodeUnauthorized)
}

// ErrForbidden reports an authenticated actor lacking permission. Per
// docs/14-SECURITY.md ownership and RBAC are enforced on the backend, so this is
// the result of an authorisation decision, not of hiding a control in the UI.
func ErrForbidden() *APIError {
	return newAPIError(http.StatusForbidden, CodeForbidden)
}

// ErrNotFound reports a missing resource.
func ErrNotFound() *APIError {
	return newAPIError(http.StatusNotFound, CodeNotFound)
}

// ErrMethodNotAllowed reports an unsupported method on an existing route.
func ErrMethodNotAllowed() *APIError {
	return newAPIError(http.StatusMethodNotAllowed, CodeMethodNotAllowed)
}

// ErrConflict reports a state conflict, such as an idempotency clash.
func ErrConflict() *APIError {
	return newAPIError(http.StatusConflict, CodeConflict)
}

// ErrRateLimited reports rate-limit exhaustion.
func ErrRateLimited() *APIError {
	return newAPIError(http.StatusTooManyRequests, CodeRateLimited)
}

// ErrRequestTimeout reports that a request exceeded its processing deadline.
//
// The status is 503 rather than the semantically obvious 504 because
// docs/08-API-CONTRACT.md enumerates the permitted statuses and does not include
// 504. Staying inside the documented set keeps clients from having to special-
// case a status the contract never promised.
func ErrRequestTimeout() *APIError {
	return newAPIError(http.StatusServiceUnavailable, CodeRequestTimeout)
}

// ErrServiceUnavailable reports that a required dependency is unavailable.
func ErrServiceUnavailable() *APIError {
	return newAPIError(http.StatusServiceUnavailable, CodeServiceUnavailable)
}

// AsAPIError extracts an *APIError from an error chain.
func AsAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}
