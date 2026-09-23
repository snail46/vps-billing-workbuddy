// Package httpx implements the HTTP response envelope defined by
// docs/08-API-CONTRACT.md.
//
//	success: {success:true,  data:{}, request_id}
//	failure: {success:false, error:{code,message_key,details}, request_id}
//
// Every response the platform emits is produced here, so the contract has
// exactly one implementation and cannot drift per handler.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/logging"
)

// ContentTypeJSON is the content type of every API response.
const ContentTypeJSON = "application/json; charset=utf-8"

// MaxRequestBodyBytes bounds the request bodies the platform will read.
//
// It is a property of the contract rather than of one handler, because two layers read
// the same body: the authentication throttling middleware reads it to find the account
// being attempted, and the handler reads it to decode the request. Two different bounds
// would mean a request accepted by one and rejected by the other, for a reason the
// caller cannot see. No documented request is anywhere near this size — the largest is
// an order-replacement body of a few hundred bytes.
const MaxRequestBodyBytes = 4 << 10

type successEnvelope struct {
	Success   bool   `json:"success"`
	Data      any    `json:"data"`
	RequestID string `json:"request_id"`
}

type errorEnvelope struct {
	Success   bool      `json:"success"`
	Error     ErrorBody `json:"error"`
	RequestID string    `json:"request_id"`
}

// ErrorBody is the `error` object of the failure envelope.
type ErrorBody struct {
	Code       string `json:"code"`
	MessageKey string `json:"message_key"`
	Details    any    `json:"details,omitempty"`
}

// WriteData renders a success envelope.
func WriteData(w http.ResponseWriter, r *http.Request, status int, data any) {
	if data == nil {
		// The envelope always carries a `data` member so that clients can rely
		// on its presence. An empty object is safer than `null`.
		data = struct{}{}
	}
	writeJSON(w, r, status, successEnvelope{
		Success:   true,
		Data:      data,
		RequestID: logging.RequestIDFrom(r.Context()),
	})
}

// WriteError renders a failure envelope.
//
// Any error that is not an *APIError becomes an opaque 500: internal causes are
// logged with the correlation identifiers and never disclosed to the client.
// The request_id is what links the user-visible failure to the server-side
// detail, which is how docs/19's "critical error with no feedback" is avoided
// without leaking internals.
func WriteError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	apiErr, ok := AsAPIError(err)
	if !ok {
		logServerError(r, logger, "unhandled error", err)
		apiErr = ErrInternal().WithCause(err)
	} else if apiErr.Status >= http.StatusInternalServerError {
		logServerError(r, logger, "request failed", apiErr)
	}

	writeJSON(w, r, apiErr.Status, errorEnvelope{
		Success: false,
		Error: ErrorBody{
			Code:       apiErr.Code,
			MessageKey: apiErr.MessageKey,
			Details:    apiErr.Details,
		},
		RequestID: logging.RequestIDFrom(r.Context()),
	})
}

// WriteNoContent responds with an empty body, used where 204 is the correct
// answer and no envelope is expected.
func WriteNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func logServerError(r *http.Request, logger *slog.Logger, msg string, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	// ErrorContext carries the request context so ContextHandler annotates the
	// record with request_id / trace_id automatically.
	logger.ErrorContext(r.Context(), msg, slog.String("error", err.Error()))
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, body any) {
	payload, err := json.Marshal(body)
	if err != nil {
		// Encoding our own envelope failed, which means a handler supplied
		// something unmarshalable. The status is not yet written, so degrade to
		// a minimal, still-contract-shaped 500.
		if logger := slog.Default(); logger != nil {
			logger.ErrorContext(r.Context(), "failed to encode response envelope",
				slog.String("error", err.Error()))
		}
		fallback, _ := json.Marshal(errorEnvelope{
			Success: false,
			Error: ErrorBody{
				Code:       CodeInternalError,
				MessageKey: MessageKeyFor(CodeInternalError),
			},
			RequestID: logging.RequestIDFrom(r.Context()),
		})
		w.Header().Set("Content-Type", ContentTypeJSON)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(fallback)
		return
	}

	w.Header().Set("Content-Type", ContentTypeJSON)
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}
