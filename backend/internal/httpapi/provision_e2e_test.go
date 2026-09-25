package httpapi_test

// The provision vertical slice's Gate (ADR-009): browse, order, pay through
// the fake gateway, and the platform provisions on its own — with the same
// hundred-callback property Phase 2 proved for money, proved one level higher
// for the workflow the money starts.

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/commerce"
	sqlcgen "github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/operation"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/outbox"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider/lxdapi"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider/lxdapi/lxdtest"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provision"
	commercestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/commerce"
	infrastore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/infra"
	instancestore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/instance"
	operationstore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/operation"
)

// driveProvision runs the worker's halves in-process until the subscription's
// instance is running or the budget is spent. It returns the last observed
// state so the caller asserts on the record.
func driveProvision(t *testing.T, e *e2eEnv, subscriptionID string) string {
	t.Helper()
	ctx := context.Background()
	parsed, err := uuid.Parse(subscriptionID)
	if err != nil {
		t.Fatalf("the subscription id is not a uuid: %v", err)
	}
	for i := 0; i < 64; i++ {
		if _, err := e.publisher.Deliver(ctx); err != nil {
			t.Fatalf("the outbox tick failed: %v", err)
		}
		if _, err := e.engine.Tick(ctx); err != nil {
			t.Fatalf("the engine tick failed: %v", err)
		}
		instance, exists, err := e.instances.BySubscription(ctx, parsed)
		if err != nil {
			t.Fatalf("read the instance: %v", err)
		}
		if exists && instance.ObservedState == instancestore.ObservedRunning {
			return instance.ObservedState
		}
	}
	instance, exists, err := e.instances.BySubscription(ctx, parsed)
	if err != nil {
		t.Fatalf("read the instance: %v", err)
	}
	if !exists {
		t.Fatal("the budget ran out before an instance existed")
	}
	return instance.ObservedState
}

// subscriptionIDForUser reads the customer's only subscription — each test
// signs up a fresh user, so ownership is the lookup.
func subscriptionIDForUser(t *testing.T, e *e2eEnv, userID uuid.UUID) string {
	t.Helper()
	var id string
	if err := e.pool.QueryRow(context.Background(),
		`SELECT id FROM subscriptions WHERE user_id = $1 LIMIT 1`, userID).Scan(&id); err != nil {
		t.Fatalf("read the subscription: %v", err)
	}
	return id
}

// seedProviderAndNode inserts a provider row named for the gateway under test
// and one node with more capacity than the seeded plan needs.
func seedProviderAndNode(t *testing.T, e *e2eEnv, providerName string) {
	t.Helper()
	ctx := context.Background()
	providerID := uuid.New()
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO providers (id, name, provider_type, status)
		VALUES ($1, $2, 'direct', 'active')`, providerID, providerName); err != nil {
		t.Fatalf("insert the provider: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, "DELETE FROM providers WHERE id = $1", providerID)
	})
	if err := e.infra.SeedNode(ctx, uuid.New(), providerID, uuid.Nil, 8, 16384, 400); err != nil {
		t.Fatalf("seed the node: %v", err)
	}
}

func TestThePaidOrderProvisionsOnItsOwn(t *testing.T) {
	e := newE2E(t)

	userID, cookie, token := signUp(t, e)
	entry := seedCatalog(t, e)
	seedProviderAndNode(t, e, "mock")

	// Browse, order, pay: the journey up to the money is Phase 2's, re-walked
	// here because the Gate is the whole chain, not its parts.
	order := placeOrderAndPayment(t, e, entry, cookie, token)
	if rec := e.deliverWebhook(t, string(order.callback), order.signature); rec.Code != http.StatusOK {
		t.Fatalf("the callback was refused: %d (%s)", rec.Code, rec.Body.String())
	}

	subscriptionID := subscriptionIDForUser(t, e, userID)
	if got := driveProvision(t, e, subscriptionID); got != instancestore.ObservedRunning {
		t.Fatalf("the provision ended with the instance %q", got)
	}

	// The customer sees the machine through their own surface — the Gate's
	// last step.
	list := e.do(t, http.MethodGet, "/api/v1/instances", "", cookie, "")
	if list.Code != http.StatusOK {
		t.Fatalf("list the instances: %d (%s)", list.Code, list.Body.String())
	}
	if got := count(t, e, `SELECT count(*) FROM instances i
		JOIN subscriptions s ON s.id = i.subscription_id
		WHERE s.user_id = $1 AND i.observed_state = 'running'`, userID); got != 1 {
		t.Errorf("the customer's list holds %d running instances, expected 1", got)
	}

	// The record: one of everything, each in its right state.
	if got := count(t, e, `SELECT count(*) FROM operations
		WHERE resource_type = 'subscription' AND resource_id = $1
		  AND type = 'provision.instance' AND status = 'succeeded'`, subscriptionID); got != 1 {
		t.Errorf("%d provision operations succeeded, expected 1", got)
	}
	if got := count(t, e, `SELECT count(*) FROM notifications WHERE user_id = $1
		AND type = 'instance.provisioned'`, userID); got != 1 {
		t.Errorf("%d notifications were recorded, expected 1", got)
	}
	// The count is scoped to THIS subscription's instance: the database is
	// shared with the rest of the suite, and their events are not ours.
	if got := count(t, e, `SELECT count(*) FROM outbox_events
		WHERE event_type = 'instance.provisioned.v1' AND aggregate_type = 'instance'
		  AND aggregate_id = (SELECT id FROM instances WHERE subscription_id = $1)`,
		subscriptionID); got != 1 {
		t.Errorf("%d provisioned events were written, expected 1", got)
	}
}

func TestAHundredCallbacksStillProvisionOnce(t *testing.T) {
	e := newE2E(t)
	ctx := context.Background()

	userID, cookie, token := signUp(t, e)
	entry := seedCatalog(t, e)
	seedProviderAndNode(t, e, "mock")

	order := placeOrderAndPayment(t, e, entry, cookie, token)
	if rec := e.deliverWebhook(t, string(order.callback), order.signature); rec.Code != http.StatusOK {
		t.Fatalf("the callback was refused: %d (%s)", rec.Code, rec.Body.String())
	}
	subscriptionID := subscriptionIDForUser(t, e, userID)
	if got := driveProvision(t, e, subscriptionID); got != instancestore.ObservedRunning {
		t.Fatalf("the provision ended with the instance %q", got)
	}

	// The same callback, ninety-nine more times, sequentially and concurrently.
	// Phase 2 proved the money absorbs this; the provision must absorb it too.
	for i := 0; i < 49; i++ {
		if rec := e.deliverWebhook(t, string(order.callback), order.signature); rec.Code != http.StatusOK {
			t.Fatalf("a replayed callback was refused: %d", rec.Code)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := e.deliverWebhook(t, string(order.callback), order.signature); rec.Code != http.StatusOK {
				errs <- fmt.Errorf("a concurrent callback was refused: %d", rec.Code)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	// The worker keeps draining: any event the replays minted gets delivered,
	// any operation they could spawn gets claimed.
	for i := 0; i < 32; i++ {
		_, _ = e.publisher.Deliver(ctx)
		_, _ = e.engine.Tick(ctx)
	}

	for name, got := range map[string]int{
		"provision operations": count(t, e, `SELECT count(*) FROM operations
			WHERE resource_type = 'subscription' AND resource_id = $1
			  AND type = 'provision.instance'`, subscriptionID),
		"instances": count(t, e, `SELECT count(*) FROM instances WHERE subscription_id = $1`, subscriptionID),
	} {
		if got != 1 {
			t.Errorf("%d %s exist, expected exactly 1", got, name)
		}
	}
}

func TestAProvisionWithoutCapacityIsRetryable(t *testing.T) {
	e := newE2E(t)
	ctx := context.Background()

	userID, cookie, token := signUp(t, e)
	entry := seedCatalog(t, e)
	seedProviderAndNode(t, e, "mock")
	// The scheduler has nothing that can hold this plan: the demand side is
	// what the test owns, so the plan asks for more cores than any node can
	// carry — a premise no concurrent package's seed can invalidate. Taking
	// the world's nodes offline was the first draft, and it broke the moment
	// another package seeded a node of its own.
	if _, err := e.pool.Exec(ctx,
		"UPDATE plans SET cpu_cores = '9999' WHERE id = $1", entry.planID); err != nil {
		t.Fatalf("make the plan unprovisionable: %v", err)
	}

	order := placeOrderAndPayment(t, e, entry, cookie, token)
	if rec := e.deliverWebhook(t, string(order.callback), order.signature); rec.Code != http.StatusOK {
		t.Fatalf("the callback was refused: %d (%s)", rec.Code, rec.Body.String())
	}
	subscriptionID := subscriptionIDForUser(t, e, userID)

	// The bridge delivers, the engine claims, the chain fails at selection
	// with a retryable code — and the operation waits, not dies.
	if _, err := e.publisher.Deliver(ctx); err != nil {
		t.Fatalf("the outbox tick failed: %v", err)
	}
	if _, err := e.engine.Tick(ctx); err != nil {
		t.Fatalf("the engine tick failed: %v", err)
	}
	if got := count(t, e, `SELECT count(*) FROM operations
		WHERE resource_id = $1 AND type = 'provision.instance' AND status = 'retrying'`,
		subscriptionID); got != 1 {
		t.Errorf("%d operations are parked for retry, expected 1", got)
	}
}

// TestTheJourneyRunsOnTheDirectProvider is the phase's Gate: the same
// journey — browse, order, pay, provision — driven by the LXD adapter against
// a LXD-shaped server, through the runner and the bridge the mock used. The
// business core is untouched: the provider map is the only seam.
func TestTheJourneyRunsOnTheDirectProvider(t *testing.T) {
	e := newE2E(t)
	ctx := context.Background()

	// The direct provider and the LXD-shaped server behind it.
	fakeLXD, endpoint := lxdtest.NewServer(t)
	direct := lxdapi.New(lxdapi.Config{Endpoint: endpoint, Token: "test-token"})

	// A second engine, wired exactly as the worker wires it, except the
	// provider map names the LXD adapter. The runner, the bridge, the stores
	// and the operation machine are the same code the mock's journey ran.
	commerceStore := commercestore.New(e.pool)
	commerceSvc, err := commerce.NewService(commerce.Deps{Store: commerceStore})
	if err != nil {
		t.Fatalf("build the commerce service: %v", err)
	}
	engine := operation.NewEngine(operationstore.New(e.pool), infrastore.New(e.pool),
		testLogger(), operation.DefaultMaxRetries)
	if err := engine.Register(provision.OperationType, provision.NewRunner(provision.Deps{
		Subscriptions: commerceSvc,
		Nodes:         infrastore.New(e.pool),
		Instances:     instancestore.New(e.pool),
		Providers:     map[string]provider.Provider{direct.Name(): direct},
		Outbox:        commerceStore,
		Logger:        testLogger(),
	})); err != nil {
		t.Fatalf("register the provision runner: %v", err)
	}
	publisher := outbox.New(sqlcgen.New(e.pool), testLogger())
	if err := publisher.Register(commerce.EventSubscriptionActivated,
		provision.HandleSubscriptionActivated(provision.BridgeDeps{Engine: engine})); err != nil {
		t.Fatalf("register the provision bridge: %v", err)
	}

	// The row the instance record names matches the adapter; the plan's
	// virtualization is one the LXD adapter declares.
	userID, cookie, token := signUp(t, e)
	entry := seedCatalog(t, e)
	seedProviderAndNode(t, e, direct.Name())

	order := placeOrderAndPayment(t, e, entry, cookie, token)
	if rec := e.deliverWebhook(t, string(order.callback), order.signature); rec.Code != http.StatusOK {
		t.Fatalf("the callback was refused: %d (%s)", rec.Code, rec.Body.String())
	}
	subscriptionID := subscriptionIDForUser(t, e, userID)

	parsed, err := uuid.Parse(subscriptionID)
	if err != nil {
		t.Fatalf("the subscription id is not a uuid: %v", err)
	}
	for i := 0; i < 64; i++ {
		if _, err := publisher.Deliver(ctx); err != nil {
			t.Fatalf("the outbox tick failed: %v", err)
		}
		if _, err := engine.Tick(ctx); err != nil {
			t.Fatalf("the engine tick failed: %v", err)
		}
		instance, exists, err := instancestore.New(e.pool).BySubscription(ctx, parsed)
		if err != nil {
			t.Fatalf("read the instance: %v", err)
		}
		if exists && instance.ObservedState == instancestore.ObservedRunning {
			// The machine the LXD adapter made is the machine the customer
			// sees running.
			if instance.ProviderInstanceID == nil {
				t.Fatal("the running instance carries no provider identity")
			}
			if fakeLXD.InstanceCount() != 1 {
				t.Errorf("the LXD server holds %d instances, expected 1", fakeLXD.InstanceCount())
			}
			return
		}
	}
	t.Fatal("the journey ended without a running instance on the direct provider")
}
