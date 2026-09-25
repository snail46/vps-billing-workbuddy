// Package instanceop runs the customer's own instance workflows — restart and
// reinstall — behind the operation engine's runner interface (ADR-011 §2).
// The HTTP layer accepts the request; this package is what actually does it,
// with the same backoff, the same progress record and the same provider
// discipline the provision chain runs under.
package instanceop

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/operation"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
	infrastore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/infra"
	instancestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/instance"
)

// The two operation types the customer's actions enqueue.
const (
	OperationTypeRestart   = "instance.restart"
	OperationTypeReinstall = "instance.reinstall"
)

// The steps. A restart is a provider round-trip and a verification; a
// reinstall is the same with a new image recorded. Three steps each — the
// progress denominator is small because the work is.
var (
	RestartSteps   = []string{"validate_instance", "restart_provider", "verify_running"}
	ReinstallSteps = []string{"validate_instance", "reinstall_provider", "verify_running"}
)

// Enqueuer is what the action paths use to start a workflow.
type Enqueuer interface {
	Enqueue(ctx context.Context, opType, resourceType string, resourceID uuid.UUID,
		idempotencyKey, traceID string, stepKeys []string, at time.Time) (operation.Operation, error)
}

// EnqueueRestart creates the restart workflow for one instance. The key names
// the request, not the instance: the guard against a second live workflow is
// the open-operation check at the action path, and a customer who restarts
// again after one finishes is not replaying anything.
func EnqueueRestart(ctx context.Context, engine Enqueuer, instanceID uuid.UUID, at time.Time) (operation.Operation, error) {
	return engine.Enqueue(ctx, OperationTypeRestart, "instance", instanceID,
		"restart:instance:"+instanceID.String()+":"+uuid.NewString(), "", RestartSteps, at)
}

// EnqueueReinstall creates the reinstall workflow for one instance.
func EnqueueReinstall(ctx context.Context, engine Enqueuer, instanceID uuid.UUID, image string, at time.Time) (operation.Operation, error) {
	return engine.Enqueue(ctx, OperationTypeReinstall, "instance", instanceID,
		"reinstall:instance:"+instanceID.String()+":"+image+":"+uuid.NewString(), "", ReinstallSteps, at)
}

// Deps are the collaborators the runner needs.
type Deps struct {
	Instances *instancestore.Store
	Nodes     *infrastore.Store
	Providers map[string]provider.Provider
	Logger    *slog.Logger
}

// Runner executes one attempt of a restart or a reinstall.
type Runner struct {
	deps Deps
}

// NewRunner builds the runner.
func NewRunner(deps Deps) *Runner {
	return &Runner{deps: deps}
}

// Steps implements operation.Runner; the frozen steps follow the type.
func (*Runner) Steps(op operation.Operation) []string {
	if op.Type == OperationTypeReinstall {
		return ReinstallSteps
	}
	return RestartSteps
}

// Run executes one attempt.
func (r *Runner) Run(ctx context.Context, exec operation.ExecutionContext) error {
	instanceID := exec.Operation().ResourceID
	isReinstall := exec.Operation().Type == OperationTypeReinstall

	// validate_instance — the machine must exist and be running; a restart of
	// a machine the provider never finished building is a caller's mistake,
	// not a retryable hiccup.
	if err := exec.Begin("validate_instance"); err != nil {
		return err
	}
	instance, ok, err := r.deps.Instances.ByID(ctx, instanceID)
	if err != nil {
		return err
	}
	if !ok {
		return failStep(exec, "validate_instance", &operation.StepFailure{
			Code: "INSTANCE_NOT_FOUND", Message: "no instance " + instanceID.String(),
		})
	}
	if instance.ObservedState != instancestore.ObservedRunning {
		return failStep(exec, "validate_instance", &operation.StepFailure{
			Code:    "INSTANCE_NOT_RUNNING",
			Message: "the instance is " + instance.ObservedState,
		})
	}
	if instance.ProviderInstanceID == nil || instance.ProviderID == nil {
		return failStep(exec, "validate_instance", &operation.StepFailure{
			Code: "INSTANCE_UNPLACED", Message: "the instance has no provider identity",
		})
	}
	gateway, err := r.providerByID(ctx, *instance.ProviderID)
	if err != nil {
		return failStep(exec, "validate_instance", &operation.StepFailure{
			Code: "PROVIDER_UNAVAILABLE", Message: err.Error(), Retry: true,
		})
	}
	// The machine is about to be worked on; the record says so from here, so a
	// crash mid-workflow leaves an observation that matches reality.
	if err := r.deps.Instances.MarkObserved(ctx, instance.ID,
		map[bool]string{true: "reinstalling", false: "restarting"}[isReinstall],
		time.Now().UTC()); err != nil {
		return err
	}
	if err := exec.Succeed("validate_instance"); err != nil {
		return err
	}

	// The provider round-trip. The adapters are synchronous from here: an LXD
	// operation is waited on inside the adapter, and a wait that outlives its
	// budget comes back as PROVIDER_TIMEOUT — retryable, so the engine's
	// backoff is what rides out a slow provider.
	action := provider.InstanceActionRequest{
		OperationID:        exec.Operation().ID.String(),
		IdempotencyKey:     exec.Operation().IdempotencyKey,
		NodeID:             nodeIDOrEmpty(instance),
		ProviderInstanceID: *instance.ProviderInstanceID,
	}
	if isReinstall {
		if err := exec.Begin("reinstall_provider"); err != nil {
			return err
		}
		image := reinstallImage(exec.Operation().IdempotencyKey)
		created, err := gateway.ReinstallInstance(ctx, provider.ReinstallInstanceRequest{
			InstanceActionRequest: action,
			Image:                 image,
		})
		if err != nil {
			return failStep(exec, "reinstall_provider", &operation.StepFailure{
				Code: "PROVIDER_REINSTALL_FAILED", Message: err.Error(), Retry: true,
			})
		}
		if !created.Accepted {
			return failStep(exec, "reinstall_provider", &operation.StepFailure{
				Code:    "PROVIDER_REINSTALL_REJECTED",
				Message: stringOr(created.Status, "the provider did not accept the reinstall"),
				Retry:   true,
			})
		}
		if err := r.deps.Instances.SetImage(ctx, instance.ID, image, time.Now().UTC()); err != nil {
			return err
		}
		if err := exec.Succeed("reinstall_provider"); err != nil {
			return err
		}
	} else {
		if err := exec.Begin("restart_provider"); err != nil {
			return err
		}
		restarted, err := gateway.RestartInstance(ctx, action)
		if err != nil {
			return failStep(exec, "restart_provider", &operation.StepFailure{
				Code: "PROVIDER_RESTART_FAILED", Message: err.Error(), Retry: true,
			})
		}
		if !restarted.Accepted {
			return failStep(exec, "restart_provider", &operation.StepFailure{
				Code:    "PROVIDER_RESTART_REJECTED",
				Message: stringOr(restarted.Status, "the provider did not accept the restart"),
				Retry:   true,
			})
		}
		if err := exec.Succeed("restart_provider"); err != nil {
			return err
		}
	}

	// verify_running — the provider's word is what the record shows (docs/05:
	// 不得猜), exactly as the provision chain verifies.
	if err := exec.Begin("verify_running"); err != nil {
		return err
	}
	report, err := gateway.GetInstance(ctx, provider.GetInstanceRequest{
		NodeID:             nodeIDOrEmpty(instance),
		PlatformInstanceID: instance.ID.String(),
		ProviderInstanceID: *instance.ProviderInstanceID,
	})
	if err != nil {
		return failStep(exec, "verify_running", &operation.StepFailure{
			Code: "PROVIDER_UNREACHABLE", Message: err.Error(), Retry: true,
		})
	}
	if report.State != "running" {
		return failStep(exec, "verify_running", &operation.StepFailure{
			Code:    "INSTANCE_NOT_RUNNING",
			Message: "the provider reports " + report.State,
			Retry:   true,
		})
	}
	if err := r.deps.Instances.MarkObserved(ctx, instance.ID, instancestore.ObservedRunning, time.Now().UTC()); err != nil {
		return err
	}
	return exec.Succeed("verify_running")
}

// reinstallImage reads the requested image from the reinstall's idempotency
// key. The key is the operation's own record of what was asked for —
// `reinstall:instance:<id>:<image>:<nonce>` — because the engine's enqueue
// carries no parameter field, and a workflow whose input survives its own
// retries is safer than one that re-derives it. The image segment is
// validated at the action path (no colons, no emptiness), so the split is
// total.
func reinstallImage(idempotencyKey string) string {
	// reinstall:instance:<id>:<image>:<nonce> — four colons, image in the
	// fourth segment.
	const prefix = "reinstall:instance:"
	if len(idempotencyKey) <= len(prefix) {
		return ""
	}
	rest := idempotencyKey[len(prefix):]
	first := indexByte(rest, ':')
	if first < 0 {
		return ""
	}
	rest = rest[first+1:]
	second := indexByte(rest, ':')
	if second < 0 {
		return rest
	}
	return rest[:second]
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// providerByID resolves the registered gateway behind the provider row the
// instance was provisioned under — by the row's identity, never by guessing a
// name (ADR-007: capability-driven, row-backed).
func (r *Runner) providerByID(ctx context.Context, providerID uuid.UUID) (provider.Provider, error) {
	rows, err := r.deps.Nodes.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("instanceop: read the provider rows: %w", err)
	}
	for i := range rows {
		if rows[i].ID != providerID {
			continue
		}
		if gateway, ok := r.deps.Providers[rows[i].Name]; ok {
			return gateway, nil
		}
		return nil, fmt.Errorf("instanceop: no gateway registered for provider %q", rows[i].Name)
	}
	return nil, fmt.Errorf("instanceop: no provider row %s", providerID)
}

func nodeIDOrEmpty(instance instancestore.Instance) string {
	if instance.NodeID == nil {
		return ""
	}
	return instance.NodeID.String()
}

func stringOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// failStep closes the step as failed and hands the engine the classified
// failure; the engine, not the runner, decides between retrying and failing.
func failStep(exec operation.ExecutionContext, stepKey string, failure *operation.StepFailure) error {
	if err := exec.Fail(stepKey, *failure); err != nil {
		return err
	}
	return failure
}

// StoreEnqueuer creates the operation row directly. The HTTP process has no
// engine and needs none: what it writes is a queued row, and the worker's
// registered runner is what the row meets (docs/02: HTTP never runs work).
type StoreEnqueuer interface {
	Enqueue(ctx context.Context, op operation.Operation, steps []operation.Step, at time.Time) (operation.Operation, error)
}

// EnqueueRestartOnStore creates the restart workflow through the store.
func EnqueueRestartOnStore(ctx context.Context, store StoreEnqueuer, instanceID uuid.UUID, at time.Time) (operation.Operation, error) {
	return store.Enqueue(ctx, newOperation(OperationTypeRestart, instanceID,
		"restart:instance:"+instanceID.String()+":"+uuid.NewString(), at),
		stepsOf(RestartSteps), at)
}

// EnqueueReinstallOnStore creates the reinstall workflow through the store.
// The requested image rides in the idempotency key — see reinstallImage —
// because the enqueue carries no parameter field and the workflow's input
// must survive its own retries.
func EnqueueReinstallOnStore(ctx context.Context, store StoreEnqueuer, instanceID uuid.UUID, image string, at time.Time) (operation.Operation, error) {
	return store.Enqueue(ctx, newOperation(OperationTypeReinstall, instanceID,
		"reinstall:instance:"+instanceID.String()+":"+image+":"+uuid.NewString(), at),
		stepsOf(ReinstallSteps), at)
}

func newOperation(opType string, resourceID uuid.UUID, key string, at time.Time) operation.Operation {
	return operation.Operation{
		ID:             uuid.New(),
		Type:           opType,
		ResourceType:   "instance",
		ResourceID:     resourceID,
		Status:         operation.StatusQueued,
		MaxRetries:     operation.DefaultMaxRetries,
		IdempotencyKey: key,
		TraceID:        uuid.NewString(),
		CreatedAt:      at.UTC(),
	}
}

func stepsOf(keys []string) []operation.Step {
	steps := make([]operation.Step, 0, len(keys))
	for i, key := range keys {
		steps = append(steps, operation.Step{
			ID:        uuid.New(),
			StepKey:   key,
			StepOrder: i + 1,
			Status:    operation.StepPending,
		})
	}
	return steps
}
