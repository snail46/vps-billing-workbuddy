-- 0008_instances — the vertical slice's tables: the instance a subscription
-- provisions and the notifications the platform leaves for its users (ADR-009).

-- docs/05's two instance machines: desired is what the customer asked for,
-- observed is what the provider last reported — and the two are not the same
-- column because a platform that conflates them cannot say "stopping".
CREATE TABLE instances (
  id uuid PRIMARY KEY,
  subscription_id uuid NOT NULL REFERENCES subscriptions(id),
  node_id uuid REFERENCES nodes(id),
  provider_id uuid REFERENCES providers(id),
  provider_instance_id varchar(255),
  name varchar(255) NOT NULL,
  desired_state varchar(64) NOT NULL,
  observed_state varchar(64) NOT NULL,
  cpu_cores numeric(10,2) NOT NULL,
  memory_mb integer NOT NULL,
  disk_gb integer NOT NULL,
  traffic_limit_gb bigint,
  bandwidth_mbps integer,
  image_id varchar(255),
  primary_ipv4 inet,
  primary_ipv6 inet,
  last_synced_at timestamptz,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  CONSTRAINT instances_desired_state_known CHECK (
    desired_state IN ('running', 'stopped', 'suspended', 'deleted')
  ),
  CONSTRAINT instances_observed_state_known CHECK (
    observed_state IN ('pending', 'provisioning', 'running', 'stopping', 'stopped',
                       'restarting', 'reinstalling', 'suspending', 'suspended',
                       'deleting', 'deleted', 'error', 'unknown')
  ),
  -- A live instance has exactly one home: the provider-side identifier is
  -- unique where it exists (the reference's index, carried over).
  CONSTRAINT instances_capacity_positive CHECK (
    cpu_cores > 0 AND memory_mb > 0 AND disk_gb > 0
  )
);

-- One provisioned instance per provider identity: a retried create that the
-- provider answered twice is caught here, not discovered by the customer.
CREATE UNIQUE INDEX ux_instance_provider_id
ON instances(provider_id, provider_instance_id)
WHERE provider_instance_id IS NOT NULL;

-- The customer's list reads their instances; the reconciler reads by state.
CREATE INDEX ix_instances_subscription ON instances (subscription_id);
CREATE INDEX ix_instances_observed ON instances (observed_state);

-- The reference's notifications table: a row the user can read later, keyed by
-- i18n message keys rather than rendered text (AGENTS.md: no hardcoded copy).
CREATE TABLE notifications (
  id uuid PRIMARY KEY,
  user_id uuid REFERENCES users(id),
  admin_id uuid REFERENCES admins(id),
  type varchar(128) NOT NULL,
  title_key varchar(255) NOT NULL,
  message_key varchar(255) NOT NULL,
  parameters jsonb NOT NULL DEFAULT '{}'::jsonb,
  severity varchar(32) NOT NULL,
  read_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT notifications_one_audience CHECK (
    (user_id IS NOT NULL)::int + (admin_id IS NOT NULL)::int = 1
  ),
  CONSTRAINT notifications_severity_known CHECK (
    severity IN ('info', 'success', 'warning', 'critical')
  ),
  CONSTRAINT notifications_parameters_is_an_object CHECK (jsonb_typeof(parameters) = 'object')
);

CREATE INDEX ix_notifications_user ON notifications (user_id, created_at DESC);
