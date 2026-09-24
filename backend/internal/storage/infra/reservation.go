package infrastore

// The reservation rows: an operation's durable promise of capacity (ADR-008 §5).
//
// The node's counters are the book; the row is the receipt that names the
// operation that made it and the moment it stops being good. Both move in one
// transaction, so no code path can leave a promise on the node that no receipt
// explains, or a receipt for capacity the node never promised.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	sqlcgen "github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/infra"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/operation"
)

// portCountCeiling is the largest port count a single reservation may ask for;
// a node's NAT table is orders of magnitude smaller.
const portCountCeiling = 1_000_000

// ReserveForOperation promises the capacity and records the receipt in one
// transaction. It is idempotent per operation: a workflow that retried after a
// timeout keeps the promise it already holds, and the counters are not charged
// twice.
func (s *Store) ReserveForOperation(ctx context.Context, operationID, nodeID uuid.UUID,
	spec operation.ResourceSpec, at time.Time) error {
	// The idempotency read is outside the transaction on purpose: it answers
	// the common retry case without taking write locks. The unique partial
	// index still arbitrates a genuinely concurrent pair.
	var existing uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT id FROM resource_reservations WHERE operation_id = $1 AND status = 'reserved'`,
		operationID).Scan(&existing)
	if err == nil {
		return nil // the promise is already on record
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("infrastore: read the reservation: %w", err)
	}

	ispec := infra.Spec{CPUCores: spec.CPUCores, MemoryMB: spec.MemoryMB, DiskGB: spec.DiskGB}
	return s.withinTransaction(ctx, func(bound *Store) error {
		ok, err := bound.Reserve(ctx, nodeID, ispec, at)
		if err != nil {
			return err
		}
		if !ok {
			return operation.ErrReservationLost
		}
		if err := bound.queries.CreateResourceReservation(ctx, sqlcgen.CreateResourceReservationParams{
			ID:          uuid.New(),
			NodeID:      nodeID,
			OperationID: operationID,
			CpuCores:    numericOf(spec.CPUCores),
			MemoryMb:    spec.MemoryMB,
			DiskGb:      spec.DiskGB,
			// Port counts are bounded by the node's NAT table, far below int32;
			// the narrowing states that rather than trusting it.
			Ipv4Count:    int32(min(spec.IPv4Count, portCountCeiling)), //nolint:gosec // clamped to the ceiling immediately above.
			Ipv6Count:    int32(min(spec.IPv6Count, portCountCeiling)), //nolint:gosec // clamped to the ceiling immediately above.
			NatPortCount: int32(min(spec.NATPorts, portCountCeiling)),  //nolint:gosec // clamped to the ceiling immediately above.
			ExpiresAt:    pgtype.Timestamptz{Time: at.Add(operation.ReservationExpiry), Valid: true},
		}); err != nil {
			return fmt.Errorf("infrastore: record the reservation: %w", err)
		}
		return nil
	})
}

// CommitReservation turns a kept promise into allocation: the receipt leaves
// `reserved` under a guard, and only the winner moves the counters. A receipt
// that is no longer open — expired by the sweep, or released — is reported
// rather than silently absorbed, because the operation's next act depends on
// capacity it believes it holds.
func (s *Store) CommitReservation(ctx context.Context, operationID, nodeID uuid.UUID,
	spec operation.ResourceSpec, at time.Time) error {
	ispec := infra.Spec{CPUCores: spec.CPUCores, MemoryMB: spec.MemoryMB, DiskGB: spec.DiskGB}
	return s.withinTransaction(ctx, func(bound *Store) error {
		tag, err := bound.queries.MarkReservationCommitted(ctx, sqlcgen.MarkReservationCommittedParams{
			OperationID: operationID,
			NodeID:      nodeID,
			UpdatedAt:   pgtype.Timestamptz{Time: at, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("infrastore: commit the receipt: %w", err)
		}
		if tag != 1 {
			return operation.ErrReservationLost
		}
		if ok, err := bound.Commit(ctx, nodeID, ispec, at); err != nil {
			return err
		} else if !ok {
			// The receipt said reserved; the node disagrees. A schema-level
			// refusal the counters cannot produce on their own is the sign of a
			// bookkeeping hole, and the caller must see it.
			return fmt.Errorf("infrastore: the node would not accept the commit")
		}
		return nil
	})
}

// ReleaseReservation withdraws a promise. Releasing an already-settled receipt
// is idempotent for released, and an error for expired — the sweep already
// gave the capacity back, and the caller's view is behind the truth.
func (s *Store) ReleaseReservation(ctx context.Context, operationID, nodeID uuid.UUID,
	spec operation.ResourceSpec, at time.Time) error {
	ispec := infra.Spec{CPUCores: spec.CPUCores, MemoryMB: spec.MemoryMB, DiskGB: spec.DiskGB}
	return s.withinTransaction(ctx, func(bound *Store) error {
		tag, err := bound.queries.MarkReservationReleased(ctx, sqlcgen.MarkReservationReleasedParams{
			OperationID: operationID,
			NodeID:      nodeID,
			UpdatedAt:   pgtype.Timestamptz{Time: at, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("infrastore: release the receipt: %w", err)
		}
		if tag != 1 {
			// Already released (idempotent) or expired (the sweep won). Distinguish
			// by reading the receipt's state.
			var status string
			readErr := bound.db.QueryRow(ctx,
				`SELECT status FROM resource_reservations WHERE operation_id = $1 AND node_id = $2
				 ORDER BY updated_at DESC LIMIT 1`, operationID, nodeID).Scan(&status)
			if readErr == nil && status == operation.ReservationReleased {
				return nil
			}
			return operation.ErrReservationLost
		}
		if _, err := bound.Release(ctx, nodeID, ispec, at); err != nil {
			return err
		}
		return nil
	})
}

// SweepExpiredReservations gives back what dead operations promised. Each
// expired receipt is claimed by exactly one sweep under SKIP LOCKED, and the
// counters are returned beside the claim. It returns the number of receipts
// it settled.
func (s *Store) SweepExpiredReservations(ctx context.Context, at time.Time) (int, error) {
	rows, err := s.queries.ExpiredReservations(ctx, pgtype.Timestamptz{Time: at, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("infrastore: sweep the expired reservations: %w", err)
	}
	for i := range rows {
		row := rows[i]
		if _, err := s.Release(ctx, row.NodeID, infra.Spec{
			CPUCores: numericFloat(row.CpuCores),
			MemoryMB: row.MemoryMb,
			DiskGB:   row.DiskGb,
		}, at); err != nil {
			return i, err
		}
	}
	return len(rows), nil
}

// withinTransaction runs fn bound to one transaction, the commerce store's
// shape, so the counters and the receipt commit or roll back together.
func (s *Store) withinTransaction(ctx context.Context, fn func(bound *Store) error) error {
	if s.tx != nil {
		return fn(s)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("infrastore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(newBound(s.pool, tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// numericFloat reads a numeric column back as the float the book keeps it as.
func numericFloat(value pgtype.Numeric) float64 {
	if !value.Valid {
		return 0
	}
	raw, err := value.Value()
	if err != nil {
		return 0
	}
	text, ok := raw.(string)
	if !ok {
		return 0
	}
	parsed, err := parseDecimal(text)
	if err != nil {
		return 0
	}
	return parsed
}

// DB exposes the underlying queryable for direct reads in tests and admin
// tooling; the domain methods remain the only writers.
func (s *Store) DB() interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
} {
	return s.db
}
