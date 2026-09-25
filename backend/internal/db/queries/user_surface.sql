-- The customer's own surface (ADR-011): reads whose ownership is the join
-- itself, plus the ticket writes the support page needs. A query that cannot
-- name the caller's user id in its WHERE clause does not belong in this file.

-- ------------------------------------------------------------------ instances --

-- The instance detail: one row, the caller's own, or none at all. The join is
-- the authorization; there is no second fetch-and-compare step.
-- name: InstanceByIDForUser :one
SELECT i.*
FROM instances i
JOIN subscriptions s ON s.id = i.subscription_id
WHERE i.id = $1 AND s.user_id = $2 AND i.deleted_at IS NULL;

-- name: InstanceNetworksByInstance :many
SELECT * FROM instance_networks WHERE instance_id = $1 ORDER BY created_at;

-- name: PortForwardsByInstance :many
SELECT * FROM port_forwards WHERE instance_id = $1 ORDER BY created_at;

-- name: TrafficByInstance :many
SELECT * FROM traffic_usage WHERE instance_id = $1 ORDER BY period_start DESC LIMIT 12;

-- The state an action asks for. The conditional UPDATE is the same idiom the
-- commerce transitions use: the WHERE names the state the row must be in.
-- name: SetInstanceDesiredState :execrows
UPDATE instances
SET desired_state = $2, version = version + 1, updated_at = $3
WHERE id = $1 AND desired_state <> $2 AND deleted_at IS NULL;

-- name: SetInstanceImage :execrows
UPDATE instances
SET image_id = $2, version = version + 1, updated_at = $3
WHERE id = $1 AND deleted_at IS NULL;

-- The user id behind an instance, for the operation ownership check.
-- name: InstanceOwner :one
SELECT s.user_id AS owner_id
FROM instances i
JOIN subscriptions s ON s.id = i.subscription_id
WHERE i.id = $1 AND i.deleted_at IS NULL;

-- --------------------------------------------------------------------- wallet --

-- The projection: what the customer can spend, per currency.
-- name: WalletsForUser :many
SELECT * FROM wallets WHERE user_id = $1 ORDER BY currency;

-- The truth behind the projection: the caller's wallet accounts' entries,
-- joined to their transactions for the description. Append-only, so newest
-- first is all the ordering a page needs.
-- name: WalletLedgerForUser :many
SELECT lt.type AS transaction_type, lt.reference_type, lt.reference_id,
       lt.description, le.direction, le.amount_minor, le.currency, le.created_at
FROM ledger_entries le
JOIN wallets w ON w.id = le.account_id
JOIN ledger_transactions lt ON lt.id = le.transaction_id
WHERE w.user_id = $1
ORDER BY le.created_at DESC
LIMIT 50;

-- ------------------------------------------------------------------- invoices --

-- name: InvoicesForUser :many
SELECT * FROM invoices WHERE user_id = $1 ORDER BY created_at DESC LIMIT 100;

-- name: InvoiceByIDForUser :one
SELECT * FROM invoices WHERE id = $1 AND user_id = $2;

-- name: InvoiceItemsByInvoice :many
SELECT * FROM invoice_items WHERE invoice_id = $1 ORDER BY created_at;

-- -------------------------------------------------------------- notifications --

-- name: NotificationsForUser :many
SELECT * FROM notifications WHERE user_id = $1 ORDER BY created_at DESC LIMIT 50;

-- name: UnreadNotificationCountForUser :one
SELECT count(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL;

-- name: MarkNotificationRead :execrows
UPDATE notifications SET read_at = $2 WHERE id = $1 AND user_id = $3 AND read_at IS NULL;

-- -------------------------------------------------------------------- tickets --

-- name: CreateTicket :exec
INSERT INTO tickets (id, ticket_no, user_id, subject, status, priority)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: CreateTicketMessage :exec
INSERT INTO ticket_messages (id, ticket_id, sender_type, sender_id, message)
VALUES ($1, $2, $3, $4, $5);

-- name: TicketsForUser :many
SELECT * FROM tickets WHERE user_id = $1 ORDER BY created_at DESC LIMIT 100;

-- The join is the authorization, as everywhere in this file.
-- name: TicketByIDForUser :one
SELECT * FROM tickets WHERE id = $1 AND user_id = $2;

-- name: TicketMessagesByTicket :many
SELECT * FROM ticket_messages WHERE ticket_id = $1 ORDER BY created_at;

-- A reply marks the conversation alive again; the owner's reply moves it out
-- of answered, because answered means "waiting for the customer".
-- name: TouchTicketOnMessage :execrows
UPDATE tickets
SET status = CASE WHEN $2 = 'user' AND status = 'answered' THEN 'open' ELSE status END,
    updated_at = $3
WHERE id = $1 AND status <> 'closed';

-- name: CloseTicket :execrows
UPDATE tickets
SET status = 'closed', closed_at = $3, updated_at = $3
WHERE id = $1 AND user_id = $2 AND status <> 'closed';

-- ------------------------------------------------- operation ownership joins --

-- The user id behind a subscription-typed operation.
-- name: SubscriptionOwner :one
SELECT user_id AS owner_id FROM subscriptions WHERE id = $1;
