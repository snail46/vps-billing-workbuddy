-- The operation queries.
--
-- The claim is SKIP LOCKED (ADR-008 §1): N workers each take distinct operations
-- and none waits, which is what makes the database a queue without a broker.

-- ------------------------------------------------------------------ writes --

-- name: CreateOperation :exec
INSERT INTO operations (
  id, type, resource_type, resource_id, status, idempotency_key,
  max_retries, trace_id, created_at
) VALUES ($1, $2, $3, $4, 'queued', $5, $6, $7, $8);

-- name: CreateOperationStep :exec
INSERT INTO operation_steps (
  id, operation_id, step_key, step_order, status, created_at
) VALUES ($1, $2, $3, $4, 'pending', $5);

-- The claim: one worker takes one queued operation. SKIP LOCKED means N workers
-- on the same database each take distinct rows and none of them waits.
-- name: ClaimQueuedOperation :one
UPDATE operations
SET status = 'running', started_at = $1, updated_at = $1
WHERE id = (
  SELECT o.id FROM operations o
  WHERE o.status = 'queued'
  ORDER BY o.created_at
  FOR UPDATE SKIP LOCKED
  LIMIT 1
)
RETURNING id, type, resource_type, resource_id, status, phase, progress,
          message_key, provider_id, provider_operation_id, idempotency_key,
          retryable, retry_count, max_retries, error_code, error_message,
          trace_id, started_at, finished_at, created_at, updated_at;

-- The claim of a retry whose backoff has elapsed: the machine edge
-- retrying → running, with the attempt count already on the row.
-- name: ClaimRetryableOperation :one
UPDATE operations
SET status = 'running', started_at = COALESCE(started_at, $1), updated_at = $1
WHERE id = (
  SELECT o.id FROM operations o
  WHERE o.status = 'retrying' AND o.updated_at <= $2
  ORDER BY o.updated_at
  FOR UPDATE SKIP LOCKED
  LIMIT 1
)
RETURNING id, type, resource_type, resource_id, status, phase, progress,
          message_key, provider_id, provider_operation_id, idempotency_key,
          retryable, retry_count, max_retries, error_code, error_message,
          trace_id, started_at, finished_at, created_at, updated_at;

-- ------------------------------------------------------------------- reads --

-- name: OperationByID :one
SELECT id, type, resource_type, resource_id, status, phase, progress,
       message_key, provider_id, provider_operation_id, idempotency_key,
       retryable, retry_count, max_retries, error_code, error_message,
       trace_id, started_at, finished_at, created_at, updated_at
FROM operations
WHERE id = $1;

-- name: OperationByIdempotencyKey :one
SELECT id, type, resource_type, resource_id, status, phase, progress,
       message_key, provider_id, provider_operation_id, idempotency_key,
       retryable, retry_count, max_retries, error_code, error_message,
       trace_id, started_at, finished_at, created_at, updated_at
FROM operations
WHERE idempotency_key = $1;

-- name: StepsForOperation :many
SELECT id, operation_id, step_key, step_order, status, progress, attempt,
       error_code, error_message, started_at, finished_at, created_at, updated_at
FROM operation_steps
WHERE operation_id = $1
ORDER BY step_order;

-- ---------------------------------------------------------------- machine --

-- A generic conditional transition. The target's own rules are the caller's
-- (the machine is enforced in the domain); the statement guarantees that the
-- row moves only from the state the caller saw.
-- name: TransitionOperation :execrows
UPDATE operations
SET status = $2, phase = $3, message_key = $4, error_code = $5,
    error_message = $6, updated_at = $7,
    finished_at = CASE WHEN $2 IN ('succeeded', 'failed', 'cancelled')
                       THEN $7 ELSE finished_at END
WHERE id = $1 AND status = $8;

-- A retry: the attempt count advances and the row waits for its backoff.
-- name: MoveOperationToRetrying :execrows
UPDATE operations
SET status = 'retrying', retry_count = retry_count + 1, error_code = $2,
    error_message = $3, updated_at = $4
WHERE id = $1 AND status = $5;

-- name: CancelOperation :execrows
UPDATE operations
SET status = 'cancelled', finished_at = $2, updated_at = $2
WHERE id = $1 AND status IN ('queued', 'running', 'waiting_provider',
                             'waiting_resource', 'verifying', 'retrying');

-- ------------------------------------------------------------------ steps --

-- name: StartOperationStep :execrows
UPDATE operation_steps
SET status = 'running', attempt = attempt + 1, started_at = $2, updated_at = $2
WHERE operation_id = $1 AND step_key = $2 AND status IN ('pending', 'failed');

-- name: FinishOperationStep :execrows
UPDATE operation_steps
SET status = $2, error_code = $3, error_message = $4, finished_at = $5, updated_at = $5
WHERE operation_id = $1 AND step_key = $2 AND status = 'running';

-- name: SetOperationPhase :execrows
UPDATE operations
SET phase = $2, progress = $3, message_key = $4, updated_at = $5
WHERE id = $1;

-- The progress is derived from the steps, never accepted from a caller
-- (docs/07: 进度按步骤映射，不按时间伪造).
-- name: SyncOperationProgress :execrows
UPDATE operations
SET progress = (
  SELECT COALESCE(SUM(CASE WHEN status IN ('succeeded', 'skipped') THEN 100 ELSE progress END)
         / GREATEST(count(*), 1), 0)
  FROM operation_steps WHERE operation_id = $1
), updated_at = $2
WHERE id = $1;
