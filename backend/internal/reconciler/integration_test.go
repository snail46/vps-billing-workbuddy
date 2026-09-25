package reconciler_test

// The reconciler against real PostgreSQL: each check of ADR-014 proven as a
// transformation the pass performs, with rows seeded to the state the check
// exists to notice.

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/operation"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/reconciler"
	runmanstore "github.com/snail46/vps-billing-workbuddy/backend/internal/runman"
	infrastore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/infra"
	instancestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/instance"
	operationstore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/operation"
)

// fakeProvider answers as a well-behaved provider whose machine runs.
type fakeProvider struct{}

func (fakeProvider) Name() string { return "mock" }

func (fakeProvider) Health(context.Context) (*provider.Health, error) {
	return &provider.Health{Status: "up"}, nil
}

func (fakeProvider) Capabilities(context.Context) (*provider.Capabilities, error) {
	return &provider.Capabilities{CreateInstance: true, DeleteInstance: true}, nil
}

func (fakeProvider) ListImages(context.Context, string) ([]provider.Image, error) {
	return []provider.Image{{ID: "debian-12", OS: "debian"}}, nil
}

func (p fakeProvider) CreateInstance(context.Context, provider.CreateInstanceRequest) (*provider.Operation, error) {
	return &provider.Operation{ProviderOperationID: "ri-fake", Status: "succeeded", Accepted: true,
		Metadata: map[string]any{"instance_id": "ri-fake"}}, nil
}

func (fakeProvider) GetInstance(_ context.Context, req provider.GetInstanceRequest) (*provider.Instance, error) {
	if req.ProviderInstanceID == "" {
		return nil, &provider.Error{Code: "INSTANCE_NOT_FOUND"}
	}
	return &provider.Instance{ProviderInstanceID: req.ProviderInstanceID, State: "running"}, nil
}

func (fakeProvider) StartInstance(context.Context, provider.InstanceActionRequest) (*provider.Operation, error) {
	return &provider.Operation{Accepted: true}, nil
}

func (fakeProvider) StopInstance(context.Context, provider.InstanceActionRequest) (*provider.Operation, error) {
	return &provider.Operation{Accepted: true}, nil
}

func (fakeProvider) RestartInstance(context.Context, provider.InstanceActionRequest) (*provider.Operation, error) {
	return &provider.Operation{Accepted: true}, nil
}

func (fakeProvider) ReinstallInstance(context.Context, provider.ReinstallInstanceRequest) (*provider.Operation, error) {
	return &provider.Operation{Accepted: true}, nil
}

func (fakeProvider) ResetPassword(context.Context, provider.ResetPasswordRequest) (*provider.Operation, error) {
	return nil, &provider.Error{Code: "UNSUPPORTED_OPERATION"}
}

func (fakeProvider) DeleteInstance(context.Context, provider.InstanceActionRequest) (*provider.Operation, error) {
	return &provider.Operation{Accepted: true}, nil
}

func (fakeProvider) GetUsage(context.Context, provider.GetInstanceRequest) (*provider.Usage, error) {
	return &provider.Usage{MemoryTotalMB: 1024}, nil
}

func (fakeProvider) GetTraffic(context.Context, provider.GetTrafficRequest) (*provider.Traffic, error) {
	return &provider.Traffic{RXBytes: 1, TXBytes: 2}, nil
}

func (fakeProvider) ListPortForwards(context.Context, provider.GetInstanceRequest) ([]provider.PortForward, error) {
	return nil, nil
}

func (fakeProvider) AddPortForward(context.Context, provider.AddPortForwardRequest) (*provider.Operation, error) {
	return &provider.Operation{Accepted: true, Metadata: map[string]any{"mapping_id": "m1"}}, nil
}

func (fakeProvider) DeletePortForward(context.Context, provider.DeletePortForwardRequest) (*provider.Operation, error) {
	return &provider.Operation{Accepted: true}, nil
}

func newEnv(t *testing.T) (*reconciler.Deps, *pgxpool.Pool) {
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
	return &reconciler.Deps{
		Operations: operationstore.New(pool),
		Instances:  instancestore.New(pool),
		Nodes:      infrastore.New(pool),
		Providers:  map[string]provider.Provider{fakeProvider{}.Name(): fakeProvider{}},
		Runman:     runmanstore.New(pool),
		Logger:     slog.Default(),
	}, pool
}

// seedPlacement inserts the minimal chain a provisioned instance needs: a
// user, a catalogue, a node and provider, a subscription, an instance.
func seedPlacement(t *testing.T, pool *pgxpool.Pool, observed string) (instanceID, subscriptionID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	providerID := uuid.New()
	nodeID := uuid.New()
	userID := uuid.New()
	productID := uuid.New()
	planID := uuid.New()
	subscriptionID = uuid.New()
	instanceID = uuid.New()

	for _, stmt := range []string{
		`INSERT INTO users (id, email, password_hash, status) VALUES ('` + userID.String() + `', '` + id.String() + `@reconciler.test', 'x', 'active')`,
		`INSERT INTO node_groups (id, name, region, status) VALUES ('` + id.String() + `', 'rg-` + id.String() + `', 'test', 'active')`,
		`INSERT INTO providers (id, name, provider_type, status) VALUES ('` + providerID.String() + `', 'mock', 'direct', 'active')`,
		`INSERT INTO nodes (id, node_group_id, provider_id, name, region, status, cpu_total, memory_total_mb, disk_total_gb)
		 VALUES ('` + nodeID.String() + `', '` + id.String() + `', '` + providerID.String() + `', 'node-` + id.String() + `', 'test', 'online', 4, 4096, 100)`,
		`INSERT INTO products (id, slug, name_i18n, description_i18n, status) VALUES ('` + productID.String() + `', 'rg-` + id.String() + `', '{}', '{}', 'active')`,
		`INSERT INTO plans (id, product_id, node_group_id, slug, name_i18n, status, memory_mb, disk_gb, billing_cycle, price_minor, currency, cpu_cores, virtualization)
		 VALUES ('` + planID.String() + `', '` + productID.String() + `', '` + id.String() + `', 'pl-` + id.String() + `', '{}', 'active', 1024, 20, 'monthly', 1000, 'CNY', 1, 'kvm')`,
		`INSERT INTO subscriptions (id, user_id, plan_id, status, billing_cycle, price_minor, currency)
		 VALUES ('` + subscriptionID.String() + `', '` + userID.String() + `', '` + planID.String() + `', 'active', 'monthly', 1000, 'CNY')`,
		`INSERT INTO instances (id, subscription_id, node_id, provider_id, provider_instance_id, name, desired_state, observed_state, cpu_cores, memory_mb, disk_gb)
		 VALUES ('` + instanceID.String() + `', '` + subscriptionID.String() + `', '` + nodeID.String() + `', '` + providerID.String() + `', 'ri-` + id.String() + `', 'i-` + id.String() + `', 'running', '` + observed + `', 1, 1024, 20)`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("seed placement: %v", err)
		}
	}
	return instanceID, subscriptionID
}

func TestStaleOperationsAreCancelled(t *testing.T) {
	deps, pool := newEnv(t)
	ctx := context.Background()

	// A workflow whose worker died: running, and silent for far longer than
	// the staleness threshold.
	opID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO operations (id, type, resource_type, resource_id, status, idempotency_key, trace_id, created_at, updated_at)
		VALUES ($1, 'probe.stale', 'probe', $2, 'running', 'stale-' || $3::text, 'trace', now() - interval '1 hour', now() - interval '1 hour')`,
		opID, uuid.New(), uuid.New()); err != nil {
		t.Fatalf("seed stale operation: %v", err)
	}

	result, err := deps.Run(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.CancelledStaleOperations == 0 {
		t.Fatal("the reconciler cancelled nothing, though the record had been silent for an hour")
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM operations WHERE id = $1`, opID).Scan(&status); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "cancelled" {
		t.Fatalf("expected cancelled, got %s", status)
	}
}

func TestDriftIsCorrectedByObservation(t *testing.T) {
	deps, pool := newEnv(t)
	ctx := context.Background()

	instanceID, _ := seedPlacement(t, pool, "stopped") // the wish says running, the record says stopped

	if _, err := deps.Run(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("run: %v", err)
	}
	var observed string
	if err := pool.QueryRow(ctx, `SELECT observed_state FROM instances WHERE id = $1`, instanceID).Scan(&observed); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if observed != "running" {
		t.Fatalf("the provider said running, the record says %s", observed)
	}
}

func TestATimedOutCreateIsAdopted(t *testing.T) {
	deps, pool := newEnv(t)
	ctx := context.Background()

	instanceID, subscriptionID := seedPlacement(t, pool, "provisioning")

	// The engine failed the chain on timeout, but the provider went on to
	// build the machine — the fake provider answers running for it.
	if _, err := pool.Exec(ctx, `
		INSERT INTO operations (id, type, resource_type, resource_id, status, error_code, idempotency_key, trace_id, created_at, finished_at)
		VALUES ($1, 'provision.instance', 'subscription', $2, 'failed', 'PROVIDER_TIMEOUT', $3, 'trace', now(), now())`,
		uuid.New(), subscriptionID, "adopt-"+uuid.NewString()); err != nil {
		t.Fatalf("seed failed provision: %v", err)
	}

	result, err := deps.Run(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.AdoptedTimedOutCreates == 0 {
		t.Fatal("the reconciler adopted nothing, though the provider reported the machine running")
	}
	var observed string
	if err := pool.QueryRow(ctx, `SELECT observed_state FROM instances WHERE id = $1`, instanceID).Scan(&observed); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if observed != "running" {
		t.Fatalf("the adopted instance's record says %s", observed)
	}
	var notifications int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = (SELECT user_id FROM subscriptions WHERE id = $1)`, subscriptionID).Scan(&notifications); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if notifications == 0 {
		t.Fatal("the adoption left no notification for the customer")
	}
}

func TestStaleAgentsAreCounted(t *testing.T) {
	deps, pool := newEnv(t)
	ctx := context.Background()

	nodeID := "stale-node-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		INSERT INTO runman_agents (id, node_id, token_hash, status, last_seen_at)
		VALUES ($1, $2::varchar, 'hash-' || $2::varchar, 'active', now() - interval '2 hours')`,
		uuid.New(), nodeID); err != nil {
		t.Fatalf("seed stale agent: %v", err)
	}

	result, err := deps.Run(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.StaleAgents == 0 {
		t.Fatal("no stale agent was counted, though one had been silent for two hours")
	}
}

// The engine's own state machine is what the reconciler's tests ride; the
// import keeps the engine's package contract visible beside the assertions.
var _ = operation.StatusQueued
