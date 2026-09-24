-- The infrastructure queries.
--
-- The reservation transitions are conditional updates for the same reason every
-- other writer here is: repetition and concurrency are the normal case, and the
-- row's own counters are the gate.

-- ------------------------------------------------------------------- reads --

-- name: ListProviders :many
SELECT id, name, provider_type, endpoint, credential_ref, status, version,
       config, capabilities, last_health_check_at, created_at, updated_at
FROM providers
ORDER BY name;

-- name: ProviderByID :one
SELECT id, name, provider_type, endpoint, credential_ref, status, version,
       config, capabilities, last_health_check_at, created_at, updated_at
FROM providers
WHERE id = $1;

-- name: ListNodeGroups :many
SELECT id, name, region, status, created_at, updated_at
FROM node_groups
ORDER BY name;

-- name: ListNodes :many
SELECT id, provider_id, node_group_id, provider_node_id, name, region, status,
       cpu_total::text AS cpu_total_text, memory_total_mb, disk_total_gb,
       cpu_allocated::text AS cpu_allocated_text, memory_allocated_mb, disk_allocated_gb,
       cpu_reserved::text AS cpu_reserved_text, memory_reserved_mb, disk_reserved_gb,
       weight, capabilities, last_seen_at, version, created_at, updated_at
FROM nodes
ORDER BY name;

-- name: NodesForGroup :many
SELECT id, provider_id, node_group_id, provider_node_id, name, region, status,
       cpu_total::text AS cpu_total_text, memory_total_mb, disk_total_gb,
       cpu_allocated::text AS cpu_allocated_text, memory_allocated_mb, disk_allocated_gb,
       cpu_reserved::text AS cpu_reserved_text, memory_reserved_mb, disk_reserved_gb,
       weight, capabilities, last_seen_at, version, created_at, updated_at
FROM nodes
WHERE node_group_id = $1 AND status = 'online'
ORDER BY id;

-- name: NodeByID :one
SELECT id, provider_id, node_group_id, provider_node_id, name, region, status,
       cpu_total::text AS cpu_total_text, memory_total_mb, disk_total_gb,
       cpu_allocated::text AS cpu_allocated_text, memory_allocated_mb, disk_allocated_gb,
       cpu_reserved::text AS cpu_reserved_text, memory_reserved_mb, disk_reserved_gb,
       weight, capabilities, last_seen_at, version, created_at, updated_at
FROM nodes
WHERE id = $1;

-- ------------------------------------------------------------ reservations --

-- The reserve: one statement is the whole gate. The counters only move if the
-- node's free capacity covers the spec as the row stands right now, so two
-- concurrent reservations of the same last vCPU cannot both promise it — the
-- loser sees no row and reports the shortage.
-- name: ReserveNodeResources :execrows
UPDATE nodes
SET cpu_reserved = cpu_reserved + $2::numeric,
    memory_reserved_mb = memory_reserved_mb + $3,
    disk_reserved_gb = disk_reserved_gb + $4,
    version = version + 1, updated_at = $5
WHERE id = $1 AND status = 'online'
  AND cpu_total - cpu_allocated - cpu_reserved >= $2::numeric
  AND memory_total_mb - memory_allocated_mb - memory_reserved_mb >= $3
  AND disk_total_gb - disk_allocated_gb - disk_reserved_gb >= $4;

-- The commit: the promise becomes an allocation, in the transaction that
-- records the instance it bought. Unconditional on the counters because the
-- reservation that reached here was already gated; a commit for a reservation
-- that was never made is a caller bug this cannot fix.
-- name: CommitNodeResources :execrows
UPDATE nodes
SET cpu_allocated = cpu_allocated + $2::numeric,
    memory_allocated_mb = memory_allocated_mb + $3,
    disk_allocated_gb = disk_allocated_gb + $4,
    cpu_reserved = cpu_reserved - $2::numeric,
    memory_reserved_mb = memory_reserved_mb - $3,
    disk_reserved_gb = disk_reserved_gb - $4,
    version = version + 1, updated_at = $5
WHERE id = $1;

-- The release: the promise is withdrawn. Conditional on the counters holding
-- the reservation, so a double release or a release after commit moves nothing
-- rather than driving the counters negative.
-- name: ReleaseNodeResources :execrows
UPDATE nodes
SET cpu_reserved = cpu_reserved - $2::numeric,
    memory_reserved_mb = memory_reserved_mb - $3,
    disk_reserved_gb = disk_reserved_gb - $4,
    version = version + 1, updated_at = $5
WHERE id = $1
  AND cpu_reserved >= $2::numeric
  AND memory_reserved_mb >= $3
  AND disk_reserved_gb >= $4;
