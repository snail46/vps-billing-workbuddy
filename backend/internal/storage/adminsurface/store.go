// Package adminsurface is the persistence behind the operator's console
// (ADR-012): lists, details and the dashboard's computed aggregates. Every
// method is a read the admin routes gate by permission; the few writes are
// conditional updates that name the state they move.
package adminsurface

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	sqlcgen "github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
)

// Store reads what the operator's console shows.
type Store struct {
	queries *sqlcgen.Queries
	pool    *pgxpool.Pool
}

// New builds a store over the pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{queries: sqlcgen.New(pool), pool: pool}
}

// Queries exposes the generated queries for handlers whose payloads map one
// row family directly. The admin surface is read-heavy and the payloads are
// the rows; a second struct layer per query would be typing without meaning.
func (s *Store) Queries() *sqlcgen.Queries { return s.queries }

// Pool exposes the pool for the handlers' ad-hoc joins. Deliberately narrow:
// the admin handlers use it only where the generated row does not carry a
// field the screen needs.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Overview is the dashboard's first screen, computed from current rows.
type Overview struct {
	FailedOperations  int64
	RunningOperations int64
	NodesOffline      int64
	ActiveUsers       int64
	SuspendedUsers    int64
	CapacityWarnings  int64
	RevenueTotalMinor int64
	PaymentsByStatus  []sqlcgen.AdminPaymentsByStatusRow
}

// Read computes the dashboard in one pass of small aggregate queries.
func (s *Store) Overview(ctx context.Context) (*Overview, error) {
	o := &Overview{}
	counters := []struct {
		name string
		read func() (int64, error)
		set  func(int64)
	}{
		{"failed operations", func() (int64, error) { return s.queries.AdminOpenFailedOperations(ctx) }, func(v int64) { o.FailedOperations = v }},
		{"running operations", func() (int64, error) { return s.queries.AdminRunningOperations(ctx) }, func(v int64) { o.RunningOperations = v }},
		{"nodes offline", func() (int64, error) { return s.queries.AdminNodesOffline(ctx) }, func(v int64) { o.NodesOffline = v }},
		{"active users", func() (int64, error) { return s.queries.AdminActiveUsers(ctx) }, func(v int64) { o.ActiveUsers = v }},
		{"suspended users", func() (int64, error) { return s.queries.AdminSuspendedUsers(ctx) }, func(v int64) { o.SuspendedUsers = v }},
		{"capacity warnings", func() (int64, error) { return s.queries.AdminCapacityWarnings(ctx) }, func(v int64) { o.CapacityWarnings = v }},
	}
	for _, counter := range counters {
		v, err := counter.read()
		if err != nil {
			return nil, fmt.Errorf("adminsurface: read %s: %w", counter.name, err)
		}
		counter.set(v)
	}
	revenue, err := s.queries.AdminRevenueTotal(ctx)
	if err != nil {
		return nil, fmt.Errorf("adminsurface: read revenue: %w", err)
	}
	o.RevenueTotalMinor = revenue
	byStatus, err := s.queries.AdminPaymentsByStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("adminsurface: read payments by status: %w", err)
	}
	o.PaymentsByStatus = byStatus
	return o, nil
}

// SetUserStatus moves a user between active and suspended. A false result
// means the row was already in the requested state — an idempotent no-op, not
// an error.
func (s *Store) SetUserStatus(ctx context.Context, id uuid.UUID, status string, at time.Time) (bool, error) {
	tag, err := s.queries.AdminSetUserStatus(ctx, sqlcgen.AdminSetUserStatusParams{
		ID:        id,
		Status:    status,
		UpdatedAt: pgTSTZ(at),
	})
	if err != nil {
		return false, fmt.Errorf("adminsurface: set user status: %w", err)
	}
	return tag == 1, nil
}

// StampTicketAnswered moves a conversation to answered when the support side
// replies. A false result means the ticket was already answered or closed.
func (s *Store) StampTicketAnswered(ctx context.Context, id uuid.UUID, at time.Time) error {
	if _, err := s.queries.AdminTicketReplyStamp(ctx, sqlcgen.AdminTicketReplyStampParams{
		ID:        id,
		UpdatedAt: pgTSTZ(at),
	}); err != nil {
		return fmt.Errorf("adminsurface: stamp ticket answered: %w", err)
	}
	return nil
}

// CloseTicket closes one conversation. A false result means it already was.
func (s *Store) CloseTicket(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	tag, err := s.queries.AdminTicketClose(ctx, sqlcgen.AdminTicketCloseParams{
		ID:       id,
		ClosedAt: pgTSTZ(at),
	})
	if err != nil {
		return false, fmt.Errorf("adminsurface: close ticket: %w", err)
	}
	return tag == 1, nil
}

// pgTSTZ wraps a timestamp for the generated params.
func pgTSTZ(at time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: at, Valid: true}
}

// WithinTransaction runs fn inside one transaction; the support reply writes
// its message and its status stamp as one fact.
func (s *Store) WithinTransaction(ctx context.Context, fn func(*Store) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("adminsurface: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(&Store{queries: sqlcgen.New(tx), pool: s.pool}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetAdminTwoFactor writes the secret and the flag in one update — the pair
// is one fact about the account (ADR-015 §1).
func (s *Store) SetAdminTwoFactor(ctx context.Context, adminID uuid.UUID, secret string, enabled bool, at time.Time) error {
	tag, err := s.queries.SetAdminTwoFactor(ctx, sqlcgen.SetAdminTwoFactorParams{
		ID:               adminID,
		TwoFactorSecret:  pgTextOrNull(secret),
		TwoFactorEnabled: enabled,
		UpdatedAt:        pgTSTZ(at),
	})
	if err != nil {
		return fmt.Errorf("adminsurface: set two factor: %w", err)
	}
	if tag != 1 {
		return fmt.Errorf("adminsurface: no admin %s", adminID)
	}
	return nil
}

func pgTextOrNull(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}
