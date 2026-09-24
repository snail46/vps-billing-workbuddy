package operation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// StepFailure is what a Runner reports when a step does not succeed.
type StepFailure struct {
	Code    string
	Message string
	Retry   bool
}

func (f *StepFailure) Error() string { return f.Code + ": " + f.Message }

// Runner executes one attempt of an operation's workflow.
//
// A Runner owns the steps of its operation type. It receives a Ctx that records
// progress into the store — phase names, step outcomes — and returns when the
// attempt is done. Returning nil finishes the operation; returning a failure
// hands the decision of retrying to the classification below.
type Runner interface {
	// Steps declares the workflow's named stages, in order. The operation's
	// progress is these names' outcomes and nothing else.
	Steps(op Operation) []string
	// Run executes one attempt.
	Run(ctx context.Context, exec ExecutionContext) error
}

// ExecutionContext is what a Runner uses to record its own progress. Its
// methods persist immediately: a worker that dies mid-step leaves a record a
// retry can continue from, rather than a gap nobody can place.
type ExecutionContext interface {
	Operation() Operation
	// Phase records a human-readable stage name. It carries no number.
	Phase(phase, messageKey string) error
	// Begin opens one attempt of a named step.
	Begin(stepKey string) error
	// Succeed closes a step as done and advances the derived progress.
	Succeed(stepKey string) error
	// Skip closes a step as not applicable; it counts toward progress, because
	// the workflow reached it and decided.
	Skip(stepKey string) error
	// Fail closes a step as failed; the engine decides what happens next.
	Fail(stepKey string, failure StepFailure) error
	// Reserve promises capacity for this operation on a node, as a durable row.
	Reserve(nodeID uuid.UUID, spec ResourceSpec) error
	// Commit turns a kept promise into allocation, in the caller's transaction.
	Commit(nodeID uuid.UUID, spec ResourceSpec) error
	// Release withdraws a promise.
	Release(nodeID uuid.UUID, spec ResourceSpec) error
}

// ResourceSpec is the capacity one operation promises, in the units the node
// book keeps.
type ResourceSpec struct {
	CPUCores  float64
	MemoryMB  int64
	DiskGB    int64
	IPv4Count int
	IPv6Count int
	NATPorts  int
}

// DefaultMaxRetries is the retry budget an operation carries when its enqueue
// does not state one. Five attempts with the capped backoff is roughly four
// minutes of patience — long enough to ride out a provider blip, short enough
// that a dead workflow is visible inside a coffee break.
const DefaultMaxRetries = 5

// Store is the persistence the engine drives.
type Store interface {
	// ClaimQueued takes the oldest queued operation, or none.
	ClaimQueued(ctx context.Context, at time.Time) (Operation, bool, error)
	// ClaimRetryable takes one retry whose backoff elapsed, or none.
	ClaimRetryable(ctx context.Context, at time.Time) (Operation, bool, error)
	// ByID reads one operation.
	ByID(ctx context.Context, id uuid.UUID) (Operation, error)
	// Steps reads the declared steps.
	Steps(ctx context.Context, id uuid.UUID) ([]Step, error)
	// Transition moves the operation from the state the caller saw.
	Transition(ctx context.Context, id uuid.UUID, from, to, phase, messageKey string, failure *StepFailure, at time.Time) (bool, error)
	// ToRetrying advances the attempt count, stamps the backoff into run_after
	// and parks the operation. completedAttempts is the attempt number that just
	// finished; the next one starts after its backoff.
	ToRetrying(ctx context.Context, id uuid.UUID, from string, failure *StepFailure, completedAttempts int, at time.Time) (bool, error)
	// SetPhase records a stage name; never a number.
	SetPhase(ctx context.Context, id uuid.UUID, phase, messageKey string, at time.Time) error
	// StartStep opens one attempt of a step.
	StartStep(ctx context.Context, operationID uuid.UUID, stepKey string, at time.Time) (bool, error)
	// FinishStep closes one attempt and recomputes progress.
	FinishStep(ctx context.Context, operationID uuid.UUID, stepKey, status string, failure *StepFailure, at time.Time) error
	// Enqueue creates the operation and freezes its steps.
	Enqueue(ctx context.Context, op Operation, steps []Step, at time.Time) (Operation, error)
}

// ReservationStore is the capacity half of the persistence, implemented by the
// infrastructure store: the promise is a row that names the operation, and the
// counters move in the same transaction.
type ReservationStore interface {
	ReserveForOperation(ctx context.Context, operationID, nodeID uuid.UUID, spec ResourceSpec, at time.Time) error
	CommitReservation(ctx context.Context, operationID, nodeID uuid.UUID, spec ResourceSpec, at time.Time) error
	ReleaseReservation(ctx context.Context, operationID, nodeID uuid.UUID, spec ResourceSpec, at time.Time) error
}

// Enqueuer is what the callers that start work use.
type Enqueuer interface {
	// Enqueue creates the operation and its steps, idempotent by key.
	Enqueue(ctx context.Context, op Operation, steps []Step, at time.Time) (Operation, error)
}

// Engine runs claimed operations against their runners.
type Engine struct {
	store      Store
	fill       ReservationStore
	runners    map[string]Runner
	logger     *slog.Logger
	now        func() time.Time
	maxRetries int
}

// NewEngine builds the engine. maxRetries is the default for an operation that
// did not state its own.
func NewEngine(store Store, fill ReservationStore, logger *slog.Logger, maxRetries int) *Engine {
	return &Engine{
		store:      store,
		fill:       fill,
		runners:    map[string]Runner{},
		logger:     logger,
		now:        time.Now,
		maxRetries: maxRetries,
	}
}

// Register mounts a runner for its operation type. A second registration of
// the same type is a programming error at startup, not a runtime surprise.
func (e *Engine) Register(operationType string, runner Runner) error {
	if _, exists := e.runners[operationType]; exists {
		return fmt.Errorf("%w: %s", ErrUnknownRunner, "type already registered: "+operationType)
	}
	e.runners[operationType] = runner
	return nil
}

// runnerFor resolves the runner, mapping the miss to the error the enqueue
// path reports.
func (e *Engine) runnerFor(operationType string) (Runner, error) {
	runner, ok := e.runners[operationType]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownRunner, operationType)
	}
	return runner, nil
}

// Enqueue creates an operation for a registered runner, with its steps frozen
// at creation. The steps are the progress denominator for the operation's
// whole life, so a workflow that later gains a stage does not retroactively
// move the numbers of the operations already in flight.
func (e *Engine) Enqueue(ctx context.Context, opType, resourceType string, resourceID uuid.UUID,
	idempotencyKey, traceID string, stepKeys []string, at time.Time) (Operation, error) {
	if _, err := e.runnerFor(opType); err != nil {
		return Operation{}, err
	}
	if idempotencyKey == "" {
		return Operation{}, errors.New("operation: an enqueue without an idempotency key cannot be retried safely")
	}
	if traceID == "" {
		traceID = uuid.NewString()
	}

	op := Operation{
		ID:             uuid.New(),
		Type:           opType,
		ResourceType:   resourceType,
		ResourceID:     resourceID,
		Status:         StatusQueued,
		MaxRetries:     e.maxRetries,
		IdempotencyKey: idempotencyKey,
		TraceID:        traceID,
	}
	steps := make([]Step, 0, len(stepKeys))
	for i, key := range stepKeys {
		steps = append(steps, Step{
			ID:          uuid.New(),
			OperationID: op.ID,
			StepKey:     key,
			StepOrder:   i + 1,
			Status:      StepPending,
		})
	}
	return e.store.Enqueue(ctx, op, steps, at.UTC())
}

// Tick claims and runs one operation: queued first, then one whose backoff
// elapsed. A false result means the queues were empty; an error means the
// tick itself failed and the caller should log it.
func (e *Engine) Tick(ctx context.Context) (bool, error) {
	at := e.now().UTC()

	claimed, ok, err := e.store.ClaimQueued(ctx, at)
	if err != nil {
		return false, err
	}
	if !ok {
		claimed, ok, err = e.store.ClaimRetryable(ctx, at)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}

	e.execute(ctx, claimed, at)
	return true, nil
}

// execute drives one claimed operation through its runner, classifying the
// outcome. It never returns an error for the operation's own failure — a
// failed operation is a recorded fact, not a broken loop.
func (e *Engine) execute(ctx context.Context, op Operation, at time.Time) {
	runner, err := e.runnerFor(op.Type)
	if err != nil {
		// An unregistered type is a deployment mistake: the enqueue path should
		// have refused it. Park the operation as failed so it cannot be claimed
		// forever, and say so loudly.
		e.logger.ErrorContext(ctx, "no runner for the claimed operation",
			slog.String("operation_id", op.ID.String()),
			slog.String("type", op.Type))
		_, _ = e.store.Transition(ctx, op.ID, op.Status, StatusFailed, "", "",
			&StepFailure{Code: "OPERATION_NO_RUNNER", Message: "no runner is registered for this operation type"}, at)
		return
	}

	exec := &execution{engine: e, op: op, failure: nil}
	runErr := runner.Run(ctx, exec)

	switch {
	case runErr == nil:
		// The engine owns every status edge — the execution context offers no
		// status change for a reason, so the machine's record and the runner's
		// word cannot drift. A successful Run closes the operation; the
		// conditional UPDATE is a no-op if a concurrent writer got there first.
		if _, err := e.store.Transition(ctx, op.ID, op.Status, StatusSucceeded, "", "", nil, at); err != nil {
			e.logger.ErrorContext(ctx, "could not complete the operation",
				slog.String("operation_id", op.ID.String()), slog.String("error", err.Error()))
		}
	case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
		// The worker is shutting down under the operation. Back to retrying, so
		// the next worker picks the attempt up rather than the operation dying
		// with the process — the machine's answer to docs/18's restart drills.
		e.classify(ctx, op, &StepFailure{Code: "OPERATION_INTERRUPTED", Message: "the worker is shutting down", Retry: true}, at)
	default:
		var failure *StepFailure
		if errors.As(runErr, &failure) {
			e.classify(ctx, op, failure, at)
			return
		}
		// An unclassified error is retried with a generic code: the runner lost
		// the classification, but the operation must still be answered.
		e.classify(ctx, op, &StepFailure{
			Code:    "OPERATION_UNCLASSIFIED_FAILURE",
			Message: runErr.Error(),
			Retry:   true,
		}, at)
	}
}

// classify decides between retrying and failing, per ADR-008 §3.
func (e *Engine) classify(ctx context.Context, op Operation, failure *StepFailure, at time.Time) {
	if !failure.Retry || op.RetryCount >= op.MaxRetries {
		if _, err := e.store.Transition(ctx, op.ID, op.Status, StatusFailed, "", "",
			&StepFailure{Code: failure.Code, Message: failure.Message}, at); err != nil {
			e.logger.ErrorContext(ctx, "could not fail the operation",
				slog.String("operation_id", op.ID.String()), slog.String("error", err.Error()))
		}
		return
	}
	if _, err := e.store.ToRetrying(ctx, op.ID, op.Status,
		&StepFailure{Code: failure.Code, Message: failure.Message}, op.RetryCount+1, at); err != nil {
		e.logger.ErrorContext(ctx, "could not park the operation for retry",
			slog.String("operation_id", op.ID.String()), slog.String("error", err.Error()))
	}
}

// execution is the runner's handle onto the store.
type execution struct {
	engine  *Engine
	op      Operation
	failure *StepFailure
}

func (x *execution) Operation() Operation { return x.op }

func (x *execution) Phase(phase, messageKey string) error {
	return x.engine.store.SetPhase(context.Background(), x.op.ID, phase, messageKey, x.engine.now().UTC())
}

func (x *execution) Begin(stepKey string) error {
	ok, err := x.engine.store.StartStep(context.Background(), x.op.ID, stepKey, x.engine.now().UTC())
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("operation: the step %q cannot start from where it stands", stepKey)
	}
	return nil
}

func (x *execution) Succeed(stepKey string) error {
	return x.engine.store.FinishStep(context.Background(), x.op.ID, stepKey, StepSucceeded, nil, x.engine.now().UTC())
}

func (x *execution) Skip(stepKey string) error {
	return x.engine.store.FinishStep(context.Background(), x.op.ID, stepKey, StepSkipped, nil, x.engine.now().UTC())
}

func (x *execution) Fail(stepKey string, failure StepFailure) error {
	return x.engine.store.FinishStep(context.Background(), x.op.ID, stepKey, StepFailed,
		&StepFailure{Code: failure.Code, Message: failure.Message}, x.engine.now().UTC())
}

func (x *execution) Reserve(nodeID uuid.UUID, spec ResourceSpec) error {
	return x.engine.fill.ReserveForOperation(context.Background(), x.op.ID, nodeID, spec, x.engine.now().UTC())
}

func (x *execution) Commit(nodeID uuid.UUID, spec ResourceSpec) error {
	return x.engine.fill.CommitReservation(context.Background(), x.op.ID, nodeID, spec, x.engine.now().UTC())
}

func (x *execution) Release(nodeID uuid.UUID, spec ResourceSpec) error {
	return x.engine.fill.ReleaseReservation(context.Background(), x.op.ID, nodeID, spec, x.engine.now().UTC())
}
