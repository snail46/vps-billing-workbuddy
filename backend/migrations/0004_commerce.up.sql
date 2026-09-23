-- 0004_commerce — products, plans, orders, payments, the ledger and the outbox.
--
-- Columns follow `db/schema.sql`. What this migration adds is what the reference
-- leaves implicit, and the additions are of three kinds:
--
--   1. Status vocabularies as CHECK constraints. `docs/05` fixes the order, payment
--      and subscription machines as words; a vocabulary that lives only in a
--      document acquires a typo, and a typo in a status is a state nothing handles.
--      Phase 1 treated `users.status` the same way.
--   2. Arithmetic that must hold, as CHECK constraints: a total that is not the sum
--      of its parts, and a line total that is not unit price times quantity, are
--      both states the application should never produce and the schema can make
--      impossible. The ledger's balance is enforced in code (see ADR-005) because it
--      spans rows and a CHECK cannot see them.
--   3. Indexes on the columns the queries filter by.
--
-- Two tables are created that this phase does not implement: `node_groups`, which
-- `plans.node_group_id` references, and `subscriptions`, which
-- `invoices.subscription_id` references. A foreign key needs its target to exist, so
-- the tables are here; the rows and the behaviour are Phase 4's and Phase 3's
-- respectively. Their status columns are deliberately left unconstrained for the
-- same reason: the vocabulary belongs to the phase that owns the machine, and
-- inventing it here would be guessing at another phase's design.
--
-- Money is in minor units as BIGINT everywhere, and no column here holds a floating
-- point value.

-- Node groups are the placement pool a plan is sold from. Phase 4 owns their
-- lifecycle; this phase only needs the table to exist.
CREATE TABLE node_groups (
  id uuid PRIMARY KEY,
  name varchar(255) NOT NULL UNIQUE,
  region varchar(128) NOT NULL,
  status varchar(64) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- `name_i18n` and `description_i18n` are objects keyed by locale rather than columns
-- per language (docs/13): adding a third locale must not be a migration.
CREATE TABLE products (
  id uuid PRIMARY KEY,
  slug varchar(255) NOT NULL UNIQUE,
  name_i18n jsonb NOT NULL,
  description_i18n jsonb NOT NULL DEFAULT '{}'::jsonb,
  status varchar(64) NOT NULL,
  sort_order integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT products_status_known CHECK (status IN ('draft', 'active', 'archived')),
  CONSTRAINT products_name_is_an_object CHECK (jsonb_typeof(name_i18n) = 'object'),
  CONSTRAINT products_description_is_an_object CHECK (jsonb_typeof(description_i18n) = 'object')
);

CREATE TABLE plans (
  id uuid PRIMARY KEY,
  product_id uuid NOT NULL REFERENCES products(id),
  node_group_id uuid REFERENCES node_groups(id),
  slug varchar(255) NOT NULL,
  name_i18n jsonb NOT NULL,
  status varchar(64) NOT NULL,
  cpu_cores numeric(10,2) NOT NULL,
  memory_mb integer NOT NULL,
  disk_gb integer NOT NULL,
  traffic_gb bigint,
  bandwidth_mbps integer,
  ipv4_count integer NOT NULL DEFAULT 0,
  ipv6_count integer NOT NULL DEFAULT 0,
  nat_port_count integer NOT NULL DEFAULT 0,
  virtualization varchar(64) NOT NULL,
  billing_cycle varchar(64) NOT NULL,
  price_minor bigint NOT NULL CHECK (price_minor >= 0),
  currency varchar(3) NOT NULL,
  stock_mode varchar(64) NOT NULL DEFAULT 'automatic',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(product_id, slug),
  CONSTRAINT plans_status_known CHECK (status IN ('draft', 'active', 'archived')),
  CONSTRAINT plans_cpu_positive CHECK (cpu_cores > 0),
  CONSTRAINT plans_memory_positive CHECK (memory_mb > 0),
  CONSTRAINT plans_disk_positive CHECK (disk_gb > 0),
  -- The billing cycle decides when a subscription renews, so an unrecognised value
  -- would be a subscription that never renews and never says why.
  CONSTRAINT plans_billing_cycle_known CHECK (billing_cycle IN ('monthly', 'quarterly', 'semiannually', 'annually')),
  CONSTRAINT plans_currency_is_an_iso_code CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT plans_ip_counts_non_negative CHECK (ipv4_count >= 0 AND ipv6_count >= 0 AND nat_port_count >= 0)
);

CREATE TABLE orders (
  id uuid PRIMARY KEY,
  order_no varchar(64) NOT NULL UNIQUE,
  user_id uuid NOT NULL REFERENCES users(id),
  status varchar(64) NOT NULL,
  subtotal_minor bigint NOT NULL,
  discount_minor bigint NOT NULL DEFAULT 0,
  total_minor bigint NOT NULL,
  currency varchar(3) NOT NULL,
  paid_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  -- docs/05: pending→paid→fulfilling→fulfilled; pending→cancelled;
  -- paid→refund_pending→refunded.
  CONSTRAINT orders_status_known CHECK (
    status IN ('pending', 'paid', 'fulfilling', 'fulfilled', 'cancelled', 'refund_pending', 'refunded')
  ),
  -- The total is derived, so storing it is a denormalisation — and a denormalisation
  -- that can disagree with its inputs is a customer being charged a number nobody
  -- can explain. The constraint makes agreement structural.
  CONSTRAINT orders_total_is_subtotal_less_discount CHECK (
    subtotal_minor >= 0 AND discount_minor >= 0
    AND total_minor = subtotal_minor - discount_minor
    AND total_minor >= 0
  ),
  CONSTRAINT orders_currency_is_an_iso_code CHECK (currency ~ '^[A-Z]{3}$'),
  -- A paid order has a payment time and an unpaid one does not. Both directions are
  -- worth holding: a paid_at without the status is a report that lies, and a status
  -- without paid_at is an order whose settlement cannot be dated.
  CONSTRAINT orders_paid_at_matches_status CHECK (
    (status IN ('paid', 'fulfilling', 'fulfilled', 'refund_pending', 'refunded') AND paid_at IS NOT NULL)
    OR (status IN ('pending', 'cancelled') AND paid_at IS NULL)
  )
);

-- The snapshots are what the customer agreed to; the identifiers are for reporting
-- across products. Both are kept, and neither is ever updated after the row is
-- written (ADR-005).
CREATE TABLE order_items (
  id uuid PRIMARY KEY,
  order_id uuid NOT NULL REFERENCES orders(id),
  product_id uuid NOT NULL REFERENCES products(id),
  plan_id uuid NOT NULL REFERENCES plans(id),
  quantity integer NOT NULL CHECK (quantity > 0),
  unit_price_minor bigint NOT NULL,
  total_minor bigint NOT NULL,
  product_snapshot jsonb NOT NULL,
  plan_snapshot jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT order_items_prices_non_negative CHECK (unit_price_minor >= 0),
  CONSTRAINT order_items_total_is_unit_times_quantity CHECK (total_minor = unit_price_minor * quantity),
  CONSTRAINT order_items_snapshots_are_objects CHECK (
    jsonb_typeof(product_snapshot) = 'object' AND jsonb_typeof(plan_snapshot) = 'object'
  )
);

CREATE TABLE payments (
  id uuid PRIMARY KEY,
  payment_no varchar(64) NOT NULL UNIQUE,
  order_id uuid NOT NULL REFERENCES orders(id),
  gateway varchar(64) NOT NULL,
  gateway_payment_id varchar(255),
  status varchar(64) NOT NULL,
  amount_minor bigint NOT NULL,
  currency varchar(3) NOT NULL,
  idempotency_key varchar(255) NOT NULL UNIQUE,
  gateway_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  paid_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  -- docs/05: pending/processing/succeeded/failed/partially_refunded/refunded.
  CONSTRAINT payments_status_known CHECK (
    status IN ('pending', 'processing', 'succeeded', 'failed', 'partially_refunded', 'refunded')
  ),
  CONSTRAINT payments_amount_positive CHECK (amount_minor > 0),
  CONSTRAINT payments_currency_is_an_iso_code CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT payments_paid_at_matches_status CHECK (
    (status IN ('succeeded', 'partially_refunded', 'refunded') AND paid_at IS NOT NULL)
    OR (status IN ('pending', 'processing', 'failed') AND paid_at IS NULL)
  )
);

-- The reference declares this, and it is the guard that stops one gateway payment
-- from becoming two payment rows. A settlement is gated on a conditional status
-- transition (ADR-005), and this is what makes the invariant hold even if that
-- transition is ever bypassed.
CREATE UNIQUE INDEX ux_payments_gateway_external
ON payments(gateway, gateway_payment_id)
WHERE gateway_payment_id IS NOT NULL;

CREATE INDEX ix_payments_order ON payments(order_id);

CREATE TABLE wallets (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id),
  currency varchar(3) NOT NULL,
  -- A projection of the ledger, maintained in the same transaction as the entries it
  -- summarises and rebuildable from them (ADR-005). It is signed: the entries carry a
  -- magnitude and a direction, so a debit larger than the credits makes this negative
  -- rather than being unrepresentable.
  available_balance_minor bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(user_id, currency),
  CONSTRAINT wallets_currency_is_an_iso_code CHECK (currency ~ '^[A-Z]{3}$')
);

-- Append-only. Nothing in this codebase issues an UPDATE against this table or
-- against ledger_entries: a mistake is corrected by an adjustment transaction, which
-- is why the rows carry no updated_at column — its absence is the statement.
CREATE TABLE ledger_transactions (
  id uuid PRIMARY KEY,
  type varchar(64) NOT NULL,
  reference_type varchar(64),
  reference_id uuid,
  description text,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT ledger_transactions_type_known CHECK (
    type IN ('payment_settlement', 'refund', 'adjustment')
  )
);

-- One transaction per referenced fact, per type. This is the structural half of the
-- idempotency decision (ADR-005): if the conditional status transition ever had a
-- hole, the second settlement would fail here rather than double-crediting. Partial,
-- because a transaction without a reference is legitimate and several of them may
-- share the same nulls.
CREATE UNIQUE INDEX ux_ledger_transactions_reference
ON ledger_transactions(type, reference_type, reference_id)
WHERE reference_id IS NOT NULL;

CREATE TABLE ledger_entries (
  id uuid PRIMARY KEY,
  transaction_id uuid NOT NULL REFERENCES ledger_transactions(id),
  -- The account is identified by a type and an opaque identifier; the reference
  -- declares no accounts table, so a platform account is named by a well-known
  -- constant rather than by a row (ADR-005).
  account_type varchar(64) NOT NULL,
  account_id uuid NOT NULL,
  direction varchar(16) NOT NULL,
  -- A magnitude, never a signed value: the sign is the direction. This is what makes
  -- an unbalanced transaction visible, because the entries of one transaction sum to
  -- zero.
  amount_minor bigint NOT NULL CHECK (amount_minor >= 0),
  currency varchar(3) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT ledger_entries_direction_known CHECK (direction IN ('debit', 'credit')),
  CONSTRAINT ledger_entries_account_type_known CHECK (
    account_type IN ('user_wallet', 'revenue', 'gateway_clearing')
  ),
  CONSTRAINT ledger_entries_currency_is_an_iso_code CHECK (currency ~ '^[A-Z]{3}$')
);

CREATE INDEX ix_ledger_entries_transaction ON ledger_entries(transaction_id);
-- A balance rebuild reads every entry of one account in order.
CREATE INDEX ix_ledger_entries_account ON ledger_entries(account_type, account_id, created_at);

-- Written in the same transaction as the change it describes (docs/09); published by
-- the worker in Phase 5. Consumers deduplicate by event_id, so a redelivery is
-- expected rather than exceptional.
CREATE TABLE outbox_events (
  id uuid PRIMARY KEY,
  -- Versioned, per docs/09: `payment.succeeded.v1`.
  event_type varchar(255) NOT NULL,
  aggregate_type varchar(64) NOT NULL,
  aggregate_id uuid NOT NULL,
  payload jsonb NOT NULL,
  status varchar(64) NOT NULL DEFAULT 'pending',
  attempts integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz,
  CONSTRAINT outbox_events_status_known CHECK (status IN ('pending', 'published', 'failed')),
  CONSTRAINT outbox_events_attempts_non_negative CHECK (attempts >= 0),
  CONSTRAINT outbox_events_published_at_matches_status CHECK (
    (status = 'published' AND published_at IS NOT NULL)
    OR (status IN ('pending', 'failed') AND published_at IS NULL)
  )
);

-- The relay's query is "pending, due, oldest first", so that is the index.
CREATE INDEX ix_outbox_events_pending ON outbox_events(next_attempt_at) WHERE status = 'pending';

-- Created because `invoices.subscription_id` references it. Phase 3 owns the
-- lifecycle, the renewal schedule and the dunning; this phase creates no row here.
CREATE TABLE subscriptions (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id),
  plan_id uuid NOT NULL REFERENCES plans(id),
  status varchar(64) NOT NULL,
  billing_cycle varchar(64) NOT NULL,
  price_minor bigint NOT NULL,
  currency varchar(3) NOT NULL,
  started_at timestamptz,
  current_period_start timestamptz,
  current_period_end timestamptz,
  next_due_at timestamptz,
  grace_until timestamptz,
  cancel_at_period_end boolean NOT NULL DEFAULT false,
  ended_at timestamptz,
  -- Carried from the reference. Phase 3 uses it for optimistic concurrency over the
  -- renewal and cancellation transitions; Phase 2 leaves it at its default.
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  -- The words are docs/05's; the machine is Phase 3's.
  CONSTRAINT subscriptions_status_known CHECK (
    status IN ('pending', 'active', 'past_due', 'suspended', 'cancelled', 'expired', 'terminated')
  ),
  CONSTRAINT subscriptions_price_non_negative CHECK (price_minor >= 0),
  CONSTRAINT subscriptions_currency_is_an_iso_code CHECK (currency ~ '^[A-Z]{3}$')
);

CREATE INDEX ix_subscriptions_user ON subscriptions(user_id);

-- `subscription_id` and `order_id` are both nullable and both kept: an invoice can
-- arise from an order (Phase 2) or from a renewal period (Phase 3), and it records
-- which. At least one has to be present, because an invoice with no origin is a
-- charge nobody can explain.
CREATE TABLE invoices (
  id uuid PRIMARY KEY,
  invoice_no varchar(64) NOT NULL UNIQUE,
  user_id uuid NOT NULL REFERENCES users(id),
  subscription_id uuid REFERENCES subscriptions(id),
  order_id uuid REFERENCES orders(id),
  status varchar(64) NOT NULL,
  amount_minor bigint NOT NULL,
  currency varchar(3) NOT NULL,
  due_at timestamptz,
  paid_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  -- docs/05 defines no invoice machine, so this is this repository's vocabulary
  -- (ADR-005). The values are constrained, which means extending it is a migration
  -- rather than a typo that nothing handles.
  CONSTRAINT invoices_status_known CHECK (status IN ('open', 'paid', 'void')),
  CONSTRAINT invoices_amount_non_negative CHECK (amount_minor >= 0),
  CONSTRAINT invoices_currency_is_an_iso_code CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT invoices_has_an_origin CHECK (subscription_id IS NOT NULL OR order_id IS NOT NULL),
  CONSTRAINT invoices_paid_at_matches_status CHECK (
    (status = 'paid' AND paid_at IS NOT NULL)
    OR (status IN ('open', 'void') AND paid_at IS NULL)
  )
);

CREATE INDEX ix_invoices_user ON invoices(user_id);
CREATE INDEX ix_invoices_order ON invoices(order_id);

CREATE TABLE invoice_items (
  id uuid PRIMARY KEY,
  invoice_id uuid NOT NULL REFERENCES invoices(id),
  description_i18n jsonb NOT NULL,
  quantity integer NOT NULL CHECK (quantity > 0),
  unit_amount_minor bigint NOT NULL,
  total_minor bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT invoice_items_amounts_non_negative CHECK (unit_amount_minor >= 0),
  CONSTRAINT invoice_items_total_is_unit_times_quantity CHECK (total_minor = unit_amount_minor * quantity),
  CONSTRAINT invoice_items_description_is_an_object CHECK (jsonb_typeof(description_i18n) = 'object')
);

CREATE INDEX ix_invoice_items_invoice ON invoice_items(invoice_id);
