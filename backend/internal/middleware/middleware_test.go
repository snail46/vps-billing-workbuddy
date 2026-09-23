package middleware_test

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
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/logging"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/middleware"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// contextCaptor records the correlation identifiers visible to a handler.
type contextCaptor struct {
	requestID string
	traceID   string
}

func captorHandler(c *contextCaptor) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.requestID = logging.RequestIDFrom(r.Context())
		c.traceID = logging.TraceIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequestIDGeneratesWhenAbsent(t *testing.T) {
	captor := &contextCaptor{}
	rec := httptest.NewRecorder()

	middleware.RequestID(captorHandler(captor)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if captor.requestID == "" {
		t.Fatal("expected a generated request id on the context")
	}
	if got := rec.Header().Get(middleware.HeaderRequestID); got != captor.requestID {
		t.Fatalf("response header %q does not match the context value %q", got, captor.requestID)
	}
}

func TestRequestIDAcceptsSafeClientValue(t *testing.T) {
	const supplied = "01J8Z6Q0M9K2V4N7P3R5T8W1XY"
	captor := &contextCaptor{}
	rec := httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(middleware.HeaderRequestID, supplied)

	middleware.RequestID(captorHandler(captor)).ServeHTTP(rec, req)

	if captor.requestID != supplied {
		t.Fatalf("expected the supplied id to be honoured, got %q", captor.requestID)
	}
}

func TestRequestIDRejectsUnsafeClientValues(t *testing.T) {
	// These values would be echoed into a header and into structured logs, so a
	// forged value could corrupt log records or inject header content.
	unsafe := map[string]string{
		"newline injection": "abc\ndef",
		"carriage return":   "abc\rdef",
		"space":             "abc def",
		"non ascii":         "abc-\u4e2d\u6587",
		"too long":          strings.Repeat("a", 129),
	}

	for name, value := range unsafe {
		t.Run(name, func(t *testing.T) {
			captor := &contextCaptor{}
			rec := httptest.NewRecorder()

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(middleware.HeaderRequestID, value)

			middleware.RequestID(captorHandler(captor)).ServeHTTP(rec, req)

			if captor.requestID == value {
				t.Fatalf("unsafe request id %q was accepted", value)
			}
			if captor.requestID == "" {
				t.Fatal("expected a replacement identifier")
			}
		})
	}
}

func TestTraceIDAdoptsValidTraceparent(t *testing.T) {
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	captor := &contextCaptor{}
	rec := httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")

	middleware.TraceID(captorHandler(captor)).ServeHTTP(rec, req)

	if captor.traceID != traceID {
		t.Fatalf("expected the upstream trace id %q to be adopted, got %q", traceID, captor.traceID)
	}
}

func TestTraceIDIgnoresMalformedTraceparent(t *testing.T) {
	malformed := map[string]string{
		"wrong field count":  "00-4bf92f3577b34da6a3ce929d0e0e4736-01",
		"trace id too short": "00-4bf92f3577b34da6a3ce929d0e0e47-00f067aa0ba902b7-01",
		"non hex":            "00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-00f067aa0ba902b7-01",
		"all zero trace id":  "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"forbidden version":  "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}

	for name, value := range malformed {
		t.Run(name, func(t *testing.T) {
			captor := &contextCaptor{}
			rec := httptest.NewRecorder()

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Traceparent", value)

			middleware.TraceID(captorHandler(captor)).ServeHTTP(rec, req)

			if captor.traceID == "" {
				t.Fatal("expected a generated trace id")
			}
			if strings.Contains(value, captor.traceID) && captor.traceID != "" {
				t.Fatalf("malformed traceparent %q was partially adopted as %q", value, captor.traceID)
			}
		})
	}
}

func TestTimeoutAnswersWithTheDocumentedEnvelope(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately ignores the context to prove the middleware still answers
		// when a handler misbehaves.
		time.Sleep(50 * time.Millisecond)
	})

	rec := httptest.NewRecorder()
	middleware.Timeout(10*time.Millisecond, quietLogger())(slow).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	var body struct {
		Success bool `json:"success"`
		Error   httpx.ErrorBody
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("timeout response is not the envelope: %v (%s)", err, rec.Body.String())
	}
	if body.Success {
		t.Error("expected success to be false")
	}
	if body.Error.Code != httpx.CodeRequestTimeout {
		t.Fatalf("expected code %s, got %s", httpx.CodeRequestTimeout, body.Error.Code)
	}
}

func TestTimeoutDoesNotOverrideACommittedResponse(t *testing.T) {
	// The handler commits a response and then overruns. Writing a second status
	// would be invalid, so the original response must stand.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
		time.Sleep(50 * time.Millisecond)
	})

	rec := httptest.NewRecorder()
	// Observability installs the wrapped writer into the context, which is how
	// Timeout learns the response was already committed.
	chain := middleware.Observability(quietLogger())(middleware.Timeout(10*time.Millisecond, quietLogger())(handler))

	chain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected the committed 200 to stand, got %d", rec.Code)
	}
}

func TestRecoverConvertsPanicToEnvelope(t *testing.T) {
	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})

	rec := httptest.NewRecorder()
	middleware.Recover(quietLogger())(panicking).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), httpx.CodeInternalError) {
		t.Fatalf("expected an envelope error, got %s", rec.Body.String())
	}
	// The panic value must not reach the client.
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("panic detail leaked to the client: %s", rec.Body.String())
	}
}

func TestRecoverRepanicsAbortHandler(t *testing.T) {
	aborting := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})

	defer func() {
		recovered := recover()
		if !errors.Is(recovered.(error), http.ErrAbortHandler) {
			t.Fatalf("expected http.ErrAbortHandler to propagate, got %v", recovered)
		}
	}()

	middleware.Recover(quietLogger())(aborting).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	t.Fatal("expected the abort handler panic to propagate")
}

func TestCORS(t *testing.T) {
	const allowed = "http://localhost:3000"
	handler := middleware.CORS([]string{allowed})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("allowlisted origin", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", allowed)

		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != allowed {
			t.Fatalf("expected the origin to be allowed, got %q", got)
		}
		if got := rec.Header().Get("Vary"); !strings.Contains(got, "Origin") {
			t.Fatalf("expected Vary: Origin for cache correctness, got %q", got)
		}
	})

	t.Run("unknown origin gets no permission", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", "http://evil.example.com")

		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("unknown origin must not be allowed, got %q", got)
		}
	})

	t.Run("preflight is answered", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodOptions, "/api/v1/orders", nil)
		req.Header.Set("Origin", allowed)
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204, got %d", rec.Code)
		}
		if rec.Header().Get("Access-Control-Allow-Methods") == "" {
			t.Fatal("expected the allowed methods to be advertised")
		}
	})
}

func TestSecurityHeadersAreApplied(t *testing.T) {
	handler := middleware.SecurityHeaders(middleware.SecurityHeadersOptions{})(captorHandler(&contextCaptor{}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("expected X-Content-Type-Options: nosniff")
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("expected X-Frame-Options: DENY")
	}
	// HSTS must be off unless explicitly enabled, since it is only meaningful
	// over TLS.
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS must not be sent when disabled")
	}
}

func TestObservabilityInstallsResponseWriterForRecovery(t *testing.T) {
	var captured bool
	handler := middleware.Observability(quietLogger())(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, captured = middleware.ResponseWriterFrom(r.Context())
		}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if !captured {
		t.Fatal("expected the wrapped response writer to be available on the context")
	}
}

func TestContextHandlerInjectsCorrelationIntoLogRecords(t *testing.T) {
	// The handler must annotate records without any call site remembering to,
	// which is the property that makes trace continuity reliable.
	var buf strings.Builder
	logger, err := logging.New("info", logging.FormatJSON, &buf)
	if err != nil {
		t.Fatalf("unexpected error building logger: %v", err)
	}

	ctx := logging.WithRequestID(context.Background(), "req-1")
	ctx = logging.WithTraceID(ctx, "trace-1")
	ctx = logging.WithOperationID(ctx, "op-1")

	logger.InfoContext(ctx, "something happened")

	for _, want := range []string{`"request_id":"req-1"`, `"trace_id":"trace-1"`, `"operation_id":"op-1"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log record is missing %s: %s", want, buf.String())
		}
	}
}

func TestLoggingRejectsUnknownLevelAndFormat(t *testing.T) {
	if _, err := logging.New("verbose", logging.FormatJSON, io.Discard); err == nil {
		t.Error("expected an unknown level to be rejected")
	}
	if _, err := logging.New("info", "xml", io.Discard); err == nil {
		t.Error("expected an unknown format to be rejected")
	}
}
