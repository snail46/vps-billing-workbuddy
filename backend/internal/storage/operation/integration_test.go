package operationstore_test

// The engine's integration suite: the machine, the queue and the receipts
// against real PostgreSQL. The runners are test doubles that record what the
// execution context was told, so the assertions are about the record the
// engine leaves, not about a mock's internals.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/operation"
	operationstore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/operation"
)

func newOperationEnv(t *testing.T) (*operation.Engine, *operationstore.Store, *pgxpool.Pool) {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; integration test skipped")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	store := operationstore.New(pool)
	engine := operation.NewEngine(store, nil, slog.Default(), operation.DefaultMaxRetries)
	return engine, store, pool
}

// recorderRunner executes a scripted attempt sequence and records the context
// calls, so the assertions can check the record rather than the double.
type recorderRunner struct {
	steps   []string
	scripts [][]error // one entry per Run invocation; nil means success
	calls   []string
}

func (r *recorderRunner) Steps(op operation.Operation) []string { return r.steps }

func (r *recorderRunner) Run(ctx context.Context, exec operation.ExecutionContext) error {
	attempt := len(r.calls)
	r.calls = append(r.calls, "run")
	script := []error(nil)
	if attempt < len(r.scripts) {
		script = r.scripts[attempt]
	}
	for i, stepKey := range r.steps {
		if err := exec.Begin(stepKey); err != nil {
			return err
		}
		var outcome error = nil
		if i < len(script) {
			outcome = script[i]
		}
		if outcome == nil {
			if err := exec.Succeed(stepKey); err != nil {
				return err
			}
			continue
		}
		var failure *operation.StepFailure
		if errors.As(outcome, &failure) {
			if err := exec.Fail(stepKey, *failure); err != nil {
				return err
			}
		}
		return outcome
	}
	return nil
}

// drain runs the engine until the queues are empty, so a test's Tick claims
// the operation it enqueued rather than one a previous test left behind on the
// shared database. Stale rows fail with OPERATION_NO_RUNNER — this engine does
// not know their types — which is the drain, and the proof the engine parks
// unknown types.
func drain(t *testing.T, engine *operation.Engine) {
	t.Helper()
	for {
		claimed, err := engine.Tick(context.Background())
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
		if !claimed {
			return
		}
	}
}

func stepFailure(code string) *operation.StepFailure {
	return &operation.StepFailure{Code: code, Message: code, Retry: true}
}

func permanentFailure(code string) *operation.StepFailure {
	return &operation.StepFailure{Code: code, Message: code, Retry: false}
}

func TestAnOperationIsIdempotentByKey(t *testing.T) {
	engine, store, _ := newOperationEnv(t)
	if err := engine.Register("probe.success", &recorderRunner{steps: []string{"only"}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	ctx := context.Background()
	at := time.Now().UTC()
	drain(t, engine)

	key := "op-idem-" + uuid.NewString()
	first, err := engine.Enqueue(ctx, "probe.success", "probe", uuid.New(), key, "", []string{"only"}, at)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	second, err := engine.Enqueue(ctx, "probe.success", "probe", uuid.New(), key, "", []string{"only"}, at)
	if err != nil {
		t.Fatalf("enqueue again: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("the retry created a second operation (%s then %s)", first.ID, second.ID)
	}

	// An unknown type is refused before anything is written: the deployment
	// mistake is an error at enqueue time, not a queued operation that fails
	// when a worker reaches it.
	if _, err := engine.Enqueue(ctx, "probe.missing", "probe", uuid.New(), "op-missing-"+uuid.NewString(), "", []string{"only"}, at); !errors.Is(err, operation.ErrUnknownRunner) {
		t.Errorf("an unregistered type produced %v", err)
	}
	_ = store
}

func TestTheEngineRunsASuccessfulWorkflow(t *testing.T) {
	engine, store, _ := newOperationEnv(t)
	runner := &recorderRunner{steps: []string{"reserve", "create", "verify"}}
	if err := engine.Register("probe.lifecycle", runner); err != nil {
		t.Fatalf("register: %v", err)
	}
	ctx := context.Background()
	at := time.Now().UTC()
	drain(t, engine)

	created, err := engine.Enqueue(ctx, "probe.lifecycle", "probe", uuid.New(),
		"op-lifecycle-"+uuid.NewString(), "", []string{"reserve", "create", "verify"}, at)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if claimed, err := engine.Tick(ctx); err != nil || !claimed {
		t.Fatalf("tick: claimed=%v err=%v", claimed, err)
	}

	op, err := store.ByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("read the operation: %v", err)
	}
	if op.Status != operation.StatusSucceeded {
		t.Fatalf("the operation is %q, expected succeeded", op.Status)
	}
	if op.Progress != 100 {
		t.Errorf("progress = %d, expected 100", op.Progress)
	}
	if op.StartedAt == nil || op.FinishedAt == nil {
		t.Errorf("a finished operation must carry both timestamps (%v, %v)", op.StartedAt, op.FinishedAt)
	}

	steps, err := store.Steps(ctx, created.ID)
	if err != nil {
		t.Fatalf("read the steps: %v", err)
	}
	for i := range steps {
		if steps[i].Status != operation.StepSucceeded {
			t.Errorf("step %s is %s, expected succeeded", steps[i].StepKey, steps[i].Status)
		}
		if steps[i].Attempt != 1 {
			t.Errorf("step %s ran %d times on the first attempt", steps[i].StepKey, steps[i].Attempt)
		}
	}
}

func TestARetryableFailureParksAndThenSucceeds(t *testing.T) {
	engine, store, _ := newOperationEnv(t)
	runner := &recorderRunner{
		steps: []string{"attempt"},
		scripts: [][]error{
			{stepFailure("PROVIDER_TIMEOUT")},
			{stepFailure("PROVIDER_TIMEOUT")},
			nil, // the third attempt succeeds
		},
	}
	if err := engine.Register("probe.retry", runner); err != nil {
		t.Fatalf("register: %v", err)
	}
	ctx := context.Background()
	at := time.Now().UTC()
	drain(t, engine)

	created, err := engine.Enqueue(ctx, "probe.retry", "probe", uuid.New(),
		"op-retry-"+uuid.NewString(), "", []string{"attempt"}, at)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Attempt 1 fails and parks.
	if claimed, err := engine.Tick(ctx); err != nil || !claimed {
		t.Fatalf("first tick: claimed=%v err=%v", claimed, err)
	}
	op, _ := store.ByID(ctx, created.ID)
	if op.Status != operation.StatusRetrying {
		t.Fatalf("after a retryable failure the operation is %q, expected retrying", op.Status)
	}
	if op.RetryCount != 1 || op.ErrorCode == nil || *op.ErrorCode != "PROVIDER_TIMEOUT" {
		t.Errorf("the parked operation is %+v", op)
	}

	// The queue is emptied around it — the shared database may hold due
	// retries from other runs, and they are this engine's business too — and
	// while the backoff runs, our operation stays parked.
	for i := 0; i < 16; i++ {
		claimed, err := engine.Tick(ctx)
		if err != nil {
			t.Fatalf("a drain tick failed: %v", err)
		}
		if !claimed {
			break
		}
		if got, _ := store.ByID(ctx, created.ID); got.Status != operation.StatusRetrying {
			t.Fatalf("our operation left the parked state early: %q", got.Status)
		}
	}

	// Simulate the elapsed backoff by rewinding the row's clock.
	if _, err := store.Pool().Exec(ctx,
		`UPDATE operations SET run_after = $2 WHERE id = $1`,
		created.ID, at.Add(-operation.BackoffFor(1))); err != nil {
		t.Fatalf("rewind the clock: %v", err)
	}

	// Attempt 2 fails and parks again. Ticks may claim other rows first; the
	// loop ends when ours has been retried.
	for i := 0; i < 16; i++ {
		got, _ := store.ByID(ctx, created.ID)
		if got.RetryCount >= 2 {
			break
		}
		if claimed, err := engine.Tick(ctx); err != nil {
			t.Fatalf("a retry tick failed: %v", err)
		} else if !claimed {
			t.Fatal("the queue emptied without our operation being retried")
		}
	}
	op, _ = store.ByID(ctx, created.ID)
	if op.RetryCount != 2 {
		t.Fatalf("the retry count is %d after the second attempt", op.RetryCount)
	}

	// Attempt 3 succeeds.
	if _, err := store.Pool().Exec(ctx,
		`UPDATE operations SET run_after = $2 WHERE id = $1`,
		created.ID, at.Add(-operation.BackoffFor(2))); err != nil {
		t.Fatalf("rewind the clock: %v", err)
	}
	for i := 0; i < 16; i++ {
		got, _ := store.ByID(ctx, created.ID)
		if got.Status == operation.StatusSucceeded {
			return
		}
		if claimed, err := engine.Tick(ctx); err != nil {
			t.Fatalf("a final tick failed: %v", err)
		} else if !claimed {
			break
		}
	}
	op, _ = store.ByID(ctx, created.ID)
	if op.Status != operation.StatusSucceeded {
		t.Fatalf("the retried workflow ended %q, expected succeeded", op.Status)
	}
}

func TestAPermanentFailureFailsWithoutRetrying(t *testing.T) {
	engine, store, _ := newOperationEnv(t)
	runner := &recorderRunner{
		steps:   []string{"validate"},
		scripts: [][]error{{permanentFailure("REQUEST_INVALID")}},
	}
	if err := engine.Register("probe.permanent", runner); err != nil {
		t.Fatalf("register: %v", err)
	}
	ctx := context.Background()
	drain(t, engine)

	created, err := engine.Enqueue(ctx, "probe.permanent", "probe", uuid.New(),
		"op-permanent-"+uuid.NewString(), "", []string{"validate"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if claimed, err := engine.Tick(ctx); err != nil || !claimed {
		t.Fatalf("tick: claimed=%v err=%v", claimed, err)
	}

	op, _ := store.ByID(ctx, created.ID)
	if op.Status != operation.StatusFailed {
		t.Fatalf("a permanent failure left the operation %q", op.Status)
	}
	if op.RetryCount != 0 {
		t.Errorf("the retry count is %d; a permanent failure spends no budget", op.RetryCount)
	}
	if op.ErrorCode == nil || *op.ErrorCode != "REQUEST_INVALID" {
		t.Errorf("the failure carries %v", op.ErrorCode)
	}
	if op.FinishedAt == nil {
		t.Error("a failed operation must carry its finish time")
	}
}

func TestAnUnknownTypeNeverRunsAndIsFailedByTheEngine(t *testing.T) {
	// A runner is registered, then the registration map is bypassed by writing
	// the row directly — the deployment-drift case the execute path must park.
	engine, store, _ := newOperationEnv(t)
	ctx := context.Background()
	at := time.Now().UTC()

	// Enqueue through the engine of a type that exists, then mutate the type to
	// one that does not, which is what a bad deploy looks like to a claimed row.
	if err := engine.Register("probe.any", &recorderRunner{steps: []string{"only"}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	drain(t, engine)
	created, err := engine.Enqueue(ctx, "probe.any", "probe", uuid.New(),
		"op-orphan-"+uuid.NewString(), "", []string{"only"}, at)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := store.Pool().Exec(ctx,
		`UPDATE operations SET type = 'probe.vanished' WHERE id = $1`, created.ID); err != nil {
		t.Fatalf("mutate the type: %v", err)
	}

	if claimed, err := engine.Tick(ctx); err != nil || !claimed {
		t.Fatalf("tick: claimed=%v err=%v", claimed, err)
	}
	op, _ := store.ByID(ctx, created.ID)
	if op.Status != operation.StatusFailed || op.ErrorCode == nil || *op.ErrorCode != "OPERATION_NO_RUNNER" {
		t.Errorf("an unclaimed type ended as %+v", op)
	}
}
