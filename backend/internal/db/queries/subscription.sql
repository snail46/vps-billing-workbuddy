-- The subscription queries.
--
-- The machine's writers are conditional updates for the same reason the payment
-- settlement is (ADR-005/006): repetition is the normal case for a sweep, and a
-- repetition that reads-then-writes is a repetition that doubles.

-- ------------------------------------------------------------------- reads --

-- The settlement needs the order's lines to know what it activated. Read without
-- the user scope: the settlement is the platform acting on its own record, not a
-- customer asking for it.
-- name: OrderByIDForSettlement :one
SELECT id, order_no, user_id, status, subtotal_minor, discount_minor, total_minor,
       currency, paid_at, created_at, updated_at
FROM orders
WHERE id = $1;

-- Read back the open renewal invoice a lost race left open (ADR-006 §6): both
-- sweeps converge on the same bill, so both attempt the same payment.
-- name: OpenInvoiceForSubscription :one
SELECT id, invoice_no, user_id, subscription_id, order_id, status, amount_minor,
       currency, due_at, paid_at, created_at, updated_at
FROM invoices
WHERE subscription_id = $1 AND status = 'open';

-- name: SubscriptionByID :one
SELECT id, user_id, plan_id, status, billing_cycle, price_minor, currency,
       started_at, current_period_start, current_period_end, next_due_at,
       grace_until, cancel_at_period_end, ended_at, version, created_at, updated_at
FROM subscriptions
WHERE id = $1;

-- name: SubscriptionByIDForUser :one
SELECT id, user_id, plan_id, status, billing_cycle, price_minor, currency,
       started_at, current_period_start, current_period_end, next_due_at,
       grace_until, cancel_at_period_end, ended_at, version, created_at, updated_at
FROM subscriptions
WHERE id = $1 AND user_id = $2;

-- name: ListSubscriptionsForUser :many
SELECT id, user_id, plan_id, status, billing_cycle, price_minor, currency,
       started_at, current_period_start, current_period_end, next_due_at,
       grace_until, cancel_at_period_end, ended_at, version, created_at, updated_at
FROM subscriptions
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT 100;

-- The sweep's work list: everything alive whose calendar deadline has passed.
-- For `active`/`past_due` rows the deadline is the period's end; for `suspended`
-- rows `grace_until` carries the state's own deadline — the instant by which the
-- current state must be resolved — which is the period's grace while past_due and
-- the expiry deadline once suspended. One column, one meaning: the resolution
-- deadline of the state the row is in.
-- The plan join is for the renewal invoice's line: the bill speaks in the plan's
-- own words, as every invoice line does.
-- name: SubscriptionsPastDeadline :many
SELECT s.id, s.user_id, s.plan_id, s.status, s.billing_cycle, s.price_minor, s.currency,
       s.started_at, s.current_period_start, s.current_period_end, s.next_due_at,
       s.grace_until, s.cancel_at_period_end, s.ended_at, s.version, s.created_at, s.updated_at,
       p.slug AS plan_slug, p.name_i18n AS plan_name_i18n
FROM subscriptions s
JOIN plans p ON p.id = s.plan_id
WHERE (s.status IN ('active', 'past_due') AND s.current_period_end <= $1)
   OR (s.status = 'suspended' AND s.grace_until <= $1);

-- ------------------------------------------------------------------ writes --

-- name: CreateSubscription :exec
INSERT INTO subscriptions (
  id, user_id, plan_id, status, billing_cycle, price_minor, currency,
  started_at, current_period_start, current_period_end, next_due_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- The conditional transition: the row moves only from the state the caller saw.
-- A zero-row result means another writer moved it first, which is the sweep
-- racing itself and losing — the answer, not a failure.
-- Two of the targets are calendar-bound, and the state alone cannot vouch for
-- them: a sweep holding a stale read of an `active` row must not mark it past
-- due — or cancel it — if a concurrent renewal has already pushed the period
-- into the future. So `past_due` and `cancelled` additionally require the period
-- to have actually ended, read from the row as it is now, not as it was read.
-- The cancellation flag is part of the state it belongs to: reaching `cancelled`
-- is the flag being honoured, so it is cleared with the row; every other target
-- leaves the customer's request exactly as they made it.
-- name: TransitionSubscription :execrows
UPDATE subscriptions
SET status = $2, grace_until = $3, ended_at = $4,
    cancel_at_period_end = CASE WHEN $2 = 'cancelled' THEN FALSE
                                ELSE subscriptions.cancel_at_period_end END,
    version = version + 1, updated_at = $5
WHERE id = $1 AND status = $6
  AND ($2 NOT IN ('past_due', 'cancelled') OR current_period_end <= $5);

-- The renewal: one period of service traded for one payment of money. It runs
-- from any of the three live states, because paying during grace (past_due) and
-- paying after suspension are both recovery, and docs/07's renew chain ends in
-- "extend subscription" for both. `grace_until` is cleared: an active row has
-- nothing to resolve.
-- name: ExtendSubscription :execrows
UPDATE subscriptions
SET status = 'active', current_period_start = $2, current_period_end = $3,
    next_due_at = $3, grace_until = NULL, version = version + 1, updated_at = $4
WHERE id = $1 AND status IN ('active', 'past_due', 'suspended');

-- name: SetCancelAtPeriodEnd :execrows
UPDATE subscriptions
SET cancel_at_period_end = TRUE, version = version + 1, updated_at = $3
WHERE id = $1 AND user_id = $2 AND status IN ('active', 'past_due');

-- The invoice a renewal is paid against. Born `open`, moved to `paid` by the one
-- caller the conditional update below lets through.
-- name: CreateSubscriptionInvoice :exec
INSERT INTO invoices (
  id, invoice_no, user_id, subscription_id, order_id, status, amount_minor, currency, due_at, paid_at
) VALUES ($1, $2, $3, $4, NULL, $5, $6, $7, $8, NULL);

-- name: CreateSubscriptionInvoiceItem :exec
INSERT INTO invoice_items (id, invoice_id, description_i18n, quantity, unit_amount_minor, total_minor)
VALUES ($1, $2, $3, $4, $5, $6);

-- The renewal's settlement is the gate: exactly one caller moves the open invoice
-- to `paid`, and that caller — and only that caller — extends the period and
-- writes the ledger movement in the same transaction.
-- name: MarkSubscriptionInvoicePaid :execrows
UPDATE invoices
SET status = 'paid', paid_at = $2, updated_at = $2
WHERE id = $1 AND status = 'open';

-- The wallet spend's gate. The row is locked, so two concurrent renewals
-- serialize here: the second reads the balance the first left, and a wallet that
-- backs one renewal cannot back two. The gate only READS and locks — the
-- projection is moved once, by the ledger entries the poster writes in the same
-- transaction (ADR-005): gating by decrementing here would move it twice.
-- name: LockWalletForSpend :one
SELECT id, user_id, currency, available_balance_minor
FROM wallets
WHERE user_id = $1 AND currency = $2
FOR UPDATE;
