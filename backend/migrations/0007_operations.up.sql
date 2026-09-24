-- 0007_operations — the operation system (ADR-008): the durable record of every
-- long action, its steps, and the resource reservations that ride on them.

-- docs/05's operation machine:
-- queued/running/waiting_provider/waiting_resource/verifying/retrying/succeeded/failed/cancelled.
CREATE TABLE operations (
  id uuid PRIMARY KEY,
  type varchar(128) NOT NULL,
  resource_type varchar(64) NOT NULL,
  resource_id uuid NOT NULL,
  status varchar(64) NOT NULL,
  phase varchar(128),
  progress integer NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
  message_key varchar(255),
  provider_id uuid REFERENCES providers(id),
  provider_operation_id varchar(255),
  idempotency_key varchar(255) NOT NULL UNIQUE,
  retryable boolean NOT NULL DEFAULT false,
  retry_count integer NOT NULL DEFAULT 0,
  max_retries integer NOT NULL DEFAULT 0,
  error_code varchar(128),
  error_message text,
  trace_id varchar(255) NOT NULL,
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT operations_status_known CHECK (
    status IN ('queued', 'running', 'waiting_provider', 'waiting_resource',
               'verifying', 'retrying', 'succeeded', 'failed', 'cancelled')
  ),
  -- A failure that cannot be named cannot be supported (AGENTS.md: 0 silent failure).
  CONSTRAINT operations_failure_has_a_code CHECK (
    (status = 'failed') = (error_code IS NOT NULL)
  ),
  -- A finished operation carries its finish time, and a live one has none yet.
  CONSTRAINT operations_finished_at_matches_status CHECK (
    (status IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL)
  )
);

-- Progress is a fact per step, computed from the steps and never accepted from a
-- caller (docs/07: 进度按步骤映射，不按时间伪造).
CREATE TABLE operation_steps (
  id uuid PRIMARY KEY,
  operation_id uuid NOT NULL REFERENCES operations(id),
  step_key varchar(128) NOT NULL,
  step_order integer NOT NULL,
  status varchar(64) NOT NULL,
  progress integer NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
  attempt integer NOT NULL DEFAULT 0,
  error_code varchar(128),
  error_message text,
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(operation_id, step_key),
  CONSTRAINT operation_steps_status_known CHECK (
    status IN ('pending', 'running', 'succeeded', 'failed', 'skipped')
  )
);

CREATE INDEX ix_operations_status ON operations (status);
CREATE INDEX ix_operations_resource ON operations (resource_type, resource_id);

-- The reservation is a row (ADR-008 §5 amends ADR-007 §2): it names the operation
-- that promised the capacity, carries an expiry, and makes a workflow's retry of
-- a half-finished reserve idempotent instead of double-counting.
CREATE TABLE resource_reservations (
  id uuid PRIMARY KEY,
  node_id uuid NOT NULL REFERENCES nodes(id),
  operation_id uuid NOT NULL REFERENCES operations(id),
  cpu_cores numeric(10,2) NOT NULL DEFAULT 0,
  memory_mb bigint NOT NULL DEFAULT 0,
  disk_gb bigint NOT NULL DEFAULT 0,
  ipv4_count integer NOT NULL DEFAULT 0,
  ipv6_count integer NOT NULL DEFAULT 0,
  nat_port_count integer NOT NULL DEFAULT 0,
  status varchar(64) NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT resource_reservations_status_known CHECK (
    status IN ('reserved', 'committed', 'released', 'expired')
  )
);

-- One live reservation per operation: a retried reserve is idempotent.
CREATE UNIQUE INDEX ux_resource_reservations_open ON resource_reservations (operation_id) WHERE status = 'reserved';
CREATE INDEX ix_resource_reservations_node ON resource_reservations (node_id) WHERE status = 'reserved';
CREATE INDEX ix_resource_reservations_expiry ON resource_reservations (expires_at) WHERE status = 'reserved';
