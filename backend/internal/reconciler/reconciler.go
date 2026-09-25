// Package reconciler notices what no individual workflow can: drift between
// the wish and the record, workflows that died with their worker, a create
// the provider finished but the platform timed out on, and nodes that went
// quiet (ADR-014). Every check is a read with conditional writes, bounded to
// a page per pass, safe to run beside itself.
package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
	runmanstore "github.com/snail46/vps-billing-workbuddy/backend/internal/runman"
	infrastore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/infra"
	instancestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/instance"
	operationstore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/operation"
)

// Thresholds the reconciler runs with. A workflow writes as it goes, so ten
// minutes of silence means it is dead; an agent is stale after five minutes
// without a heartbeat; a timed-out create is only worth adopting while the
// provision attempt is still recent.
const (
	staleOperationAfter = 10 * time.Minute
	staleAgentAfter     = 5 * time.Minute
	adoptWindow         = 24 * time.Hour
)

// Deps are the collaborators the reconciler reads through.
type Deps struct {
	Operations *operationstore.Store
	Instances  *instancestore.Store
	Nodes      NodesStore
	Providers  map[string]provider.Provider
	Runman     *runmanstore.Store
	Logger     *slog.Logger
}

// NodesStore reads the provider rows the placement resolution joins against.
// The infra store satisfies it; the narrow interface keeps this package from
// reaching for the whole infrastructure surface.
type NodesStore interface {
	ListProviders(ctx context.Context) ([]infrastore.ProviderRecord, error)
}

// Result is one pass's outcome, for the worker's log and the operator's
// dashboard.
type Result struct {
	CancelledStaleOperations int
	ReobservedDrifted        int
	AdoptedTimedOutCreates   int
	StaleAgents              int
}

// Run executes one pass.
func (d *Deps) Run(ctx context.Context, now time.Time) (Result, error) {
	result := Result{}

	result.CancelledStaleOperations = d.cancelStaleOperations(ctx, now)
	if err := d.reobserveDrift(ctx); err != nil {
		return result, err
	}
	adopted, err := d.adoptTimedOutCreates(ctx, now)
	if err != nil {
		return result, err
	}
	result.AdoptedTimedOutCreates = adopted
	stale, err := d.staleAgents(ctx, now)
	if err != nil {
		return result, err
	}
	result.StaleAgents = stale
	return result, nil
}

// cancelStaleOperations cancels live workflows whose record has gone silent.
// The conditional cancel is the same transition the platform's own API uses,
// so a workflow that moved a heartbeat earlier loses nothing.
func (d *Deps) cancelStaleOperations(ctx context.Context, now time.Time) int {
	stale, err := d.Operations.StaleLiveOperations(ctx, now.Add(-staleOperationAfter))
	if err != nil {
		d.log("read stale operations", err)
		return 0
	}
	cancelled := 0
	for i := range stale {
		op := &stale[i]
		ok, err := d.Operations.Cancel(ctx, op.ID, now)
		if err != nil {
			d.log("cancel a stale operation", err)
			continue
		}
		if ok {
			cancelled++
			d.log("cancelled a stale operation",
				fmt.Errorf("%s (%s, phase %s)", op.ID, op.Type, stringOr(op.Phase)), slog.LevelWarn)
		}
	}
	return cancelled
}

// reobserveDrift asks the provider what the machine's state actually is and
// writes that — never the wish.
func (d *Deps) reobserveDrift(ctx context.Context) error {
	drifted, _, err := d.Instances.Drifting(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: read drifting instances: %w", err)
	}
	for i := range drifted {
		inst := &drifted[i]
		gateway, err := d.providerByID(ctx, *inst.ProviderID)
		if err != nil {
			d.log("resolve a drifting instance's provider", err)
			continue
		}
		report, err := gateway.GetInstance(ctx, provider.GetInstanceRequest{
			NodeID:             inst.NodeID.String(),
			ProviderInstanceID: *inst.ProviderInstanceID,
		})
		if err != nil {
			d.log("observe a drifting instance", err)
			continue
		}
		if err := d.Instances.MarkObserved(ctx, inst.ID, report.State, time.Now().UTC()); err != nil {
			d.log("record an observation", err)
			continue
		}
	}
	return nil
}

// adoptTimedOutCreates finishes what the engine could not: a provision chain
// that failed on timeout while the provider went on to build the machine. The
// machine that exists is adopted — the record says running, the customer is
// told.
func (d *Deps) adoptTimedOutCreates(ctx context.Context, now time.Time) (int, error) {
	failed, err := d.Operations.FailedProvisionOperations(ctx, now.Add(-adoptWindow))
	if err != nil {
		return 0, fmt.Errorf("reconciler: read failed provisions: %w", err)
	}
	adopted := 0
	for i := range failed {
		op := &failed[i]
		instance, ok, err := d.Instances.BySubscription(ctx, op.ResourceID)
		if err != nil || !ok {
			continue
		}
		if instance.ObservedState != instancestore.ObservedProvisioning {
			continue
		}
		gateway, err := d.providerByID(ctx, *instance.ProviderID)
		if err != nil {
			d.log("resolve an adopting provision's provider", err)
			continue
		}
		report, err := gateway.GetInstance(ctx, provider.GetInstanceRequest{
			NodeID:             instance.NodeID.String(),
			ProviderInstanceID: *instance.ProviderInstanceID,
		})
		if err != nil || report.State != "running" {
			continue
		}
		if err := d.Instances.MarkObserved(ctx, instance.ID, instancestore.ObservedRunning, now); err != nil {
			d.log("adopt an instance", err)
			continue
		}
		if err := d.Instances.RecordNotification(ctx, instancestore.Notification{
			ID:         uuid.New(),
			UserID:     d.ownerOf(ctx, instance.SubscriptionID),
			Type:       "instance.adopted",
			TitleKey:   "notification.instance_adopted.title",
			MessageKey: "notification.instance_adopted.message",
			Parameters: adoptionParameters(instance.ID, report.ProviderInstanceID),
			Severity:   "success",
		}); err != nil {
			d.log("notify an adoption", err)
			continue
		}
		adopted++
	}
	return adopted, nil
}

// ownerOf resolves the subscription's owner for the adoption notification; an
// unreadable owner is not a reason to skip the adoption.
func (d *Deps) ownerOf(ctx context.Context, subscriptionID uuid.UUID) uuid.UUID {
	owner, err := d.Operations.Queries().SubscriptionOwner(ctx, subscriptionID)
	if err != nil {
		return uuid.Nil
	}
	return owner
}

// staleAgents counts the node credentials whose heartbeat aged out. Offline
// stays a derived fact; the operator's dashboard reads this count.
func (d *Deps) staleAgents(ctx context.Context, now time.Time) (int, error) {
	if d.Runman == nil {
		return 0, nil
	}
	agents, err := d.Runman.StaleAgents(ctx, now.Add(-staleAgentAfter))
	if err != nil {
		return 0, fmt.Errorf("reconciler: read stale agents: %w", err)
	}
	return len(agents), nil
}

// providerByID resolves the gateway behind a provider row, the same
// row-backed resolution the workflows use (ADR-007).
func (d *Deps) providerByID(ctx context.Context, providerID uuid.UUID) (provider.Provider, error) {
	rows, err := d.Nodes.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconciler: read the provider rows: %w", err)
	}
	for i := range rows {
		if rows[i].ID != providerID {
			continue
		}
		if gateway, ok := d.Providers[rows[i].Name]; ok {
			return gateway, nil
		}
		return nil, fmt.Errorf("reconciler: no gateway registered for provider %q", rows[i].Name)
	}
	return nil, fmt.Errorf("reconciler: no provider row %s", providerID)
}

func (d *Deps) log(message string, err error, level ...slog.Level) {
	lvl := slog.LevelError
	if len(level) > 0 {
		lvl = level[0]
	}
	d.Logger.LogAttrs(context.Background(), lvl, message, slog.String("error", err.Error()))
}

func stringOr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func adoptionParameters(instanceID uuid.UUID, providerInstanceID string) []byte {
	encoded, _ := json.Marshal(map[string]any{
		"instance_id":          instanceID.String(),
		"provider_instance_id": providerInstanceID,
	})
	return encoded
}
