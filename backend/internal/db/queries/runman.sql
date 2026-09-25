-- The agent provider's persistence (ADR-013): the credential registry and the
-- command queue. All writes are conditional updates; the idempotency key is
-- what makes a redelivered command the same command.

-- name: CreateRunmanAgent :exec
INSERT INTO runman_agents (id, node_id, token_hash, status)
VALUES ($1, $2, $3, $4);

-- name: RunmanAgentByTokenHash :one
SELECT * FROM runman_agents WHERE token_hash = $1;

-- name: RunmanAgentByID :one
SELECT * FROM runman_agents WHERE id = $1;

-- name: TouchRunmanAgent :execrows
UPDATE runman_agents
SET last_seen_at = $2, updated_at = $2
WHERE id = $1 AND status = 'active';

-- The enqueue is the idempotent insert: a second call with the same key reads
-- the row the first one wrote.
-- name: EnqueueRunmanCommand :one
INSERT INTO runman_commands (id, agent_id, type, idempotency_key, payload, status)
VALUES ($1, $2, $3, $4, $5, 'queued')
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING *;

-- name: RunmanCommandByIdempotencyKey :one
SELECT * FROM runman_commands WHERE idempotency_key = $1;

-- name: RunmanCommandByID :one
SELECT * FROM runman_commands WHERE id = $1;

-- The claim is one conditional UPDATE over the agent's queued rows: the same
-- idiom everywhere else in this codebase guards a transition.
-- name: ClaimRunmanCommands :many
UPDATE runman_commands c
SET status = 'delivered', delivered_at = $2, updated_at = $2
WHERE c.id IN (
  SELECT k.id FROM runman_commands k
  WHERE k.agent_id = $1 AND k.status = 'queued'
  ORDER BY k.created_at
  LIMIT 10
)
RETURNING c.*;

-- name: CompleteRunmanCommand :execrows
UPDATE runman_commands
SET status = $2, result = $3, error_code = $4, finished_at = $5, updated_at = $5
WHERE id = $1 AND status = 'delivered';

-- name: CountPendingRunmanCommands :one
SELECT count(*) FROM runman_commands WHERE agent_id = $1 AND status = 'queued';


-- name: ActiveRunmanAgentByNode :one
SELECT * FROM runman_agents
WHERE node_id = $1 AND status = 'active'
ORDER BY created_at DESC
LIMIT 1;

-- name: StaleRunmanAgents :many
SELECT * FROM runman_agents
WHERE status = 'active' AND (last_seen_at IS NULL OR last_seen_at < $1)
ORDER BY created_at
LIMIT 50;
