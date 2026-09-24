package infrastore_test

// The reservation receipts (ADR-008 §5): a promise, its commit, its release,
// and the sweep that takes back what a dead operation left.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/infra"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/operation"
	infrastore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/infra"
	operationstore "github.com/snail46/vps-billing-workbuddy/backend/internal/storage/operation"
)

// newOperationForReservation enqueues one operation directly through the store
// — the FK target a receipt needs — and returns its identifier.
func newOperationForReservation(t *testing.T, operationStore *operationstore.Store) uuid.UUID {
	t.Helper()
	op, err := operationStore.Enqueue(context.Background(), operation.Operation{
		ID:             uuid.New(),
		Type:           "probe.reserve",
		ResourceType:   "instance",
		ResourceID:     uuid.New(),
		Status:         operation.StatusQueued,
		MaxRetries:     1,
		IdempotencyKey: "op-reserve-" + uuid.NewString(),
		TraceID:        uuid.NewString(),
	}, []operation.Step{{
		ID:        uuid.New(),
		StepKey:   "reserve",
		StepOrder: 1,
		Status:    operation.StepPending,
	}}, time.Now().UTC())
	if err != nil {
		t.Fatalf("enqueue the operation: %v", err)
	}
	return op.ID
}

func TestAReservationIsRecordedAndIdempotent(t *testing.T) {
	pool := newTestPool(t)
	store := newInfraStore(t, pool)
	opStore := operationstore.New(pool)
	ctx := context.Background()
	at := time.Now().UTC()

	nodeID := seedNode(t, store, 4, 4096, 100, uuid.Nil)
	operationID := newOperationForReservation(t, opStore)
	spec := operation.ResourceSpec{CPUCores: 2, MemoryMB: 2048, DiskGB: 50}

	if err := store.ReserveForOperation(ctx, operationID, nodeID, spec, at); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	// The retried reserve is the same promise, not a second charge.
	if err := store.ReserveForOperation(ctx, operationID, nodeID, spec, at); err != nil {
		t.Fatalf("reserve again: %v", err)
	}

	node := readNode(t, store, nodeID)
	if node.CPURserved != 2 || node.MemoryRsrvMB != 2048 || node.DiskRsrvGB != 50 {
		t.Errorf("the counters hold %+v after one promise and its retry", node)
	}
}

func TestACommitMovesTheCountersOnceAndClosesTheReceipt(t *testing.T) {
	pool := newTestPool(t)
	store := newInfraStore(t, pool)
	opStore := operationstore.New(pool)
	ctx := context.Background()
	at := time.Now().UTC()

	nodeID := seedNode(t, store, 4, 4096, 100, uuid.Nil)
	operationID := newOperationForReservation(t, opStore)
	spec := operation.ResourceSpec{CPUCores: 2, MemoryMB: 2048, DiskGB: 50}

	if err := store.ReserveForOperation(ctx, operationID, nodeID, spec, at); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := store.CommitReservation(ctx, operationID, nodeID, spec, at); err != nil {
		t.Fatalf("commit: %v", err)
	}

	node := readNode(t, store, nodeID)
	if node.CPURserved != 0 || node.CPUAllocated != 2 {
		t.Errorf("after the commit the node holds reserved=%v allocated=%v", node.CPURserved, node.CPUAllocated)
	}

	// A second commit is lost, not idempotent: the allocation has happened, and
	// a caller whose view is behind must be told.
	err := store.CommitReservation(ctx, operationID, nodeID, spec, at)
	if !errors.Is(err, operation.ErrReservationLost) {
		t.Errorf("a second commit produced %v", err)
	}
}

func TestAReleaseGivesTheCapacityBack(t *testing.T) {
	pool := newTestPool(t)
	store := newInfraStore(t, pool)
	opStore := operationstore.New(pool)
	ctx := context.Background()
	at := time.Now().UTC()

	nodeID := seedNode(t, store, 4, 4096, 100, uuid.Nil)
	operationID := newOperationForReservation(t, opStore)
	spec := operation.ResourceSpec{CPUCores: 2, MemoryMB: 2048, DiskGB: 50}

	if err := store.ReserveForOperation(ctx, operationID, nodeID, spec, at); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := store.ReleaseReservation(ctx, operationID, nodeID, spec, at); err != nil {
		t.Fatalf("release: %v", err)
	}
	// Releasing again is idempotent: the caller retried because it never heard
	// the answer.
	if err := store.ReleaseReservation(ctx, operationID, nodeID, spec, at); err != nil {
		t.Fatalf("release again: %v", err)
	}

	node := readNode(t, store, nodeID)
	if node.CPURserved != 0 || node.MemoryRsrvMB != 0 || node.DiskRsrvGB != 0 {
		t.Errorf("the released capacity is still held: %+v", node)
	}
}

func TestTheSweepReleasesWhatExpiryFinds(t *testing.T) {
	pool := newTestPool(t)
	store := newInfraStore(t, pool)
	opStore := operationstore.New(pool)
	ctx := context.Background()
	at := time.Now().UTC()

	nodeID := seedNode(t, store, 4, 4096, 100, uuid.Nil)
	operationID := newOperationForReservation(t, opStore)
	spec := operation.ResourceSpec{CPUCores: 2, MemoryMB: 2048, DiskGB: 50}

	if err := store.ReserveForOperation(ctx, operationID, nodeID, spec, at); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	// Nothing to release before the expiry — the tally is global on a shared
	// database, so the assertions below are about THIS receipt, never the count.
	if _, err := store.SweepExpiredReservations(ctx, at.Add(time.Minute)); err != nil {
		t.Fatalf("sweep before expiry: %v", err)
	}
	if status := reservationStatus(t, store, operationID); status != operation.ReservationReserved {
		t.Fatalf("a sweep inside the expiry window moved our receipt to %q", status)
	}

	// Past the expiry the promise is given back.
	if _, err := store.SweepExpiredReservations(ctx, at.Add(operation.ReservationExpiry+time.Minute)); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if status := reservationStatus(t, store, operationID); status != operation.ReservationExpired {
		t.Fatalf("the sweep left our receipt %q, expected expired", status)
	}

	node := readNode(t, store, nodeID)
	if node.CPURserved != 0 {
		t.Errorf("the expired promise still holds %v vCPU", node.CPURserved)
	}
}

// reservationStatus reads one receipt's state by its operation, the key the
// partial unique index keys it on.
func reservationStatus(t *testing.T, store *infrastore.Store, operationID uuid.UUID) string {
	t.Helper()
	var status string
	if err := store.DB().QueryRow(context.Background(),
		`SELECT status FROM resource_reservations WHERE operation_id = $1
		 ORDER BY updated_at DESC LIMIT 1`, operationID).Scan(&status); err != nil {
		t.Fatalf("read the receipt: %v", err)
	}
	return status
}

// readNode reads one node's book straight back.
func readNode(t *testing.T, store *infrastore.Store, nodeID uuid.UUID) infra.Node {
	t.Helper()
	nodes, err := store.ListNodes(context.Background())
	if err != nil {
		t.Fatalf("list the nodes: %v", err)
	}
	return findNode(t, nodes, nodeID)
}

// newInfraStore builds the store under test.
func newInfraStore(t *testing.T, pool *pgxpool.Pool) *infrastore.Store {
	t.Helper()
	return infrastore.New(pool)
}
