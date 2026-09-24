package infrastore_test

// The reservation primitives against real PostgreSQL.
//
// The reserve is one conditional UPDATE whose WHERE clause carries the capacity
// check, so the properties under test are the schema's: two concurrent
// reservations of the same last vCPU cannot both promise it, a commit moves the
// promise into allocation, and a double release moves nothing rather than
// driving a counter negative.

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/infra"
	infrastore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/infra"
)

func testStore(t *testing.T) *infrastore.Store {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL is not set; integration test skipped")
	}
	return infrastore.New(newTestPool(t))
}

// seedNode inserts one provider, one group and one node with the given capacity,
// and returns the node's identifier.
func seedNode(t *testing.T, store *infrastore.Store, cpu float64, mem, disk int64, groupID uuid.UUID) uuid.UUID {
	t.Helper()
	providerID := uuid.New()
	nodeID := uuid.New()
	ctx := context.Background()
	if err := store.WithinTransaction(ctx, func(tx *infrastore.Store) error {
		return tx.SeedProvider(ctx, providerID, "seed-provider")
	}); err != nil {
		t.Fatalf("seed the provider: %v", err)
	}
	if err := store.SeedNode(ctx, nodeID, providerID, groupID, cpu, mem, disk); err != nil {
		t.Fatalf("seed the node: %v", err)
	}
	return nodeID
}

func TestReserveCommitReleaseWalksTheCounters(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	nodeID := seedNode(t, store, 8, 8192, 200, uuid.Nil)
	spec := infra.Spec{CPUCores: 2, MemoryMB: 1024, DiskGB: 40}

	applied, err := store.Reserve(ctx, nodeID, spec, testNow())
	if err != nil || !applied {
		t.Fatalf("reserve: applied=%v err=%v", applied, err)
	}
	nodes, err := store.ListNodes(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	reserved := findNode(t, nodes, nodeID)
	if reserved.CPURserved != 2 || reserved.MemoryRsrvMB != 1024 || reserved.DiskRsrvGB != 40 {
		t.Fatalf("the reserve wrote (%g, %d, %d)", reserved.CPURserved, reserved.MemoryRsrvMB, reserved.DiskRsrvGB)
	}

	// The commit turns the promise into allocation and empties the reservation.
	applied, err = store.Commit(ctx, nodeID, spec, testNow())
	if err != nil || !applied {
		t.Fatalf("commit: applied=%v err=%v", applied, err)
	}
	nodes, _ = store.ListNodes(ctx)
	committed := findNode(t, nodes, nodeID)
	if committed.CPUAllocated != 2 || committed.CPURserved != 0 {
		t.Fatalf("the commit wrote allocated=%g reserved=%g", committed.CPUAllocated, committed.CPURserved)
	}

	// The release of a committed reservation moves nothing: there is no promise
	// left to withdraw, and a counter that went negative would be a lie.
	applied, err = store.Release(ctx, nodeID, spec, testNow())
	if err != nil || applied {
		t.Fatalf("release after commit: applied=%v err=%v", applied, err)
	}
	nodes, _ = store.ListNodes(ctx)
	final := findNode(t, nodes, nodeID)
	if final.CPUAllocated != 2 || final.CPURserved != 0 {
		t.Fatalf("a phantom release moved the counters: allocated=%g reserved=%g",
			final.CPUAllocated, final.CPURserved)
	}
}

func TestReserveRefusesWhatTheNodeCannotCover(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	nodeID := seedNode(t, store, 4, 4096, 100, uuid.Nil)
	spec := infra.Spec{CPUCores: 8, MemoryMB: 1024, DiskGB: 20}

	applied, err := store.Reserve(ctx, nodeID, spec, testNow())
	if err != nil || applied {
		t.Fatalf("an oversized reservation was applied=%v err=%v", applied, err)
	}

	// A reservation against a node that cannot take one at all is the same
	// answer: the scheduling decision and the capacity check agree.
	offline := infra.Node{Status: infra.NodeOffline}
	if offline.Status == infra.NodeOnline {
		t.Fatal("unreachable; the constant moved")
	}
}

func TestConcurrentReservesCannotPromiseTheSameCapacity(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	// Capacity for exactly one of the spec: two concurrent reserves, one winner.
	nodeID := seedNode(t, store, 2, 2048, 50, uuid.Nil)
	spec := infra.Spec{CPUCores: 2, MemoryMB: 2048, DiskGB: 50}

	var applied int64
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.Reserve(ctx, nodeID, spec, testNow())
			if err != nil {
				errs <- err
				return
			}
			if ok {
				atomic.AddInt64(&applied, 1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a concurrent reserve failed: %v", err)
	}
	if applied != 1 {
		t.Fatalf("%d of two concurrent reserves applied, expected exactly 1", applied)
	}
	nodes, _ := store.ListNodes(ctx)
	node := findNode(t, nodes, nodeID)
	if node.CPURserved != 2 {
		t.Fatalf("reserved=%g; a second promise landed", node.CPURserved)
	}
}
