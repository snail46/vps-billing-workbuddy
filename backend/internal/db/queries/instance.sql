-- The instance domain's queries (ADR-009).

-- name: CreateInstance :exec
INSERT INTO instances (id, subscription_id, node_id, provider_id, provider_instance_id,
                       name, desired_state, observed_state, cpu_cores, memory_mb, disk_gb,
                       image_id, last_synced_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- The idempotent create's lookup: a retried provision finds its instance by
-- the subscription that owns exactly one.
-- name: InstanceBySubscription :one
SELECT * FROM instances WHERE subscription_id = $1;

-- name: InstancesForUser :many
SELECT i.*
FROM instances i
JOIN subscriptions s ON s.id = i.subscription_id
WHERE s.user_id = $1 AND i.deleted_at IS NULL
ORDER BY i.created_at DESC;

-- The moment the provider confirms running: the node and the provider identity
-- land together with the observed state, in the caller's transaction.
-- name: SetInstanceProvisioned :execrows
UPDATE instances
SET node_id = $2, provider_id = $3, provider_instance_id = $4,
    observed_state = 'running', last_synced_at = $5, version = version + 1, updated_at = $5
WHERE id = $1;

-- name: MarkInstanceObserved :execrows
UPDATE instances
SET observed_state = $2, last_synced_at = $3, version = version + 1, updated_at = $3
WHERE id = $1;

-- name: CreateNotification :exec
INSERT INTO notifications (id, user_id, admin_id, type, title_key, message_key, parameters, severity)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- The action paths read the machine they act on; the runner too.
-- name: InstanceByID :one
SELECT * FROM instances WHERE id = $1 AND deleted_at IS NULL;

-- The reconciler's drift page: machines whose wish and whose record disagree
-- and that no live workflow is working on.
-- name: DriftingInstances :many
SELECT i.*
FROM instances i
WHERE i.deleted_at IS NULL
  AND i.desired_state <> i.observed_state
  AND i.observed_state <> 'provisioning' -- a machine being built belongs to its workflow, not to drift
  AND i.node_id IS NOT NULL AND i.provider_id IS NOT NULL
  AND i.provider_instance_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM operations o
    WHERE o.resource_type = 'instance' AND o.resource_id = i.id
      AND o.status IN ('queued', 'running', 'waiting_provider', 'waiting_resource', 'verifying', 'retrying')
  )
ORDER BY i.created_at
LIMIT 50;
