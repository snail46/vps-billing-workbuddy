-- 0010_runman — the agent provider's registry and command queue (ADR-013).
-- The agent dials out with a node token; the platform's provider calls become
-- rows the agent claims and answers. Redis is deliberately absent: a command
-- is a durable request to real hardware.

-- One credential per node. The token itself is never stored — only its hash —
-- because a registry that leaks its own credentials is worse than no registry.
-- node_id carries the platform's node identifier as the provider interface
-- presents it: an opaque string. Referential integrity is the enrollment
-- flow's job, not the schema's — the contract suite addresses agents by the
-- same opaque string the business core does.
CREATE TABLE runman_agents (
  id uuid PRIMARY KEY,
  node_id varchar(255) NOT NULL UNIQUE,
  token_hash varchar(128) NOT NULL UNIQUE,
  status varchar(32) NOT NULL,
  last_seen_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT runman_agents_status_known CHECK (status IN ('active', 'revoked'))
);

-- The delivery queue. One idempotency key per command: a gateway that
-- redelivers is answered by the same row, not a second machine working.
CREATE TABLE runman_commands (
  id uuid PRIMARY KEY,
  agent_id uuid NOT NULL REFERENCES runman_agents(id),
  type varchar(64) NOT NULL,
  idempotency_key varchar(255) NOT NULL UNIQUE,
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  status varchar(32) NOT NULL,
  result jsonb,
  error_code varchar(128),
  created_at timestamptz NOT NULL DEFAULT now(),
  delivered_at timestamptz,
  finished_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT runman_commands_type_known CHECK (
    type IN ('create_instance', 'delete_instance', 'start_instance', 'stop_instance',
             'restart_instance', 'reinstall_instance', 'get_state', 'get_usage',
             'get_traffic', 'list_port_forwards', 'add_port_forward', 'delete_port_forward',
             'list_images')
  ),
  CONSTRAINT runman_commands_status_known CHECK (
    status IN ('queued', 'delivered', 'succeeded', 'failed')
  ),
  CONSTRAINT runman_commands_terminal_matches_finish CHECK (
    (status IN ('succeeded', 'failed')) = (finished_at IS NOT NULL)
  )
);

CREATE INDEX ix_runman_commands_agent_status ON runman_commands (agent_id, status);
CREATE INDEX ix_runman_commands_idem ON runman_commands (idempotency_key);
