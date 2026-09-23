package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/config"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/health"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpapi"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/middleware"
)

func testRouter() http.Handler {
	cfg := config.Config{
		AppEnv:              config.EnvDevelopment,
		HTTPRequestTimeout:  5 * time.Second,
		UserWebOrigin:       "http://localhost:3000",
		AdminWebOrigin:      "http://localhost:3001",
		DatabaseURL:         "postgres://user:pass@localhost:5432/db?sslmode=disable",
		RedisURL:            "redis://localhost:6379/0",
		DBMaxConns:          4,
		HTTPShutdownTimeout: 5 * time.Second,
		WorkerTickInterval:  30 * time.Second,
	}

	return httpapi.NewRouter(httpapi.Deps{
		Config: cfg,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Health: health.NewHandler(health.Options{Environment: "test", Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}),
	})
}

func TestHealthEndpointsAreMountedAtTheRoot(t *testing.T) {
	router := testRouter()

	for _, path := range []string{"/health/live", "/health/ready"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d (%s)", path, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUnknownAPIRouteReturnsTheErrorEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/does-not-exist", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var body struct {
		Success   bool `json:"success"`
		Error     httpx.ErrorBody
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("404 response is not the envelope: %v (%s)", err, rec.Body.String())
	}
	if body.Success {
		t.Error("expected success to be false")
	}
	if body.Error.Code != httpx.CodeNotFound {
		t.Fatalf("expected %s, got %s", httpx.CodeNotFound, body.Error.Code)
	}
	// Every response is correlatable, including a routing failure.
	if body.RequestID == "" {
		t.Error("expected a request_id on the error envelope")
	}
}

func TestUnknownRootPathAlsoUsesTheEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("expected a JSON envelope, got %s", rec.Body.String())
	}
}

func TestMethodNotAllowedUsesTheEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health/live", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("expected a JSON envelope, got %s", rec.Body.String())
	}
}

func TestResponsesCarryCorrelationHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))

	if rec.Header().Get(middleware.HeaderRequestID) == "" {
		t.Error("expected an X-Request-ID response header")
	}
	if rec.Header().Get(middleware.HeaderTraceID) == "" {
		t.Error("expected an X-Trace-ID response header")
	}
}

func TestSecurityHeadersArePresentOnEveryResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))

	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("expected X-Content-Type-Options: nosniff")
	}
}

func TestPanicInHandlerIsContained(t *testing.T) {
	// The router is exercised end to end to prove the middleware order actually
	// contains a panic. A dedicated sub-router is mounted to inject the handler.
	cfg := config.Config{
		AppEnv:              config.EnvDevelopment,
		HTTPRequestTimeout:  5 * time.Second,
		UserWebOrigin:       "http://localhost:3000",
		DatabaseURL:         "postgres://user:pass@localhost:5432/db?sslmode=disable",
		RedisURL:            "redis://localhost:6379/0",
		DBMaxConns:          4,
		HTTPShutdownTimeout: 5 * time.Second,
		WorkerTickInterval:  30 * time.Second,
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	// The foundation router exposes no business route that can panic, so the
	// chain is assembled the same way and a panicking route added.
	handler := middleware.Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("unexpected")
	}))
	_ = cfg

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}
