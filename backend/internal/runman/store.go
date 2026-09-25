// Package runman is the agent provider's gateway and transport (ADR-013):
// the registry of node credentials, the durable command queue the agent
// claims, and the provider adapter that turns docs/06's interface into
// commands that ride the agent's connection.
package runman

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	sqlcgen "github.com/snail46/vps-billing-workbuddy/backend/internal/db/sqlcgen"
)

// Command types — the vocabulary the agent speaks and the adapter writes.
const (
	CmdCreateInstance    = "create_instance"
	CmdDeleteInstance    = "delete_instance"
	CmdStartInstance     = "start_instance"
	CmdStopInstance      = "stop_instance"
	CmdRestartInstance   = "restart_instance"
	CmdReinstallInstance = "reinstall_instance"
	CmdGetState          = "get_state"
	CmdGetUsage          = "get_usage"
	CmdGetTraffic        = "get_traffic"
	CmdListPortForwards  = "list_port_forwards"
	CmdAddPortForward    = "add_port_forward"
	CmdDeletePortForward = "delete_port_forward"
	CmdListImages        = "list_images"
)

// Command states.
const (
	StatusQueued    = "queued"
	StatusDelivered = "delivered"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// HashToken derives the registry's stored form of an agent token. The token
// itself is shown once at creation and never stored.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Command is one queued request, as the gateway and the adapter both see it.
type Command = sqlcgen.RunmanCommand

// Store is the registry and the queue.
type Store struct {
	queries *sqlcgen.Queries
	pool    *pgxpool.Pool
}

// New builds a store over the pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{queries: sqlcgen.New(pool), pool: pool}
}

// Queries exposes the generated queries for the gateway handlers.
func (s *Store) Queries() *sqlcgen.Queries { return s.queries }

// CreateAgent registers one node's agent with its token. The node id is the
// opaque string the provider interface addresses nodes by.
func (s *Store) CreateAgent(ctx context.Context, nodeID, token string) (uuid.UUID, error) {
	id := uuid.New()
	if err := s.queries.CreateRunmanAgent(ctx, sqlcgen.CreateRunmanAgentParams{
		ID:        id,
		NodeID:    nodeID,
		TokenHash: HashToken(token),
		Status:    "active",
	}); err != nil {
		return uuid.Nil, fmt.Errorf("runman: create agent: %w", err)
	}
	return id, nil
}

// Authenticate validates a presented token and answers the agent it names.
func (s *Store) Authenticate(ctx context.Context, token string) (sqlcgen.RunmanAgent, error) {
	agent, err := s.queries.RunmanAgentByTokenHash(ctx, HashToken(token))
	if err != nil {
		return sqlcgen.RunmanAgent{}, fmt.Errorf("runman: authenticate: %w", err)
	}
	return agent, nil
}

// Heartbeat stamps the agent's last seen and answers how much work waits.
func (s *Store) Heartbeat(ctx context.Context, agentID uuid.UUID) (int64, error) {
	tag, err := s.queries.TouchRunmanAgent(ctx, sqlcgen.TouchRunmanAgentParams{
		ID:         agentID,
		LastSeenAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		return 0, fmt.Errorf("runman: heartbeat: %w", err)
	}
	if tag != 1 {
		return 0, fmt.Errorf("runman: agent %s is not active", agentID)
	}
	return s.queries.CountPendingRunmanCommands(ctx, agentID)
}

// Enqueue writes one command, or returns the one an earlier call with the
// same key wrote.
func (s *Store) Enqueue(ctx context.Context, agentID uuid.UUID, cmdType, idempotencyKey string, payload any) (Command, error) {
	if idempotencyKey == "" {
		return Command{}, fmt.Errorf("runman: a command without an idempotency key cannot be redelivered safely")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Command{}, fmt.Errorf("runman: encode payload: %w", err)
	}
	command, err := s.queries.EnqueueRunmanCommand(ctx, sqlcgen.EnqueueRunmanCommandParams{
		ID:             uuid.New(),
		AgentID:        agentID,
		Type:           cmdType,
		IdempotencyKey: idempotencyKey,
		Payload:        encoded,
	})
	if err == nil {
		return command, nil
	}
	// The conflict path is the normal path for a retried create.
	existing, lookErr := s.queries.RunmanCommandByIdempotencyKey(ctx, idempotencyKey)
	if lookErr != nil {
		return Command{}, fmt.Errorf("runman: enqueue: %w", err)
	}
	return existing, nil
}

// Claim moves the agent's queued commands to delivered and hands them over.
func (s *Store) Claim(ctx context.Context, agentID uuid.UUID) ([]Command, error) {
	now := time.Now().UTC()
	commands, err := s.queries.ClaimRunmanCommands(ctx, sqlcgen.ClaimRunmanCommandsParams{
		AgentID:     agentID,
		DeliveredAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("runman: claim: %w", err)
	}
	return commands, nil
}

// Complete closes one delivered command with its outcome.
func (s *Store) Complete(ctx context.Context, commandID uuid.UUID, status string, result json.RawMessage, errorCode string) error {
	tag, err := s.queries.CompleteRunmanCommand(ctx, sqlcgen.CompleteRunmanCommandParams{
		ID:         commandID,
		Status:     status,
		Result:     result,
		ErrorCode:  pgText(errorCode),
		FinishedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("runman: complete: %w", err)
	}
	if tag != 1 {
		return fmt.Errorf("runman: command %s is not in delivered state", commandID)
	}
	return nil
}

// Wait polls one command until it reaches a terminal state or the deadline.
// The wait is the adapter's timeout, and a timed-out command may still land —
// which is exactly why the engine's backoff rides it (ADR-013 §3).
func (s *Store) Wait(ctx context.Context, idempotencyKey string, timeout time.Duration) (Command, error) {
	deadline := time.Now().Add(timeout)
	for {
		command, err := s.queries.RunmanCommandByIdempotencyKey(ctx, idempotencyKey)
		if err != nil {
			return Command{}, fmt.Errorf("runman: wait: %w", err)
		}
		if command.Status == StatusSucceeded || command.Status == StatusFailed {
			return command, nil
		}
		if time.Now().After(deadline) {
			return command, nil
		}
		select {
		case <-ctx.Done():
			return command, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func pgText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

// StaleAgents reads the active agents whose last-seen stamp aged past the
// given moment — the reconciler's node-silence page.
func (s *Store) StaleAgents(ctx context.Context, staleBefore time.Time) ([]sqlcgen.RunmanAgent, error) {
	agents, err := s.queries.StaleRunmanAgents(ctx, pgtype.Timestamptz{Time: staleBefore, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("runman: read stale agents: %w", err)
	}
	return agents, nil
}
