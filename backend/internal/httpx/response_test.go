package httpx_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/logging"
)

// discardLogger keeps expected error paths from printing during tests while
// still exercising the real logging code path.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func requestWithIDs(ctx context.Context) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/example", nil)
	return r.WithContext(ctx)
}

func TestWriteDataProducesSuccessEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := logging.WithRequestID(context.Background(), "req-123")

	httpx.WriteData(rec, requestWithIDs(ctx), http.StatusOK, map[string]string{"status": "up"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != httpx.ContentTypeJSON {
		t.Fatalf("unexpected content type %q", ct)
	}

	var body struct {
		Success   bool              `json:"success"`
		Data      map[string]string `json:"data"`
		RequestID string            `json:"request_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	if !body.Success {
		t.Error("expected success to be true")
	}
	if body.Data["status"] != "up" {
		t.Errorf("unexpected data payload: %#v", body.Data)
	}
	if body.RequestID != "req-123" {
		t.Errorf("expected request_id to be echoed, got %q", body.RequestID)
	}
}

func TestWriteDataRendersEmptyObjectWhenDataIsNil(t *testing.T) {
	rec := httptest.NewRecorder()

	httpx.WriteData(rec, requestWithIDs(context.Background()), http.StatusOK, nil)

	// The contract always carries a `data` member; null would force every client
	// to special-case it.
	if !strings.Contains(rec.Body.String(), `"data":{}`) {
		t.Fatalf("expected an empty data object, got %s", rec.Body.String())
	}
}

func TestWriteErrorRendersAPIError(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := logging.WithRequestID(context.Background(), "req-404")

	httpx.WriteError(rec, requestWithIDs(ctx), discardLogger(), httpx.ErrNotFound())

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var body struct {
		Success   bool `json:"success"`
		Error     httpx.ErrorBody
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	if body.Success {
		t.Error("expected success to be false")
	}
	if body.Error.Code != httpx.CodeNotFound {
		t.Errorf("unexpected code %q", body.Error.Code)
	}
	if body.Error.MessageKey != "errors.not_found" {
		t.Errorf("expected an i18n message key, got %q", body.Error.MessageKey)
	}
	if body.RequestID != "req-404" {
		t.Errorf("expected request_id to be echoed, got %q", body.RequestID)
	}
}

func TestWriteErrorHidesInternalCause(t *testing.T) {
	rec := httptest.NewRecorder()
	secret := "pq: relation \"orders\" does not exist"

	httpx.WriteError(rec, requestWithIDs(context.Background()), discardLogger(), errors.New(secret))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	// An unexpected error must not leak diagnostics to the client; the
	// request_id is the link to the logged detail.
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("internal error text leaked into the response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), httpx.CodeInternalError) {
		t.Fatalf("expected %s in the response, got %s", httpx.CodeInternalError, rec.Body.String())
	}
}

func TestWriteErrorKeepsWrappedAPIError(t *testing.T) {
	rec := httptest.NewRecorder()
	// A cause attached server-side must still render as the API error, because
	// the cause is context for the logs, not part of the classification.
	err := httpx.ErrConflict().WithCause(errors.New("duplicate idempotency key"))

	httpx.WriteError(rec, requestWithIDs(context.Background()), discardLogger(), err)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "duplicate idempotency key") {
		t.Fatalf("cause leaked into the response: %s", rec.Body.String())
	}
}

func TestWriteErrorIncludesDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	err := httpx.ErrValidation().WithDetails(map[string]any{"field": "quantity"})

	httpx.WriteError(rec, requestWithIDs(context.Background()), discardLogger(), err)

	if !strings.Contains(rec.Body.String(), `"field":"quantity"`) {
		t.Fatalf("expected details in the response, got %s", rec.Body.String())
	}
}

func TestMessageKeyConvention(t *testing.T) {
	// docs/13 defines `errors.<code lowercased>`; keeping the mapping in one
	// place is what stops handlers inventing their own keys.
	tests := map[string]string{
		httpx.CodeNotFound:           "errors.not_found",
		httpx.CodeValidationFailed:   "errors.validation_failed",
		httpx.CodeMethodNotAllowed:   "errors.method_not_allowed",
		httpx.CodeRequestTimeout:     "errors.request_timeout",
		httpx.CodeServiceUnavailable: "errors.service_unavailable",
	}

	for code, want := range tests {
		if got := httpx.MessageKeyFor(code); got != want {
			t.Errorf("MessageKeyFor(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestAPIErrorStatusCodes(t *testing.T) {
	tests := []struct {
		name string
		err  *httpx.APIError
		want int
	}{
		{"internal", httpx.ErrInternal(), http.StatusInternalServerError},
		{"validation", httpx.ErrValidation(), http.StatusUnprocessableEntity},
		{"bad request", httpx.ErrBadRequest(), http.StatusBadRequest},
		{"unauthorized", httpx.ErrUnauthorized(), http.StatusUnauthorized},
		{"forbidden", httpx.ErrForbidden(), http.StatusForbidden},
		{"not found", httpx.ErrNotFound(), http.StatusNotFound},
		{"method not allowed", httpx.ErrMethodNotAllowed(), http.StatusMethodNotAllowed},
		{"conflict", httpx.ErrConflict(), http.StatusConflict},
		{"rate limited", httpx.ErrRateLimited(), http.StatusTooManyRequests},
		// 504 is deliberately avoided: docs/08 does not list it.
		{"request timeout maps to 503", httpx.ErrRequestTimeout(), http.StatusServiceUnavailable},
		{"service unavailable", httpx.ErrServiceUnavailable(), http.StatusServiceUnavailable},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Status != tc.want {
				t.Fatalf("expected status %d, got %d", tc.want, tc.err.Status)
			}
		})
	}
}
