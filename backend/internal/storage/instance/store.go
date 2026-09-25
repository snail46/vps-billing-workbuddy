// Package instancestore is the persistence of the instance record and the
// notifications the platform leaves for its users (ADR-009). The provision
// runner owns the writes; the user surface owns the reads.
package instancestore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	sqlcgen "github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
)

// Instance is one provisioned machine, as the record holds it.
type Instance struct {
	ID                 uuid.UUID
	SubscriptionID     uuid.UUID
	NodeID             *uuid.UUID
	ProviderID         *uuid.UUID
	ProviderInstanceID *string
	Name               string
	DesiredState       string
	ObservedState      string
	CPUCores           float64
	MemoryMB           int64
	DiskGB             int64
	ImageID            *string
	LastSyncedAt       *time.Time
	Version            int64
	CreatedAt          time.Time
}

// The capacity ceilings a single instance may declare; the column is int32
// and no real machine is near it.
const (
	maxCapacityMb = 1 << 30
	maxCapacityGb = 1 << 30
)

// Instance states — docs/05's two machines.
const (
	DesiredRunning = "running"
	DesiredStopped = "stopped"

	ObservedPending      = "pending"
	ObservedProvisioning = "provisioning"
	ObservedRunning      = "running"
	ObservedError        = "error"
)

// Store reads and writes the instance record.
type Store struct {
	queries *sqlcgen.Queries
}

// New builds a store over the pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{queries: sqlcgen.New(pool)}
}

// Create inserts the instance row in the provisioning state.
func (s *Store) Create(ctx context.Context, inst Instance, at time.Time) error {
	return s.queries.CreateInstance(ctx, sqlcgen.CreateInstanceParams{
		ID:                 inst.ID,
		SubscriptionID:     inst.SubscriptionID,
		NodeID:             inst.NodeID,
		ProviderID:         inst.ProviderID,
		ProviderInstanceID: textOrNull(inst.ProviderInstanceID),
		Name:               inst.Name,
		DesiredState:       inst.DesiredState,
		ObservedState:      inst.ObservedState,
		CpuCores:           numericOf(inst.CPUCores),
		// The plan's capacity is bounded by the catalogue's own arithmetic long
		// before it reaches this conversion; the clamps state it.
		MemoryMb:     int32(min(inst.MemoryMB, int64(maxCapacityMb))), //nolint:gosec // clamped to the ceiling immediately above.
		DiskGb:       int32(min(inst.DiskGB, int64(maxCapacityGb))),   //nolint:gosec // clamped to the ceiling immediately above.
		ImageID:      textOrNull(inst.ImageID),
		LastSyncedAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
}

// BySubscription reads the instance a subscription owns. A missing instance is
// the normal state of a subscription that has not been provisioned yet.
func (s *Store) BySubscription(ctx context.Context, subscriptionID uuid.UUID) (Instance, bool, error) {
	row, err := s.queries.InstanceBySubscription(ctx, subscriptionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Instance{}, false, nil
	}
	if err != nil {
		return Instance{}, false, fmt.Errorf("instancestore: read by subscription: %w", err)
	}
	return fromRow(row), true, nil
}

// ByID reads one instance by its platform identifier. The action paths and the
// instance workflows read this way; ownership is resolved by the caller —
// either the SQL join of the user surface or the workflow's own record.
func (s *Store) ByID(ctx context.Context, id uuid.UUID) (Instance, bool, error) {
	row, err := s.queries.InstanceByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Instance{}, false, nil
	}
	if err != nil {
		return Instance{}, false, fmt.Errorf("instancestore: read by id: %w", err)
	}
	return fromRow(row), true, nil
}

// SetImage records the image a reinstall put on the machine.
func (s *Store) SetImage(ctx context.Context, id uuid.UUID, image string, at time.Time) error {
	tag, err := s.queries.SetInstanceImage(ctx, sqlcgen.SetInstanceImageParams{
		ID:        id,
		ImageID:   pgtype.Text{String: image, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("instancestore: set image: %w", err)
	}
	if tag != 1 {
		return fmt.Errorf("instancestore: the instance %s vanished mid-update", id)
	}
	return nil
}

// ForUser reads a customer's live instances, newest first.
func (s *Store) ForUser(ctx context.Context, userID uuid.UUID) ([]Instance, error) {
	rows, err := s.queries.InstancesForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("instancestore: read for user: %w", err)
	}
	instances := make([]Instance, 0, len(rows))
	for i := range rows {
		instances = append(instances, fromRow(rows[i]))
	}
	return instances, nil
}

// MarkProvisioned records the provider's acceptance and the running state in
// one update: the node, the provider identity and the observation land
// together, because a row that claims running without saying where is a row
// the reconciler cannot work with.
func (s *Store) MarkProvisioned(ctx context.Context, id, nodeID, providerID uuid.UUID,
	providerInstanceID string, at time.Time) error {
	tag, err := s.queries.SetInstanceProvisioned(ctx, sqlcgen.SetInstanceProvisionedParams{
		ID:                 id,
		NodeID:             &nodeID,
		ProviderID:         &providerID,
		ProviderInstanceID: pgtype.Text{String: providerInstanceID, Valid: true},
		LastSyncedAt:       pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("instancestore: mark provisioned: %w", err)
	}
	if tag != 1 {
		return fmt.Errorf("instancestore: the instance %s vanished mid-provision", id)
	}
	return nil
}

// MarkObserved records the provider's last word on the machine's state.
func (s *Store) MarkObserved(ctx context.Context, id uuid.UUID, observed string, at time.Time) error {
	tag, err := s.queries.MarkInstanceObserved(ctx, sqlcgen.MarkInstanceObservedParams{
		ID:            id,
		ObservedState: observed,
		LastSyncedAt:  pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("instancestore: mark observed: %w", err)
	}
	if tag != 1 {
		return fmt.Errorf("instancestore: the instance %s vanished mid-update", id)
	}
	return nil
}

// Notification is one record the platform leaves for a user.
type Notification struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Type       string
	TitleKey   string
	MessageKey string
	Parameters []byte
	Severity   string
}

// RecordNotification writes the notification row.
func (s *Store) RecordNotification(ctx context.Context, n Notification) error {
	return s.queries.CreateNotification(ctx, sqlcgen.CreateNotificationParams{
		ID:         n.ID,
		UserID:     &n.UserID,
		Type:       n.Type,
		TitleKey:   n.TitleKey,
		MessageKey: n.MessageKey,
		Parameters: n.Parameters,
		Severity:   n.Severity,
	})
}

func fromRow(row sqlcgen.Instance) Instance {
	return Instance{
		ID:                 row.ID,
		SubscriptionID:     row.SubscriptionID,
		NodeID:             row.NodeID,
		ProviderID:         row.ProviderID,
		ProviderInstanceID: textPtr(row.ProviderInstanceID),
		Name:               row.Name,
		DesiredState:       row.DesiredState,
		ObservedState:      row.ObservedState,
		CPUCores:           numericFloat(row.CpuCores),
		MemoryMB:           int64(row.MemoryMb),
		DiskGB:             int64(row.DiskGb),
		ImageID:            textPtr(row.ImageID),
		LastSyncedAt:       timePtr(row.LastSyncedAt),
		Version:            row.Version,
		CreatedAt:          row.CreatedAt.Time.UTC(),
	}
}

func textPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func textOrNull(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func numericOf(value float64) pgtype.Numeric {
	var numeric pgtype.Numeric
	if err := numeric.Scan(strconv.FormatFloat(value, 'f', -1, 64)); err != nil {
		return pgtype.Numeric{}
	}
	return numeric
}

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
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0
	}
	return parsed
}
