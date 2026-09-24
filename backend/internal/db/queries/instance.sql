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
