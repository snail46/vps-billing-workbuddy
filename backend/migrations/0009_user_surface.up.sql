-- 0009_user_surface — the tables the customer's own pages need (ADR-011):
-- the network, port-forward and traffic records the instance detail reads,
-- and the ticket pair the support page is built on. All existed in the
-- reference schema from the start; this phase is where the user web finally
-- reads them.

-- The instance's addresses as the provider actually wired them: the network
-- tab reads these rows, one per address family.
CREATE TABLE instance_networks (
  id uuid PRIMARY KEY,
  instance_id uuid NOT NULL REFERENCES instances(id),
  type varchar(32) NOT NULL,
  address inet,
  gateway inet,
  prefix integer,
  provider_network_id varchar(255),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT instance_networks_type_known CHECK (
    type IN ('public_ipv4', 'public_ipv6', 'private', 'nat')
  )
);

CREATE INDEX ix_instance_networks_instance ON instance_networks (instance_id);

-- proxy devices = 端口转发面 (ADR-010): what the provider reports, what the
-- customer reads on the instance's network tab. The public triple is unique —
-- two instances cannot both hold the same public port.
CREATE TABLE port_forwards (
  id uuid PRIMARY KEY,
  instance_id uuid NOT NULL REFERENCES instances(id),
  protocol varchar(16) NOT NULL,
  public_ip inet NOT NULL,
  public_port integer NOT NULL CHECK (public_port BETWEEN 1 AND 65535),
  guest_port integer NOT NULL CHECK (guest_port BETWEEN 1 AND 65535),
  description varchar(255),
  status varchar(64) NOT NULL,
  provider_mapping_id varchar(255),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT port_forwards_protocol_known CHECK (protocol IN ('tcp', 'udp'))
);

CREATE UNIQUE INDEX ux_port_forward
ON port_forwards(public_ip, protocol, public_port);

CREATE INDEX ix_port_forwards_instance ON port_forwards (instance_id);

-- Traffic per billing period per source: the instance's traffic tab reads the
-- current period; the reconciler writes what the provider observed.
CREATE TABLE traffic_usage (
  id uuid PRIMARY KEY,
  instance_id uuid NOT NULL REFERENCES instances(id),
  period_start timestamptz NOT NULL,
  period_end timestamptz NOT NULL,
  rx_bytes bigint NOT NULL DEFAULT 0 CHECK (rx_bytes >= 0),
  tx_bytes bigint NOT NULL DEFAULT 0 CHECK (tx_bytes >= 0),
  source varchar(64) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT traffic_usage_period_ordered CHECK (period_end > period_start)
);

CREATE UNIQUE INDEX ux_traffic_period
ON traffic_usage (instance_id, period_start, source);

-- Tickets: a conversation, not a commerce flow (ADR-011 §4). The customer
-- opens with a subject and a first message; either side writes more; the
-- owner closes. There is no state machine in docs/05 because nothing
-- asynchronous happens to a ticket — only writes by parties that are present.
CREATE TABLE tickets (
  id uuid PRIMARY KEY,
  ticket_no varchar(64) NOT NULL UNIQUE,
  user_id uuid NOT NULL REFERENCES users(id),
  subject varchar(255) NOT NULL,
  status varchar(64) NOT NULL,
  priority varchar(32) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  closed_at timestamptz,
  CONSTRAINT tickets_status_known CHECK (status IN ('open', 'answered', 'closed')),
  CONSTRAINT tickets_priority_known CHECK (priority IN ('low', 'normal', 'high'))
);

CREATE INDEX ix_tickets_user ON tickets (user_id, created_at DESC);

CREATE TABLE ticket_messages (
  id uuid PRIMARY KEY,
  ticket_id uuid NOT NULL REFERENCES tickets(id),
  sender_type varchar(32) NOT NULL,
  sender_id uuid,
  message text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT ticket_messages_sender_known CHECK (
    sender_type IN ('user', 'admin', 'system')
  )
);

CREATE INDEX ix_ticket_messages_ticket ON ticket_messages (ticket_id, created_at);
