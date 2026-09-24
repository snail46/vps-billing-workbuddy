package infrastore

// The infrastructure adapter: sqlc's generated queries behind the interfaces
// the infra domain and the workflows declare.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/infra"
	"github.com/snail46/vps-billing-workbuddy/backend/internal/provider"
)

// Store reads the infrastructure and drives its capacity transitions.
type Store struct {
	queries *sqlcgen.Queries
	db      sqlcgen.DBTX
	pool    *pgxpool.Pool
	tx      pgx.Tx
}

// New returns a store over a pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{queries: sqlcgen.New(pool), db: pool, pool: pool}
}

// newBound returns a store bound to a transaction, so a reservation and the
// writes it guards share one commit.
func newBound(pool *pgxpool.Pool, tx pgx.Tx) *Store {
	return &Store{queries: sqlcgen.New(tx), db: tx, pool: pool, tx: tx}
}

// WithinTransaction runs fn against a store bound to a new transaction. Nested
// calls stay inside the caller's transaction: the boundary is the outermost.
func (s *Store) WithinTransaction(ctx context.Context, fn func(*Store) error) error {
	if s.tx != nil {
		return fn(s)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("infrastore: begin: %w", err)
	}
	if err := fn(newBound(s.pool, tx)); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// --------------------------------------------------------------- catalogue --

// ProviderRecord is a provider as stored — the platform's record of the seam,
// not the live capability read.
type ProviderRecord struct {
	ID           uuid.UUID
	Name         string
	Type         string
	Endpoint     *string
	Credential   *string
	Status       string
	Version      *string
	Capabilities provider.Capabilities
}

// ListProviders returns every provider, name order.
func (s *Store) ListProviders(ctx context.Context) ([]ProviderRecord, error) {
	rows, err := s.queries.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("infrastore: list providers: %w", err)
	}
	out := make([]ProviderRecord, 0, len(rows))
	for i := range rows {
		record := ProviderRecord{
			ID:     rows[i].ID,
			Name:   rows[i].Name,
			Type:   rows[i].ProviderType,
			Status: rows[i].Status,
		}
		if rows[i].Endpoint.Valid {
			value := rows[i].Endpoint.String
			record.Endpoint = &value
		}
		if rows[i].CredentialRef.Valid {
			value := rows[i].CredentialRef.String
			record.Credential = &value
		}
		if rows[i].Version.Valid {
			value := rows[i].Version.String
			record.Version = &value
		}
		// A capabilities column that does not decode is a row this code did not
		// write; it is carried as empty rather than dropped with its bytes.
		_ = json.Unmarshal(rows[i].Capabilities, &record.Capabilities)
		out = append(out, record)
	}
	return out, nil
}

// ListNodeGroups returns every group, name order.
func (s *Store) ListNodeGroups(ctx context.Context) ([]infra.NodeGroup, error) {
	rows, err := s.queries.ListNodeGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("infrastore: list node groups: %w", err)
	}
	groups := make([]infra.NodeGroup, 0, len(rows))
	for i := range rows {
		groups = append(groups, infra.NodeGroup{
			ID:     rows[i].ID,
			Name:   rows[i].Name,
			Region: rows[i].Region,
			Status: rows[i].Status,
		})
	}
	return groups, nil
}

// ListNodes returns every node, name order.
func (s *Store) ListNodes(ctx context.Context) ([]infra.Node, error) {
	rows, err := s.queries.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("infrastore: list nodes: %w", err)
	}
	nodes := make([]infra.Node, 0, len(rows))
	for i := range rows {
		// The two node queries select the same columns in the same order, so the
		// struct conversion is exact; if one gains a column the other lacks, the
		// conversion stops compiling rather than silently losing the field.
		node, err := toNode(sqlcgen.NodesForGroupRow(rows[i]))
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// NodesForGroup returns a group's online nodes, id order — the scheduler's
// candidate list. Deterministic ordering is the query's, not the caller's.
func (s *Store) NodesForGroup(ctx context.Context, groupID uuid.UUID) ([]infra.Node, error) {
	rows, err := s.queries.NodesForGroup(ctx, &groupID)
	if err != nil {
		return nil, fmt.Errorf("infrastore: list the group's nodes: %w", err)
	}
	nodes := make([]infra.Node, 0, len(rows))
	for i := range rows {
		node, err := toNode(rows[i])
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// toNode converts one nodes row. The numeric capacity columns arrive as their
// exact decimal text — a float that reads back as 1.9999999 is the first
// symptom of a rounding decision that was never made.
func toNode(row sqlcgen.NodesForGroupRow) (infra.Node, error) {
	node := infra.Node{
		ID:             row.ID,
		ProviderID:     row.ProviderID,
		NodeGroupID:    derefUUID(row.NodeGroupID),
		ProviderNodeID: textValue(row.ProviderNodeID),
		Name:           row.Name,
		Region:         row.Region,
		Status:         row.Status,
		MemoryTotalMB:  row.MemoryTotalMb,
		DiskTotalGB:    row.DiskTotalGb,
		MemoryAllocMB:  row.MemoryAllocatedMb,
		DiskAllocGB:    row.DiskAllocatedGb,
		MemoryRsrvMB:   row.MemoryReservedMb,
		DiskRsrvGB:     row.DiskReservedGb,
		Weight:         int(row.Weight),
		Version:        row.Version,
	}
	for text, target := range map[string]*float64{
		row.CpuTotalText:     &node.CPUTotal,
		row.CpuAllocatedText: &node.CPUAllocated,
		row.CpuReservedText:  &node.CPURserved,
	} {
		value, err := parseDecimal(text)
		if err != nil {
			return infra.Node{}, fmt.Errorf("infrastore: %w", err)
		}
		*target = value
	}
	if err := json.Unmarshal(row.Capabilities, &node.Capabilities); err != nil {
		return infra.Node{}, fmt.Errorf("infrastore: decode nodes.capabilities: %w", err)
	}
	return node, nil
}

// ------------------------------------------------------------ reservations --

// Reserve promises capacity on a node, if the node's free capacity covers the
// spec as the row stands right now. A false result is the shortage — or a node
// that is not online, which for scheduling is the same answer.
func (s *Store) Reserve(ctx context.Context, nodeID uuid.UUID, spec infra.Spec, at time.Time) (bool, error) {
	tag, err := s.queries.ReserveNodeResources(ctx, sqlcgen.ReserveNodeResourcesParams{
		ID:               nodeID,
		Column2:          numericOf(spec.CPUCores),
		MemoryReservedMb: spec.MemoryMB,
		DiskReservedGb:   spec.DiskGB,
		UpdatedAt:        pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("infrastore: reserve: %w", err)
	}
	return tag == 1, nil
}

// Commit moves a kept promise into allocation, in the caller's transaction.
func (s *Store) Commit(ctx context.Context, nodeID uuid.UUID, spec infra.Spec, at time.Time) (bool, error) {
	tag, err := s.queries.CommitNodeResources(ctx, sqlcgen.CommitNodeResourcesParams{
		ID:                nodeID,
		Column2:           numericOf(spec.CPUCores),
		MemoryAllocatedMb: spec.MemoryMB,
		DiskAllocatedGb:   spec.DiskGB,
		UpdatedAt:         pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("infrastore: commit: %w", err)
	}
	return tag == 1, nil
}

// Release withdraws a promise. A false result means the reservation was not on
// the row — already committed or already released.
func (s *Store) Release(ctx context.Context, nodeID uuid.UUID, spec infra.Spec, at time.Time) (bool, error) {
	tag, err := s.queries.ReleaseNodeResources(ctx, sqlcgen.ReleaseNodeResourcesParams{
		ID:               nodeID,
		Column2:          numericOf(spec.CPUCores),
		MemoryReservedMb: spec.MemoryMB,
		DiskReservedGb:   spec.DiskGB,
		UpdatedAt:        pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("infrastore: release: %w", err)
	}
	return tag == 1, nil
}

// numericOf carries a float into the numeric column through its exact text, so
// the value the platform books is the value the plan stated.
func numericOf(value float64) pgtype.Numeric {
	var numeric pgtype.Numeric
	if err := numeric.Scan(strconv.FormatFloat(value, 'f', -1, 64)); err != nil {
		// Unreachable for a finite float; a NaN would be a caller bug worth a
		// loud zero rather than a silently wrong capacity.
		return pgtype.Numeric{}
	}
	return numeric
}

// parseDecimal reads the exact text form of a numeric column.
func parseDecimal(text string) (float64, error) {
	if text == "" {
		return 0, nil
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("decode numeric %q", text)
	}
	return value, nil
}

func derefUUID(value *uuid.UUID) uuid.UUID {
	if value == nil {
		return uuid.Nil
	}
	return *value
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
