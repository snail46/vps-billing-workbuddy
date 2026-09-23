package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/health"
)

// fakeCheck lets a test decide the outcome of one dependency.
type fakeCheck struct {
	name string
	err  error
}

func (f fakeCheck) Name() string { return f.name }

func (f fakeCheck) Check(context.Context) error { return f.err }

func newHandler(checks ...health.Check) *health.Handler {
	return health.NewHandler(health.Options{
		Environment: "test",
		Checks:      checks,
		Timeout:     time.Second,
	})
}

func decodeReport(t *testing.T, rec *httptest.ResponseRecorder) health.Report {
	t.Helper()
	var report health.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("health response is not valid JSON: %v (%s)", err, rec.Body.String())
	}
	return report
}

func TestLiveReportsUpEvenWhenDependenciesFail(t *testing.T) {
	handler := newHandler(fakeCheck{name: "postgres", err: errors.New("connection refused")})

	rec := httptest.NewRecorder()
	handler.Live()(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))

	// Liveness must not depend on anything external: an orchestrator restarting
	// a healthy process because a database blipped is a self-inflicted outage.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	report := decodeReport(t, rec)
	if report.Status != health.StateUp {
		t.Fatalf("expected status up, got %q", report.Status)
	}
	if len(report.Checks) != 0 {
		t.Fatalf("liveness must consult no dependency, got %d checks", len(report.Checks))
	}
	if report.Environment != "test" {
		t.Fatalf("expected the environment to be reported, got %q", report.Environment)
	}
}

func TestReadyReportsUpWhenAllChecksPass(t *testing.T) {
	handler := newHandler(
		fakeCheck{name: "postgres"},
		fakeCheck{name: "redis"},
	)

	rec := httptest.NewRecorder()
	handler.Ready()(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	report := decodeReport(t, rec)
	if report.Status != health.StateUp {
		t.Fatalf("expected status up, got %q", report.Status)
	}
	if len(report.Checks) != 2 {
		t.Fatalf("expected 2 check results, got %d", len(report.Checks))
	}
	for _, check := range report.Checks {
		if check.Status != health.StateUp {
			t.Errorf("check %s should be up, got %q", check.Name, check.Status)
		}
	}
}

func TestReadyReportsDownAndNamesTheFailingDependency(t *testing.T) {
	handler := newHandler(
		fakeCheck{name: "postgres"},
		fakeCheck{name: "redis", err: errors.New("dial tcp: connection refused")},
	)

	rec := httptest.NewRecorder()
	handler.Ready()(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	report := decodeReport(t, rec)
	if report.Status != health.StateDown {
		t.Fatalf("expected status down, got %q", report.Status)
	}
	// Every check is still reported, so an operator sees which dependency is
	// responsible rather than only that something is wrong.
	if len(report.Checks) != 2 {
		t.Fatalf("expected both checks to be reported, got %d", len(report.Checks))
	}

	var found bool
	for _, check := range report.Checks {
		if check.Name == "redis" {
			found = true
			if check.Status != health.StateDown {
				t.Errorf("expected redis to be down, got %q", check.Status)
			}
			if check.Error == "" {
				t.Error("expected the failure reason to be reported for an operator")
			}
		}
	}
	if !found {
		t.Fatal("failing dependency was not reported")
	}
}

func TestReadyWithNoChecksIsUp(t *testing.T) {
	rec := httptest.NewRecorder()
	newHandler().Ready()(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with no registered checks, got %d", rec.Code)
	}
}

func TestReportCarriesBuildIdentity(t *testing.T) {
	rec := httptest.NewRecorder()
	newHandler().Live()(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))

	report := decodeReport(t, rec)
	// An operator must be able to tell which artefact answered the probe.
	if report.Version == "" || report.Commit == "" {
		t.Fatalf("expected version and commit to be reported, got %+v", report)
	}
	if report.Time.IsZero() {
		t.Fatal("expected a timestamp")
	}
}
