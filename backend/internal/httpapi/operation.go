// The operations surface: an operator watches the work the platform performs.
//
// The customer-facing stream arrives with the vertical slice that owns the
// resource ownership join; until then the machine's readers are the operators
// whose permissions the seed already grants.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/identity"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/authmw"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/httpx"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/operation"
	operationstore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/operation"
)

// OperationsDeps are the collaborators the operations surface needs.
type OperationsDeps struct {
	Store *operationstore.Store
}

// operationPayload is one operation as the API renders it.
type operationPayload struct {
	ID                  string     `json:"id"`
	Type                string     `json:"type"`
	ResourceType        string     `json:"resource_type"`
	ResourceID          string     `json:"resource_id"`
	Status              string     `json:"status"`
	Phase               *string    `json:"phase"`
	Progress            int        `json:"progress"`
	MessageKey          *string    `json:"message_key"`
	ProviderOperationID *string    `json:"provider_operation_id"`
	RetryCount          int        `json:"retry_count"`
	MaxRetries          int        `json:"max_retries"`
	ErrorCode           *string    `json:"error_code"`
	ErrorMessage        *string    `json:"error_message"`
	TraceID             string     `json:"trace_id"`
	StartedAt           *time.Time `json:"started_at"`
	FinishedAt          *time.Time `json:"finished_at"`
	CreatedAt           time.Time  `json:"created_at"`
}

type operationStepPayload struct {
	Key          string     `json:"key"`
	Order        int        `json:"order"`
	Status       string     `json:"status"`
	Progress     int        `json:"progress"`
	Attempt      int        `json:"attempt"`
	ErrorCode    *string    `json:"error_code"`
	ErrorMessage *string    `json:"error_message"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

type operationDetailPayload struct {
	Operation operationPayload       `json:"operation"`
	Steps     []operationStepPayload `json:"steps"`
}

// getOperation answers with the operation and its steps.
func (a *api) getOperation(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok || principal.Session.Subject != identity.SubjectAdmin {
		httpx.WriteError(w, r, a.logger, httpx.ErrForbidden())
		return
	}

	operationID, err := uuid.Parse(chi.URLParam(r, "operationID"))
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrValidation())
		return
	}

	op, err := a.operations.Store.ByID(r.Context(), operationID)
	if err != nil {
		if errors.Is(err, operation.ErrNotFound) {
			httpx.WriteError(w, r, a.logger, httpx.ErrNotFound())
			return
		}
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}
	steps, err := a.operations.Store.Steps(r.Context(), operationID)
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}

	httpx.WriteData(w, r, http.StatusOK, operationDetailPayload{
		Operation: newOperationPayload(op),
		Steps:     newOperationSteps(steps),
	})
}

// streamEvents is the operation's SSE stream (ADR-008 §4): the row is read on
// a second, an update is pushed when the row changed, and the stream ends when
// the machine reaches a terminal state — the client's view and the record are
// the same thing, with no second channel to keep honest.
func (a *api) streamEvents(w http.ResponseWriter, r *http.Request) {
	principal, ok := authmw.PrincipalFrom(r.Context())
	if !ok || principal.Session.Subject != identity.SubjectAdmin {
		httpx.WriteError(w, r, a.logger, httpx.ErrForbidden())
		return
	}

	operationID, err := uuid.Parse(r.URL.Query().Get("operation_id"))
	if err != nil {
		httpx.WriteError(w, r, a.logger, httpx.ErrValidation())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.WriteError(w, r, a.logger, httpx.ErrInternal())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	lastJSON := ""
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-ticker.C:
			op, err := a.operations.Store.ByID(r.Context(), operationID)
			if err != nil {
				if errors.Is(err, operation.ErrNotFound) {
					// The operation vanished mid-stream: tell the client once and
					// end, rather than spinning on a missing row.
					a.sendEvent(w, flusher, operationID, map[string]any{"deleted": true})
					return
				}
				a.logger.ErrorContext(r.Context(), "the stream could not read the operation",
					slog.String("operation_id", operationID.String()), slog.String("error", err.Error()))
				return
			}

			encoded, err := json.Marshal(newOperationPayload(op))
			if err != nil {
				return
			}
			if string(encoded) == lastJSON {
				continue
			}
			a.sendEvent(w, flusher, operationID, map[string]any{
				"operation": op,
				"terminal":  operation.OperationIsTerminal(op.Status),
			})
			lastJSON = string(encoded)

			if operation.OperationIsTerminal(op.Status) {
				_, _ = fmt.Fprint(w, "event: done\ndata: {}\n\n")
				flusher.Flush()
				return
			}
		}
	}
}

// sendEvent writes one envelope per docs/09: event type, aggregate, data.
func (*api) sendEvent(w http.ResponseWriter, flusher http.Flusher, aggregateID uuid.UUID, data map[string]any) {
	envelope, err := json.Marshal(map[string]any{
		"event_type":     "operation.updated.v1",
		"occurred_at":    time.Now().UTC().Format(time.RFC3339Nano),
		"aggregate_type": "operation",
		"aggregate_id":   aggregateID.String(),
		"data":           data,
	})
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: operation.updated\ndata: %s\n\n", envelope)
	flusher.Flush()
}

func newOperationPayload(op operation.Operation) operationPayload {
	return operationPayload{
		ID:                  op.ID.String(),
		Type:                op.Type,
		ResourceType:        op.ResourceType,
		ResourceID:          op.ResourceID.String(),
		Status:              op.Status,
		Phase:               op.Phase,
		Progress:            op.Progress,
		MessageKey:          op.MessageKey,
		ProviderOperationID: op.ProviderOperationID,
		RetryCount:          op.RetryCount,
		MaxRetries:          op.MaxRetries,
		ErrorCode:           op.ErrorCode,
		ErrorMessage:        op.ErrorMessage,
		TraceID:             op.TraceID,
		StartedAt:           op.StartedAt,
		FinishedAt:          op.FinishedAt,
		CreatedAt:           op.CreatedAt,
	}
}

func newOperationSteps(steps []operation.Step) []operationStepPayload {
	payloads := make([]operationStepPayload, 0, len(steps))
	for i := range steps {
		step := steps[i]
		payloads = append(payloads, operationStepPayload{
			Key:          step.StepKey,
			Order:        step.StepOrder,
			Status:       step.Status,
			Progress:     step.Progress,
			Attempt:      step.Attempt,
			ErrorCode:    step.ErrorCode,
			ErrorMessage: step.ErrorMessage,
			StartedAt:    step.StartedAt,
			FinishedAt:   step.FinishedAt,
		})
	}
	return payloads
}
