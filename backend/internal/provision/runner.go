// Package provision walks docs/07's provision chain behind the operation
// engine's runner interface, and bridges the subscription's activation event
// to the operation that does the work (ADR-009).
package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/commerce"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/infra"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/operation"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/outbox"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
	infrastore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/infra"
	instancestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/instance"
)

// OperationType is the type string the provision runner is registered under.
const OperationType = "provision.instance"

// The steps, in docs/07's order. The network steps are declared even though
// the vertical slice skips them: the progress denominator is the document's
// chain, and Phase 7's real provider fills them in without moving the numbers
// of the operations already in flight (ADR-009 §4).
var Steps = []string{
	"validate_subscription",
	"select_node",
	"reserve_resources",
	"create_instance",
	"wait_provider",
	"configure_network",
	"verify_running",
	"persist_network",
	"commit_reservation",
	"activate_subscription",
	"notify",
}

// BridgeDeps are the collaborators the activation bridge needs.
type BridgeDeps struct {
	Engine        *operation.Engine
	Subscriptions *commerce.Service
}

// EnqueueForSubscription creates the provision operation for one subscription.
// It is idempotent by the subscription: a retried bridge keeps the operation
// it already created, which is what makes a hundred replays of the same
// callback produce one workflow (ADR-009 §2).
func EnqueueForSubscription(ctx context.Context, deps BridgeDeps, subscriptionID uuid.UUID, at time.Time) (operation.Operation, error) {
	return deps.Engine.Enqueue(ctx, OperationType, "subscription", subscriptionID,
		"provision:subscription:"+subscriptionID.String(), "", Steps, at)
}

// HandleSubscriptionActivated is the outbox handler: the subscription's own
// event starts its provisioning.
func HandleSubscriptionActivated(deps BridgeDeps) func(context.Context, outbox.Event) error {
	return func(ctx context.Context, event outbox.Event) error {
		var payload struct {
			SubscriptionID string `json:"subscription_id"`
			UserID         string `json:"user_id"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			// A payload the bridge cannot read is a producer bug; it must not be
			// retried into the same wall forever.
			return fmt.Errorf("provision: unreadable payload for %s: %w", event.ID, err)
		}
		subscriptionID, err := uuid.Parse(payload.SubscriptionID)
		if err != nil {
			return fmt.Errorf("provision: payload names no subscription: %w", err)
		}
		if _, err := uuid.Parse(payload.UserID); err != nil {
			return fmt.Errorf("provision: payload names no user: %w", err)
		}
		_, err = EnqueueForSubscription(ctx, deps, subscriptionID, time.Now().UTC())
		return err
	}
}

// Deps are the collaborators the runner needs.
type Deps struct {
	Subscriptions *commerce.Service
	Nodes         *infrastore.Store
	Instances     *instancestore.Store
	Providers     map[string]provider.Provider
	Outbox        OutboxRecorder
	Logger        *slog.Logger
}

// OutboxRecorder records an event beside the change it describes.
type OutboxRecorder interface {
	RecordOutboxEvent(ctx context.Context, event commerce.OutboxEvent) error
}

// Runner executes one attempt of the provision chain.
type Runner struct {
	deps Deps
}

// NewRunner builds the runner.
func NewRunner(deps Deps) *Runner {
	return &Runner{deps: deps}
}

// Steps implements operation.Runner.
func (*Runner) Steps(_ operation.Operation) []string { return Steps }

// Run executes one attempt, one step at a time, recording each outcome as it
// happens so a worker that dies mid-chain leaves a record a retry continues
// from rather than a gap (ADR-008's execution context contract).
func (r *Runner) Run(ctx context.Context, exec operation.ExecutionContext) error {
	subscriptionID := exec.Operation().ResourceID

	// validate_subscription — the subscription must be live; nothing else in
	// the chain makes sense for a cancelled one.
	if err := exec.Begin("validate_subscription"); err != nil {
		return err
	}
	subscription, err := r.deps.Subscriptions.SystemSubscription(ctx, subscriptionID)
	if err != nil {
		return r.failStep(exec, "validate_subscription", &operation.StepFailure{
			Code: "SUBSCRIPTION_NOT_FOUND", Message: err.Error(),
		})
	}
	if subscription.Status != commerce.SubscriptionActive {
		return r.failStep(exec, "validate_subscription", &operation.StepFailure{
			Code:    "SUBSCRIPTION_NOT_LIVE",
			Message: "the subscription is " + subscription.Status,
			Retry:   true,
		})
	}
	plan, err := r.deps.Subscriptions.SystemPlan(ctx, subscription.PlanID)
	if err != nil {
		return r.failStep(exec, "validate_subscription", &operation.StepFailure{
			Code: "PLAN_NOT_FOUND", Message: err.Error(),
		})
	}
	if err := exec.Succeed("validate_subscription"); err != nil {
		return err
	}
	if err := exec.Phase("selected", "operation.phase.selecting_node"); err != nil {
		return err
	}

	// select_node — deterministic: the same book state picks the same node, so
	// a reconcile that re-runs selection does not move the customer.
	if err := exec.Begin("select_node"); err != nil {
		return err
	}
	nodes, err := r.deps.Nodes.ListNodes(ctx)
	if err != nil {
		return r.failStep(exec, "select_node", &operation.StepFailure{
			Code: "NODES_UNAVAILABLE", Message: err.Error(), Retry: true,
		})
	}
	spec := infra.Spec{
		CPUCores: parseCores(plan.CpuCores),
		MemoryMB: int64(plan.MemoryMB),
		DiskGB:   int64(plan.DiskGB),
	}
	node, err := infra.SelectNode(nodes, spec)
	if err != nil {
		return r.failStep(exec, "select_node", &operation.StepFailure{
			Code:    "CAPACITY_UNAVAILABLE",
			Message: "no node can hold " + plan.Slug + " right now",
			Retry:   true,
		})
	}
	if err := exec.Succeed("select_node"); err != nil {
		return err
	}

	// reserve_resources — the durable receipt; a retried reserve keeps the
	// promise it already holds.
	if err := exec.Begin("reserve_resources"); err != nil {
		return err
	}
	rspec := resourceSpecOf(plan)
	if err := exec.Reserve(node.ID, rspec); err != nil {
		return r.failStep(exec, "reserve_resources", &operation.StepFailure{
			Code: "RESERVATION_REFUSED", Message: err.Error(), Retry: true,
		})
	}
	if err := exec.Succeed("reserve_resources"); err != nil {
		return err
	}

	// create_instance — idempotent by the subscription: the retry finds the
	// instance the first attempt recorded instead of creating a second one.
	if err := exec.Begin("create_instance"); err != nil {
		return err
	}
	providerGateway, providerRowID, err := r.pickProvider(ctx, plan.Virtualization)
	if err != nil {
		return r.failStep(exec, "create_instance", &operation.StepFailure{
			Code: "NO_CAPABLE_PROVIDER", Message: err.Error(),
		})
	}
	instance, exists, err := r.deps.Instances.BySubscription(ctx, subscription.ID)
	if err != nil {
		return r.failStep(exec, "create_instance", &operation.StepFailure{
			Code: "INSTANCE_READ_FAILED", Message: err.Error(), Retry: true,
		})
	}
	if !exists {
		instance = instancestore.Instance{
			ID:             uuid.New(),
			SubscriptionID: subscription.ID,
			Name:           "vps-" + subscription.ID.String()[:8],
			DesiredState:   instancestore.DesiredRunning,
			ObservedState:  instancestore.ObservedProvisioning,
			CPUCores:       spec.CPUCores,
			MemoryMB:       spec.MemoryMB,
			DiskGB:         spec.DiskGB,
			ImageID:        nil,
		}
		if err := r.deps.Instances.Create(ctx, instance, time.Now().UTC()); err != nil {
			return r.failStep(exec, "create_instance", &operation.StepFailure{
				Code: "INSTANCE_WRITE_FAILED", Message: err.Error(), Retry: true,
			})
		}
	}
	if instance.ProviderInstanceID == nil {
		created, err := providerGateway.CreateInstance(ctx, provider.CreateInstanceRequest{
			OperationID:    exec.Operation().ID.String(),
			IdempotencyKey: "provision:" + subscription.ID.String(),
			NodeID:         node.ID.String(),
			InstanceID:     instance.ID.String(),
			Name:           instance.Name,
			CPUCores:       spec.CPUCores,
			MemoryMB:       spec.MemoryMB,
			DiskGB:         spec.DiskGB,
			Image:          "debian-12",
			Virtualization: plan.Virtualization,
		})
		if err != nil {
			return r.failStep(exec, "create_instance", &operation.StepFailure{
				Code: "PROVIDER_CREATE_FAILED", Message: err.Error(), Retry: true,
			})
		}
		if !created.Accepted {
			return r.failStep(exec, "create_instance", &operation.StepFailure{
				Code:    "PROVIDER_CREATE_REJECTED",
				Message: stringOr(created.Status, "the provider did not accept the create"),
				Retry:   true,
			})
		}
		// The provider names the machine it just made; until this identifier is
		// in the record, the platform cannot ask the provider about its own
		// instance — which is exactly the failure the first draft of this chain
		// hit at verify.
		providerInstanceID, _ := created.Metadata["instance_id"].(string)
		if providerInstanceID == "" {
			return r.failStep(exec, "create_instance", &operation.StepFailure{
				Code:    "PROVIDER_CREATE_UNNAMED",
				Message: "the provider accepted the create but named no instance",
				Retry:   true,
			})
		}
		instance.ProviderInstanceID = &providerInstanceID
	}
	if err := exec.Succeed("create_instance"); err != nil {
		return err
	}

	// wait_provider — the mock answers synchronously; the record of the wait is
	// still the record, and a real provider replaces it in Phase 7.
	if err := exec.Begin("wait_provider"); err != nil {
		return err
	}
	if err := exec.Succeed("wait_provider"); err != nil {
		return err
	}

	// configure_network / persist_network — reached and skipped: the mock has
	// no network to configure, and a skipped step counts toward the progress
	// the machine derives (ADR-009 §4).
	if err := exec.Begin("configure_network"); err != nil {
		return err
	}
	if err := exec.Skip("configure_network"); err != nil {
		return err
	}
	if err := exec.Begin("persist_network"); err != nil {
		return err
	}
	if err := exec.Skip("persist_network"); err != nil {
		return err
	}

	// verify_running — the provider's word is what the record shows; the
	// platform never reports running on its own say-so (docs/05: 不得猜).
	if err := exec.Begin("verify_running"); err != nil {
		return err
	}
	report, err := providerGateway.GetInstance(ctx, provider.GetInstanceRequest{
		NodeID:             node.ID.String(),
		PlatformInstanceID: instance.ID.String(),
		// The provider knows its own machines by the name it gave them; the
		// platform's identifier means nothing beyond this record.
		ProviderInstanceID: *instance.ProviderInstanceID,
	})
	if err != nil {
		return r.failStep(exec, "verify_running", &operation.StepFailure{
			Code: "PROVIDER_UNREACHABLE", Message: err.Error(), Retry: true,
		})
	}
	if report.State != "running" {
		return r.failStep(exec, "verify_running", &operation.StepFailure{
			Code:    "INSTANCE_NOT_RUNNING",
			Message: "the provider reports " + report.State,
			Retry:   true,
		})
	}
	if instance.ProviderInstanceID == nil || *instance.ProviderInstanceID != report.ProviderInstanceID {
		// The provider answered for a machine we did not ask it about: stop
		// rather than adopt a stranger's instance.
		return r.failStep(exec, "verify_running", &operation.StepFailure{
			Code:    "PROVIDER_INSTANCE_MISMATCH",
			Message: "the provider answered for " + report.ProviderInstanceID,
		})
	}
	if err := exec.Succeed("verify_running"); err != nil {
		return err
	}
	if err := r.deps.Instances.MarkProvisioned(ctx, instance.ID, node.ID, providerRowID,
		report.ProviderInstanceID, time.Now().UTC()); err != nil {
		return r.failStep(exec, "verify_running", &operation.StepFailure{
			Code: "INSTANCE_WRITE_FAILED", Message: err.Error(), Retry: true,
		})
	}

	// commit_reservation — the promise becomes allocation; the receipt closes.
	if err := exec.Begin("commit_reservation"); err != nil {
		return err
	}
	if err := exec.Commit(node.ID, rspec); err != nil {
		return r.failStep(exec, "commit_reservation", &operation.StepFailure{
			Code: "RESERVATION_LOST", Message: err.Error(), Retry: true,
		})
	}
	if err := exec.Succeed("commit_reservation"); err != nil {
		return err
	}

	// activate_subscription — the settlement already did this; the chain
	// records the fact rather than repeating it.
	if err := exec.Begin("activate_subscription"); err != nil {
		return err
	}
	if err := exec.Skip("activate_subscription"); err != nil {
		return err
	}

	// notify — the row the user reads later, and the event the rest of the
	// platform reacts to (ADR-009 §6).
	if err := exec.Begin("notify"); err != nil {
		return err
	}
	if err := r.deps.Outbox.RecordOutboxEvent(ctx, commerce.OutboxEvent{
		ID:            uuid.New(),
		EventType:     "instance.provisioned.v1",
		AggregateType: "instance",
		AggregateID:   instance.ID,
		Payload: map[string]any{
			"instance_id":          instance.ID.String(),
			"subscription_id":      subscription.ID.String(),
			"provider_instance_id": report.ProviderInstanceID,
		},
	}); err != nil {
		return r.failStep(exec, "notify", &operation.StepFailure{
			Code: "OUTBOX_WRITE_FAILED", Message: err.Error(), Retry: true,
		})
	}
	if err := r.deps.Instances.RecordNotification(ctx, instancestore.Notification{
		ID:         uuid.New(),
		UserID:     subscription.UserID,
		Type:       "instance.provisioned",
		TitleKey:   "notification.instance_provisioned.title",
		MessageKey: "notification.instance_provisioned.message",
		Parameters: notificationParameters(instance.ID, subscription.ID, report.ProviderInstanceID),
		Severity:   "success",
	}); err != nil {
		return r.failStep(exec, "notify", &operation.StepFailure{
			Code: "NOTIFICATION_WRITE_FAILED", Message: err.Error(), Retry: true,
		})
	}
	return exec.Succeed("notify")
}

// failStep closes the step as failed and hands the engine the classified
// failure; the engine, not the runner, decides between retrying and failing.
func (*Runner) failStep(exec operation.ExecutionContext, stepKey string, failure *operation.StepFailure) error {
	if err := exec.Fail(stepKey, *failure); err != nil {
		return err
	}
	return failure
}

// pickProvider finds the registered provider whose row is active and whose
// capabilities include the plan's virtualization — capability-driven, never
// name-driven (ADR-007). It returns the gateway and its row's identifier,
// because the instance record names the row the work ran under.
func (r *Runner) pickProvider(ctx context.Context, virtualization string) (provider.Provider, uuid.UUID, error) {
	rows, err := r.deps.Nodes.ListProviders(ctx)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("provision: read the provider rows: %w", err)
	}
	for _, gateway := range r.deps.Providers {
		caps, err := gateway.Capabilities(ctx)
		if err != nil {
			continue
		}
		serves := false
		for _, runtime := range caps.SupportedRuntimes {
			if runtime == virtualization {
				serves = true
				break
			}
		}
		if !serves {
			continue
		}
		for i := range rows {
			row := &rows[i]
			if row.Name == gateway.Name() && row.Status == "active" {
				return gateway, row.ID, nil
			}
		}
	}
	return nil, uuid.Nil, fmt.Errorf("provision: no active provider offers %q", virtualization)
}

func resourceSpecOf(plan commerce.Plan) operation.ResourceSpec {
	return operation.ResourceSpec{
		CPUCores: parseCores(plan.CpuCores),
		MemoryMB: int64(plan.MemoryMB),
		DiskGB:   int64(plan.DiskGB),
	}
}

func parseCores(text string) float64 {
	var cores float64
	// The plan's cpu_cores is an exact decimal carried as text; a parse failure
	// is a row the catalogue phase did not write, and zero will be refused by
	// the capacity arithmetic downstream.
	_, _ = fmt.Sscanf(text, "%g", &cores)
	return cores
}

func stringOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// notificationParameters renders the notification's parameter object once, so
// the row the user reads and the event the platform reacts to carry the same
// facts.
func notificationParameters(instanceID, subscriptionID uuid.UUID, providerInstanceID string) []byte {
	encoded, err := json.Marshal(map[string]any{
		"instance_id":          instanceID.String(),
		"subscription_id":      subscriptionID.String(),
		"provider_instance_id": providerInstanceID,
	})
	if err != nil {
		// json.Marshal of a map of strings cannot fail; a failure would mean a
		// non-serialisable value slipped in, and an empty object beats a panic.
		return []byte("{}")
	}
	return encoded
}
