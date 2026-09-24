package infrastore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SeedProvider and SeedNode exist for the integration tests that need real rows
// to reserve against. They are on the store rather than in a test helper because
// they are the store's own tables' minimal inserts, and a second copy of them in
// a test package would be a third place that knows the columns.

// SeedProvider inserts one active direct provider.
func (s *Store) SeedProvider(ctx context.Context, id uuid.UUID, name string) error {
	if _, err := s.db.Exec(ctx,
		`INSERT INTO providers (id, name, provider_type, status) VALUES ($1, $2, 'direct', 'active')`,
		id, name); err != nil {
		return fmt.Errorf("infrastore: seed the provider: %w", err)
	}
	return nil
}

// SeedGroup inserts one active node group.
func (s *Store) SeedGroup(ctx context.Context, id uuid.UUID, name string) error {
	if _, err := s.db.Exec(ctx,
		`INSERT INTO node_groups (id, name, region, status) VALUES ($1, $2, 'seed-region', 'active')`,
		id, name); err != nil {
		return fmt.Errorf("infrastore: seed the group: %w", err)
	}
	return nil
}

// SeedNode inserts one online node with the given capacity. A zero group
// identifier gets a group of its own, which is what a single-node test wants.
func (s *Store) SeedNode(ctx context.Context, nodeID, providerID, groupID uuid.UUID,
	cpu float64, memoryMB, diskGB int64) error {
	if groupID == uuid.Nil {
		groupID = uuid.New()
		if err := s.SeedGroup(ctx, groupID, "seed-group-"+groupID.String()[:8]); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(ctx,
		`INSERT INTO nodes (id, provider_id, node_group_id, provider_node_id, name, region, status,
		                   cpu_total, memory_total_mb, disk_total_gb)
		 VALUES ($1, $2, $3, $4, $5, 'seed-region', 'online', $6::numeric, $7, $8)`,
		nodeID, providerID, groupID, "seed-"+nodeID.String()[:8],
		"seed-node-"+nodeID.String()[:8], cpu, memoryMB, diskGB); err != nil {
		return fmt.Errorf("infrastore: seed the node: %w", err)
	}
	return nil
}

// NewPoolForTests opens a pool from a URL. It is test-only by convention and
// lives here so the tests do not each parse the environment themselves.
func NewPoolForTests(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, databaseURL)
}
