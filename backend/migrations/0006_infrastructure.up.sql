-- 0006_infrastructure — the infrastructure domain's tables and vocabularies.
--
-- 0004 created node_groups (as a foreign-key target for plans) and deliberately
-- left its status unconstrained: the words belonged to this phase. providers and
-- nodes are created here, with their vocabularies inline (ADR-007 §1).

-- docs/05's node machine: online/degraded/draining/maintenance/offline. The
-- group is an on/off switch for whether the platform schedules onto it.
ALTER TABLE node_groups
  ADD CONSTRAINT node_groups_status_known CHECK (status IN ('active', 'disabled'));

CREATE TABLE providers (
  id uuid PRIMARY KEY,
  name varchar(255) NOT NULL,
  provider_type varchar(64) NOT NULL,
  endpoint text,
  credential_ref varchar(255),
  status varchar(64) NOT NULL,
  version varchar(128),
  config jsonb NOT NULL DEFAULT '{}'::jsonb,
  capabilities jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_health_check_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT providers_status_known CHECK (status IN ('active', 'disabled')),
  CONSTRAINT providers_type_known CHECK (provider_type IN ('direct', 'agent')),
  -- A provider's own API contract, recorded as the platform reads it.
  CONSTRAINT providers_capabilities_is_an_object CHECK (jsonb_typeof(capabilities) = 'object'),
  CONSTRAINT providers_config_is_an_object CHECK (jsonb_typeof(config) = 'object')
);

CREATE TABLE nodes (
  id uuid PRIMARY KEY,
  provider_id uuid NOT NULL REFERENCES providers(id),
  node_group_id uuid REFERENCES node_groups(id),
  provider_node_id varchar(255),
  name varchar(255) NOT NULL UNIQUE,
  region varchar(128) NOT NULL,
  status varchar(64) NOT NULL,
  cpu_total numeric(10,2) NOT NULL DEFAULT 0,
  memory_total_mb bigint NOT NULL DEFAULT 0,
  disk_total_gb bigint NOT NULL DEFAULT 0,
  cpu_allocated numeric(10,2) NOT NULL DEFAULT 0,
  memory_allocated_mb bigint NOT NULL DEFAULT 0,
  disk_allocated_gb bigint NOT NULL DEFAULT 0,
  cpu_reserved numeric(10,2) NOT NULL DEFAULT 0,
  memory_reserved_mb bigint NOT NULL DEFAULT 0,
  disk_reserved_gb bigint NOT NULL DEFAULT 0,
  weight integer NOT NULL DEFAULT 100,
  capabilities jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_seen_at timestamptz,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  -- docs/05's node machine.
  CONSTRAINT nodes_status_known CHECK (
    status IN ('online', 'degraded', 'draining', 'maintenance', 'offline')
  ),
  CONSTRAINT nodes_provider_node_unique UNIQUE (provider_id, provider_node_id),
  CONSTRAINT nodes_capabilities_is_an_object CHECK (jsonb_typeof(capabilities) = 'object'),
  -- A capacity that has been promised or spent cannot be negative: the counters
  -- are the reservation's book, and a negative entry is a bug, not a state.
  CONSTRAINT nodes_allocated_non_negative CHECK (
    cpu_allocated >= 0 AND memory_allocated_mb >= 0 AND disk_allocated_gb >= 0
  ),
  CONSTRAINT nodes_reserved_non_negative CHECK (
    cpu_reserved >= 0 AND memory_reserved_mb >= 0 AND disk_reserved_gb >= 0
  ),
  -- The counters cannot exceed the machine.
  CONSTRAINT nodes_within_capacity CHECK (
    cpu_allocated + cpu_reserved <= cpu_total
    AND memory_allocated_mb + memory_reserved_mb <= memory_total_mb
    AND disk_allocated_gb + disk_reserved_gb <= disk_total_gb
  )
);

-- The scheduler reads a group's live nodes.
CREATE INDEX ix_nodes_group_status ON nodes (node_group_id, status);
