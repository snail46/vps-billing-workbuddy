// Package operationstore is the persistence boundary of the operation system:
// the machine's states and edges live in internal/operation, and this package is
// the only code that turns them into SQL.
package operationstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	sqlcgen "github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/operation"
)

// Store implements the operation system's persistence on PostgreSQL.
type Store struct {
	pool    *pgxpool.Pool
	queries *sqlcgen.Queries
}

// New builds a store over the pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, queries: sqlcgen.New(pool)}
}

// maxRetriesCeiling is the largest retry budget an enqueue may ask for; far
// above anything a workflow wants, far below the column's int32.
const maxRetriesCeiling = 1_000_000

// stepOrderCeiling is the largest step count a workflow may declare; the
// machine's workflows have single digits.
const stepOrderCeiling = 100_000

// Pool exposes the pool for callers that compose operations with their own
// transactional work.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Queries exposes the generated queries for callers inside a transaction.
func (s *Store) Queries() *sqlcgen.Queries { return s.queries }

// WithTx rebinds the store to a transaction, the same shape commerce's store
// uses, so an operation and the work it describes commit together.
func (s *Store) WithTx(ctx context.Context, fn func(tx *Store) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("operationstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(&Store{pool: s.pool, queries: sqlcgen.New(tx)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Enqueue inserts an operation in the queued state. It is idempotent by the
// caller's key: the workflow that retried after a timeout gets the operation it
// already created rather than a second one with the same promise.
func (s *Store) Enqueue(ctx context.Context, op operation.Operation, steps []operation.Step, at time.Time) (operation.Operation, error) {
	existing, err := s.queries.OperationByIdempotencyKey(ctx, op.IdempotencyKey)
	if err == nil {
		return fromRow(existing), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return operation.Operation{}, fmt.Errorf("operationstore: look up the idempotency key: %w", err)
	}

	err = s.queries.CreateOperation(ctx, sqlcgen.CreateOperationParams{
		ID:             op.ID,
		Type:           op.Type,
		ResourceType:   op.ResourceType,
		ResourceID:     op.ResourceID,
		IdempotencyKey: op.IdempotencyKey,
		// The engine's default budget is 5 and Enqueue caps what a caller may
		// raise it to; the guard is the proof at the conversion.
		MaxRetries: int32(min(op.MaxRetries, maxRetriesCeiling)), //nolint:gosec // clamped to the ceiling immediately above.
		TraceID:    op.TraceID,
		CreatedAt:  pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		// A concurrent enqueue with the same key won the race; its operation is
		// the answer either way.
		if raced, lookErr := s.queries.OperationByIdempotencyKey(ctx, op.IdempotencyKey); lookErr == nil {
			return fromRow(raced), nil
		}
		return operation.Operation{}, fmt.Errorf("operationstore: enqueue: %w", err)
	}

	for i := range steps {
		step := steps[i]
		if err := s.queries.CreateOperationStep(ctx, sqlcgen.CreateOperationStepParams{
			ID:          step.ID,
			OperationID: op.ID,
			StepKey:     step.StepKey,
			StepOrder:   int32(min(step.StepOrder, stepOrderCeiling)), //nolint:gosec // clamped to the ceiling immediately above.
			CreatedAt:   pgtype.Timestamptz{Time: at, Valid: true},
		}); err != nil {
			return operation.Operation{}, fmt.Errorf("operationstore: create the step %q: %w", step.StepKey, err)
		}
	}

	created, err := s.queries.OperationByID(ctx, op.ID)
	if err != nil {
		return operation.Operation{}, fmt.Errorf("operationstore: read the enqueued operation: %w", err)
	}
	return fromRow(created), nil
}

// ClaimQueued takes the oldest queued operation that no other worker holds.
// A false result is an empty queue, not a failure.
func (s *Store) ClaimQueued(ctx context.Context, at time.Time) (operation.Operation, bool, error) {
	row, err := s.queries.ClaimQueuedOperation(ctx, pgtype.Timestamptz{Time: at, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return operation.Operation{}, false, nil
	}
	if err != nil {
		return operation.Operation{}, false, fmt.Errorf("operationstore: claim: %w", err)
	}
	return fromRow(row), true, nil
}

// ClaimRetryable takes one retry whose backoff has elapsed.
func (s *Store) ClaimRetryable(ctx context.Context, at time.Time) (operation.Operation, bool, error) {
	row, err := s.queries.ClaimRetryableOperation(ctx, sqlcgen.ClaimRetryableOperationParams{
		UpdatedAt: pgtype.Timestamptz{Time: at, Valid: true},
		RunAfter:  pgtype.Timestamptz{Time: at, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return operation.Operation{}, false, nil
	}
	if err != nil {
		return operation.Operation{}, false, fmt.Errorf("operationstore: claim a retry: %w", err)
	}
	return fromRow(row), true, nil
}

// ByID reads one operation.
func (s *Store) ByID(ctx context.Context, id uuid.UUID) (operation.Operation, error) {
	row, err := s.queries.OperationByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return operation.Operation{}, operation.ErrNotFound
	}
	if err != nil {
		return operation.Operation{}, fmt.Errorf("operationstore: read the operation: %w", err)
	}
	return fromRow(row), nil
}

// ByIdempotencyKey reads the operation a caller's key created.
func (s *Store) ByIdempotencyKey(ctx context.Context, key string) (operation.Operation, error) {
	row, err := s.queries.OperationByIdempotencyKey(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return operation.Operation{}, operation.ErrNotFound
	}
	if err != nil {
		return operation.Operation{}, fmt.Errorf("operationstore: read by key: %w", err)
	}
	return fromRow(row), nil
}

// Steps reads an operation's steps in their declared order.
func (s *Store) Steps(ctx context.Context, id uuid.UUID) ([]operation.Step, error) {
	rows, err := s.queries.StepsForOperation(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("operationstore: read the steps: %w", err)
	}
	steps := make([]operation.Step, 0, len(rows))
	for i := range rows {
		steps = append(steps, stepFromRow(rows[i]))
	}
	return steps, nil
}

// Transition moves the operation from the state the caller saw and records the
// phase and message beside it. A false result means another writer moved it
// first — the answer to a race, not an error.
func (s *Store) Transition(ctx context.Context, id uuid.UUID, from, to string,
	phase, messageKey string, failure *operation.StepFailure, at time.Time) (bool, error) {
	tag, err := s.queries.TransitionOperation(ctx, sqlcgen.TransitionOperationParams{
		ID:           id,
		Status:       to,
		Phase:        textOrNull(phase),
		MessageKey:   textOrNull(messageKey),
		ErrorCode:    failureCode(failure),
		ErrorMessage: failureMessage(failure),
		UpdatedAt:    pgtype.Timestamptz{Time: at, Valid: true},
		Status_2:     from,
	})
	if err != nil {
		return false, fmt.Errorf("operationstore: transition: %w", err)
	}
	return tag == 1, nil
}

// ToRetrying sends a failed attempt back with its count advanced and its
// backoff stamped into run_after — the column the claim reads, because updated_at
// moves on every write and cannot carry a second meaning. A false result means
// the operation was moved by someone else meanwhile.
func (s *Store) ToRetrying(ctx context.Context, id uuid.UUID, from string,
	failure *operation.StepFailure, completedAttempts int, at time.Time) (bool, error) {
	tag, err := s.queries.MoveOperationToRetrying(ctx, sqlcgen.MoveOperationToRetryingParams{
		ID:           id,
		RunAfter:     pgtype.Timestamptz{Time: at.Add(operation.BackoffFor(completedAttempts)), Valid: true},
		ErrorCode:    failureCode(failure),
		ErrorMessage: failureMessage(failure),
		UpdatedAt:    pgtype.Timestamptz{Time: at, Valid: true},
		Status:       from,
	})
	if err != nil {
		return false, fmt.Errorf("operationstore: retry: %w", err)
	}
	return tag == 1, nil
}

// Cancel stops an unfinished operation. A false result means it had already
// finished.
func (s *Store) Cancel(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	tag, err := s.queries.CancelOperation(ctx, sqlcgen.CancelOperationParams{
		ID:         id,
		FinishedAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("operationstore: cancel: %w", err)
	}
	return tag == 1, nil
}

// SetPhase records where a long attempt is, without touching progress — the
// number is the steps' business and theirs alone.
func (s *Store) SetPhase(ctx context.Context, id uuid.UUID, phase, messageKey string, at time.Time) error {
	if _, err := s.queries.SetOperationPhase(ctx, sqlcgen.SetOperationPhaseParams{
		ID:         id,
		Phase:      textOrNull(phase),
		MessageKey: textOrNull(messageKey),
		UpdatedAt:  pgtype.Timestamptz{Time: at, Valid: true},
	}); err != nil {
		return fmt.Errorf("operationstore: set the phase: %w", err)
	}
	return nil
}

// SetProviderOperation records the provider-side identifier a poll waits on.
func (s *Store) SetProviderOperation(ctx context.Context, id uuid.UUID, providerOperationID string, at time.Time) error {
	if _, err := s.queries.SetOperationProvider(ctx, sqlcgen.SetOperationProviderParams{
		ID:                  id,
		ProviderOperationID: textOrNull(providerOperationID),
		UpdatedAt:           pgtype.Timestamptz{Time: at, Valid: true},
	}); err != nil {
		return fmt.Errorf("operationstore: set the provider operation: %w", err)
	}
	return nil
}

// StartStep begins one attempt of a step. A false result means the step is not
// startable from where it stands.
func (s *Store) StartStep(ctx context.Context, operationID uuid.UUID, stepKey string, at time.Time) (bool, error) {
	tag, err := s.queries.StartOperationStep(ctx, sqlcgen.StartOperationStepParams{
		OperationID: operationID,
		StartedAt:   pgtype.Timestamptz{Time: at, Valid: true},
		StepKey:     stepKey,
	})
	if err != nil {
		return false, fmt.Errorf("operationstore: start the step: %w", err)
	}
	return tag == 1, nil
}

// FinishStep ends one attempt of a step and recomputes the operation's
// progress from its steps in the same statement pair — the caller's number is
// never consulted.
func (s *Store) FinishStep(ctx context.Context, operationID uuid.UUID, stepKey, status string,
	failure *operation.StepFailure, at time.Time) error {
	if _, err := s.queries.FinishOperationStep(ctx, sqlcgen.FinishOperationStepParams{
		OperationID:  operationID,
		Status:       status,
		ErrorCode:    failureCode(failure),
		ErrorMessage: failureMessage(failure),
		FinishedAt:   pgtype.Timestamptz{Time: at, Valid: true},
		StepKey:      stepKey,
	}); err != nil {
		return fmt.Errorf("operationstore: finish the step: %w", err)
	}
	return s.SyncProgress(ctx, operationID, at)
}

// SyncProgress recomputes the operation's progress from its steps.
func (s *Store) SyncProgress(ctx context.Context, operationID uuid.UUID, at time.Time) error {
	if _, err := s.queries.SyncOperationProgress(ctx, sqlcgen.SyncOperationProgressParams{
		OperationID: operationID,
		UpdatedAt:   pgtype.Timestamptz{Time: at, Valid: true},
	}); err != nil {
		return fmt.Errorf("operationstore: sync the progress: %w", err)
	}
	return nil
}

func failureCode(f *operation.StepFailure) pgtype.Text {
	if f == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: f.Code, Valid: true}
}

func failureMessage(f *operation.StepFailure) pgtype.Text {
	if f == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: f.Message, Valid: true}
}

func textOrNull(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func fromRow(row sqlcgen.Operation) operation.Operation {
	return operation.Operation{
		ID:                  row.ID,
		Type:                row.Type,
		ResourceType:        row.ResourceType,
		ResourceID:          row.ResourceID,
		Status:              row.Status,
		Phase:               textPtr(row.Phase),
		Progress:            int(row.Progress),
		MessageKey:          textPtr(row.MessageKey),
		ProviderID:          row.ProviderID,
		ProviderOperationID: textPtr(row.ProviderOperationID),
		IdempotencyKey:      row.IdempotencyKey,
		Retryable:           row.Retryable,
		RetryCount:          int(row.RetryCount),
		MaxRetries:          int(row.MaxRetries),
		ErrorCode:           textPtr(row.ErrorCode),
		ErrorMessage:        textPtr(row.ErrorMessage),
		TraceID:             row.TraceID,
		StartedAt:           timePtr(row.StartedAt),
		FinishedAt:          timePtr(row.FinishedAt),
		CreatedAt:           row.CreatedAt.Time.UTC(),
	}
}

func stepFromRow(row sqlcgen.OperationStep) operation.Step {
	return operation.Step{
		ID:           row.ID,
		OperationID:  row.OperationID,
		StepKey:      row.StepKey,
		StepOrder:    int(row.StepOrder),
		Status:       row.Status,
		Progress:     int(row.Progress),
		Attempt:      int(row.Attempt),
		ErrorCode:    textPtr(row.ErrorCode),
		ErrorMessage: textPtr(row.ErrorMessage),
		StartedAt:    timePtr(row.StartedAt),
		FinishedAt:   timePtr(row.FinishedAt),
	}
}

func textPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}
