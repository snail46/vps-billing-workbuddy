-- The commercial queries.
--
-- Every statement is written to be safe to run inside the transaction the settlement
-- uses, and the two state transitions are conditional updates: the WHERE clause names
-- the state the row must be in, so exactly one caller of a hundred can see a row
-- returned. That is the property the Gate is about, and it is a property of the
-- statement rather than of the code around it.

-- ---------------------------------------------------------------- catalogue --

-- name: ListActiveProducts :many
SELECT id, slug, name_i18n, description_i18n, status, sort_order, created_at, updated_at
FROM products
WHERE status = 'active'
ORDER BY sort_order, slug;

-- name: ListActivePlans :many
SELECT id, product_id, node_group_id, slug, name_i18n, status, memory_mb, disk_gb,
       traffic_gb, bandwidth_mbps, ipv4_count, ipv6_count, nat_port_count, virtualization,
       billing_cycle, price_minor, currency, stock_mode, created_at, updated_at,
       cpu_cores::text AS cpu_cores_text
FROM plans
WHERE status = 'active'
ORDER BY price_minor, slug;

-- name: PlanByID :one
SELECT id, product_id, node_group_id, slug, name_i18n, status, memory_mb, disk_gb,
       traffic_gb, bandwidth_mbps, ipv4_count, ipv6_count, nat_port_count, virtualization,
       billing_cycle, price_minor, currency, stock_mode, created_at, updated_at,
       cpu_cores::text AS cpu_cores_text
FROM plans
WHERE id = $1;

-- ------------------------------------------------------------------ orders --

-- name: CreateOrder :exec
INSERT INTO orders (
  id, order_no, user_id, status,
  subtotal_minor, discount_minor, total_minor, currency, paid_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: CreateOrderItem :exec
INSERT INTO order_items (
  id, order_id, product_id, plan_id, quantity,
  unit_price_minor, total_minor, product_snapshot, plan_snapshot
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: OrderByIDForUser :one
SELECT id, order_no, user_id, status, subtotal_minor, discount_minor, total_minor,
       currency, paid_at, created_at, updated_at
FROM orders
WHERE id = $1 AND user_id = $2;

-- name: ListOrdersForUser :many
SELECT id, order_no, user_id, status, subtotal_minor, discount_minor, total_minor,
       currency, paid_at, created_at, updated_at
FROM orders
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT 100;

-- name: OrderItemsForOrder :many
SELECT id, order_id, product_id, plan_id, quantity, unit_price_minor, total_minor,
       product_snapshot, plan_snapshot, created_at
FROM order_items
WHERE order_id = $1
ORDER BY created_at, id;

-- The condition names the only state a payment can settle from. A row that is not
-- pending is either already paid — a redelivered callback — or cancelled, and neither
-- may be moved by a second writer.
-- name: PayOrder :execrows
UPDATE orders
SET status = 'paid', paid_at = $2, updated_at = $2
WHERE id = $1 AND status = 'pending';

-- ---------------------------------------------------------------- invoices --

-- An invoice is opened with the order, so it can say what was asked for before it was
-- paid. `subscription_id` is left null: this phase's invoices arise from orders, and the
-- renewal invoices of Phase 3 will set it instead.
-- name: CreateInvoice :exec
INSERT INTO invoices (
  id, invoice_no, user_id, subscription_id, order_id, status, amount_minor, currency, due_at, paid_at
) VALUES ($1, $2, $3, NULL, $4, $5, $6, $7, NULL, NULL);

-- name: MarkInvoicePaid :execrows
UPDATE invoices
SET status = 'paid', paid_at = $2, updated_at = $2
WHERE order_id = $1 AND status = 'open';

-- name: CreateInvoiceItem :exec
INSERT INTO invoice_items (id, invoice_id, description_i18n, quantity, unit_amount_minor, total_minor)
VALUES ($1, $2, $3, $4, $5, $6);

-- ---------------------------------------------------------------- payments --

-- name: CreatePayment :exec
INSERT INTO payments (
  id, payment_no, order_id, gateway, gateway_payment_id, status,
  amount_minor, currency, idempotency_key, gateway_payload, paid_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '{}'::jsonb, NULL);

-- name: PaymentByGatewayRef :one
SELECT id, payment_no, order_id, gateway, gateway_payment_id, status, amount_minor,
       currency, idempotency_key, gateway_payload, paid_at, created_at, updated_at
FROM payments
WHERE gateway = $1 AND gateway_payment_id = $2;

-- The whole idempotency decision is this statement. It is one UPDATE with the current
-- state named in the WHERE clause, so under a hundred concurrent deliveries of the same
-- callback exactly one of them returns a row and the other ninety-nine see none.
-- name: SettlePayment :one
UPDATE payments
SET status = 'succeeded', paid_at = $2, updated_at = $2
WHERE id = $1 AND status IN ('pending', 'processing')
RETURNING id, payment_no, order_id, gateway, gateway_payment_id, status, amount_minor,
          currency, idempotency_key, gateway_payload, paid_at, created_at, updated_at;

-- name: FailPayment :one
UPDATE payments
SET status = 'failed', updated_at = $2
WHERE id = $1 AND status IN ('pending', 'processing')
RETURNING id, payment_no, order_id, gateway, gateway_payment_id, status, amount_minor,
          currency, idempotency_key, gateway_payload, paid_at, created_at, updated_at;

-- Merged into what the gateway sent, rather than replacing it: the payload accumulates
-- across the callbacks of one payment, which is what a dispute needs.
-- name: AppendPaymentPayload :exec
UPDATE payments
SET gateway_payload = gateway_payload || $2::jsonb, updated_at = now()
WHERE id = $1;

-- ------------------------------------------------------------------ ledger --

-- name: CreateLedgerTransaction :exec
INSERT INTO ledger_transactions (id, type, reference_type, reference_id, description)
VALUES ($1, $2, $3, $4, $5);

-- name: CreateLedgerEntry :exec
INSERT INTO ledger_entries (id, transaction_id, account_type, account_id, direction, amount_minor, currency)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- The credit-minus-debit total of one account. This is the derivation the wallet
-- projection is checked against (ADR-005): it reads the entries and nothing else, so it
-- is the answer to "is the projection right".
-- name: SumLedgerAccount :one
SELECT COALESCE(
         SUM(CASE WHEN direction = 'credit' THEN amount_minor ELSE - amount_minor END),
         0
       )::bigint AS balance
FROM ledger_entries
WHERE account_type = $1 AND account_id = $2 AND currency = $3;

-- The projection. Applied in the same transaction as the entries it summarises, and
-- always as a delta rather than an absolute value, because an absolute write would
-- silently overwrite whatever a concurrent posting had already added.
-- name: UpsertWalletBalance :exec
INSERT INTO wallets (id, user_id, currency, available_balance_minor)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, currency) DO UPDATE
SET available_balance_minor = wallets.available_balance_minor + $4,
    updated_at = now();

-- ------------------------------------------------------------------- outbox --

-- Written in the same transaction as the change it describes (docs/09). Publishing is
-- the worker's, in Phase 5.
-- name: CreateOutboxEvent :exec
INSERT INTO outbox_events (id, event_type, aggregate_type, aggregate_id, payload)
VALUES ($1, $2, $3, $4, $5);

-- The delivery side, owned by the worker (ADR-005): claim what is due under
-- SKIP LOCKED, so N workers share the batch rather than racing over it.
-- name: ClaimDueOutboxEvents :many
UPDATE outbox_events
SET status = 'pending', attempts = attempts + 1, next_attempt_at = $1
WHERE id IN (
  SELECT o.id FROM outbox_events o
  WHERE o.status = 'pending' AND o.next_attempt_at <= $1
    AND o.event_type = ANY($2::text[])
  ORDER BY o.created_at
  FOR UPDATE SKIP LOCKED
  LIMIT 32
)
RETURNING id, event_type, aggregate_type, aggregate_id, payload, attempts, created_at;

-- A delivered event leaves the queue for good; consumer dedup is by event_id.
-- name: MarkOutboxPublished :execrows
UPDATE outbox_events
SET status = 'published', published_at = $2
WHERE id = $1;

-- A failed delivery retries on the same backoff the operations use.
-- name: MarkOutboxFailed :execrows
UPDATE outbox_events
SET status = 'pending', next_attempt_at = $2
WHERE id = $1 AND status = 'pending';
