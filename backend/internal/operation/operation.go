package operation

// The operation machine (ADR-008 §2).
//
// docs/05 fixes the vocabulary; the edges are this repository's decision, in the
// same table-and-test form as every other machine, because a transition an
// operator cannot find in a table is a transition that gets invented at 3am.

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Operation statuses — docs/05's machine.
const (
	StatusQueued          = "queued"
	StatusRunning         = "running"
	StatusWaitingProvider = "waiting_provider"
	StatusWaitingResource = "waiting_resource"
	StatusVerifying       = "verifying"
	StatusRetrying        = "retrying"
	StatusSucceeded       = "succeeded"
	StatusFailed          = "failed"
	StatusCancelled       = "cancelled"
)

// Step statuses.
const (
	StepPending   = "pending"
	StepRunning   = "running"
	StepSucceeded = "succeeded"
	StepFailed    = "failed"
	StepSkipped   = "skipped"
)

// operationTransitions is the machine.
var operationTransitions = map[string][]string{
	StatusQueued: {
		StatusRunning,   // claimed
		StatusCancelled, // cancelled before a worker reached it
	},
	StatusRunning: {
		StatusWaitingProvider,
		StatusWaitingResource,
		StatusVerifying,
		StatusRetrying,
		StatusSucceeded,
		StatusFailed,
		StatusCancelled,
	},
	StatusWaitingProvider: {
		StatusRunning,
		StatusRetrying,
		StatusFailed,
		StatusCancelled,
	},
	StatusWaitingResource: {
		StatusRunning,
		StatusRetrying,
		StatusFailed,
		StatusCancelled,
	},
	StatusVerifying: {
		StatusSucceeded,
		StatusRetrying,
		StatusFailed,
		StatusCancelled,
	},
	StatusRetrying: {
		StatusRunning, // the backoff elapsed; attempt+1
		StatusFailed,  // max_retries exhausted
		StatusCancelled,
	},
}

// OperationCanTransition reports whether the machine allows the move.
func OperationCanTransition(from, to string) bool {
	for _, next := range operationTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// OperationStatuses returns every status the machine knows.
func OperationStatuses() []string {
	return []string{
		StatusQueued, StatusRunning, StatusWaitingProvider, StatusWaitingResource,
		StatusVerifying, StatusRetrying, StatusSucceeded, StatusFailed, StatusCancelled,
	}
}

// OperationIsTerminal reports whether the operation has stopped for good.
func OperationIsTerminal(status string) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// Backoff is the delay before attempt n+1, exponential with a cap (ADR-008 §3):
// 2s, 4s, 8s ... capped at a minute, so a retrying operation is visible without
// being a storm.
const (
	BackoffBase = 2 * time.Second
	BackoffCap  = 60 * time.Second
)

// BackoffFor computes the delay after attempt n (1-based).
func BackoffFor(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := BackoffBase
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= BackoffCap {
			return BackoffCap
		}
	}
	return delay
}

// Reservation statuses — the lifecycle of one operation's promise of capacity.
const (
	ReservationReserved  = "reserved"
	ReservationCommitted = "committed"
	ReservationReleased  = "released"
	ReservationExpired   = "expired"
)

// ReservationExpiry is how long a `reserved` row holds capacity without its
// workflow confirming: past it, the sweep releases the counters, because a
// worker that died mid-provision must not hold a node forever (ADR-008 §5).
const ReservationExpiry = 15 * time.Minute

// Operation is one long action the platform is performing for a resource.
type Operation struct {
	ID                  uuid.UUID
	Type                string
	ResourceType        string
	ResourceID          uuid.UUID
	Status              string
	Phase               *string
	Progress            int
	MessageKey          *string
	ProviderID          *uuid.UUID
	ProviderOperationID *string
	IdempotencyKey      string
	Retryable           bool
	RetryCount          int
	MaxRetries          int
	ErrorCode           *string
	ErrorMessage        *string
	TraceID             string
	StartedAt           *time.Time
	FinishedAt          *time.Time
}

// Step is one named stage of the workflow behind an operation.
type Step struct {
	ID           uuid.UUID
	OperationID  uuid.UUID
	StepKey      string
	StepOrder    int
	Status       string
	Progress     int
	Attempt      int
	ErrorCode    *string
	ErrorMessage *string
}

// ProgressOf computes an operation's progress from its steps — the completed
// share, never a number a caller sent (docs/07: 进度按步骤映射).
func ProgressOf(steps []Step) int {
	if len(steps) == 0 {
		return 0
	}
	done := 0
	for i := range steps {
		switch steps[i].Status {
		case StepSucceeded, StepSkipped:
			done++
		}
	}
	return done * 100 / len(steps)
}

// Errors reported by the operation system.
var (
	// ErrNotFound reports an operation that does not exist, or one the caller
	// cannot see.
	ErrNotFound = errors.New("operation: no such operation")
	// ErrNotTerminalForCancel reports a cancellation against a finished operation.
	ErrNotTerminalForCancel = errors.New("operation: already finished")
	// ErrUnknownRunner reports an enqueue whose type no runner registered.
	ErrUnknownRunner = errors.New("operation: no runner for this type")
	// ErrReservationLost reports a commit or release whose reservation row is not
	// in the state the caller saw.
	ErrReservationLost = errors.New("operation: the reservation is no longer open")
)

// String renders a step for the failure lines an operator reads.
func (s Step) String() string {
	return fmt.Sprintf("%s(%s)", s.StepKey, s.Status)
}
