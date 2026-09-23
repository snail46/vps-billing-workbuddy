// Package health implements the liveness and readiness probes required by
// docs/17-DEPLOYMENT.md.
//
//	/health/live   process is alive; no dependency is consulted
//	/health/ready  every platform-critical dependency is reachable
//
// Responses are plain JSON rather than the docs/08 envelope. The envelope
// describes the `/api/v1` product API consumed by the web clients; these
// endpoints are consumed by the container runtime and by operators, and a probe
// that needs to unwrap an application envelope to learn a status code would be
// worse for it.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/version"
)

// State describes the outcome of a health evaluation.
type State string

const (
	// StateUp means everything evaluated successfully.
	StateUp State = "up"
	// StateDown means at least one critical dependency is unreachable.
	StateDown State = "down"
)

// defaultTimeout bounds the whole readiness evaluation.
const defaultTimeout = 3 * time.Second

// Check is a readiness dependency.
//
// Only platform-critical dependencies belong in the readiness set. docs/17 is
// explicit that a single offline Provider must never make the platform as a
// whole unready, so Provider reachability is reported by its own admin-facing
// health surface (introduced with the infrastructure phases) and must not be
// registered here.
type Check interface {
	// Name identifies the dependency in the report.
	Name() string
	// Check returns nil when the dependency is reachable. The context carries
	// the evaluation deadline.
	Check(ctx context.Context) error
}

// CheckResult is the per-dependency outcome.
type CheckResult struct {
	Name      string `json:"name"`
	Status    State  `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// Report is the probe response body.
type Report struct {
	Status        State         `json:"status"`
	Version       string        `json:"version"`
	Commit        string        `json:"commit"`
	Environment   string        `json:"environment"`
	UptimeSeconds int64         `json:"uptime_seconds"`
	Checks        []CheckResult `json:"checks,omitempty"`
	Time          time.Time     `json:"time"`
}

// Options configures a Handler.
type Options struct {
	Environment string
	Checks      []Check
	// Timeout bounds the whole readiness evaluation. A zero value uses
	// defaultTimeout.
	Timeout time.Duration
	Logger  *slog.Logger
}

// Handler serves the probe endpoints.
type Handler struct {
	environment string
	checks      []Check
	timeout     time.Duration
	logger      *slog.Logger
	startedAt   time.Time
}

// NewHandler builds a probe handler.
func NewHandler(opts Options) *Handler {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		environment: opts.Environment,
		checks:      opts.Checks,
		timeout:     timeout,
		logger:      logger,
		startedAt:   time.Now(),
	}
}

// Live reports process liveness.
//
// It deliberately consults no dependency: a process that is running must not be
// restarted because a database is briefly unavailable, and reporting a
// dependency as down here would cause exactly that under an orchestrator.
func (h *Handler) Live() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.write(w, r, http.StatusOK, Report{
			Status:        StateUp,
			Version:       version.Version,
			Commit:        version.Commit,
			Environment:   h.environment,
			UptimeSeconds: int64(time.Since(h.startedAt).Seconds()),
			Time:          time.Now().UTC(),
		})
	}
}

// Ready reports whether every critical dependency is reachable.
//
// All checks share one deadline, so the total evaluation time is bounded
// regardless of how many dependencies are registered.
func (h *Handler) Ready() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
		defer cancel()

		results := make([]CheckResult, 0, len(h.checks))
		status := StateUp

		for _, check := range h.checks {
			start := time.Now()
			err := check.Check(ctx)
			result := CheckResult{
				Name:      check.Name(),
				Status:    StateUp,
				LatencyMS: time.Since(start).Milliseconds(),
			}
			if err != nil {
				result.Status = StateDown
				// The dependency's own error text is safe to expose here: these
				// endpoints are internal, and an operator needs the reason.
				result.Error = err.Error()
				status = StateDown
			}
			results = append(results, result)
		}

		code := http.StatusOK
		if status != StateUp {
			code = http.StatusServiceUnavailable
			h.logger.WarnContext(ctx, "readiness check failed",
				slog.String("status", string(status)))
		}

		h.write(w, r, code, Report{
			Status:        status,
			Version:       version.Version,
			Commit:        version.Commit,
			Environment:   h.environment,
			UptimeSeconds: int64(time.Since(h.startedAt).Seconds()),
			Checks:        results,
			Time:          time.Now().UTC(),
		})
	}
}

func (h *Handler) write(w http.ResponseWriter, r *http.Request, code int, report Report) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)

	if err := json.NewEncoder(w).Encode(report); err != nil {
		// The status line is already sent; the only useful action is to record
		// that the probe response was truncated.
		h.logger.ErrorContext(r.Context(), "failed to encode health report",
			slog.String("error", err.Error()))
	}
}
