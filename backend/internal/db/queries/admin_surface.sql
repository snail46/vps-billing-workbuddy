-- The operator's surface (ADR-012): lists, details and the dashboard's
-- computed aggregates. Every statement is a read behind a declared permission;
-- the few writes are conditional updates that name the state they move.

-- ------------------------------------------------------------------ overview --

-- name: AdminOpenFailedOperations :one
SELECT count(*) FROM operations WHERE status IN ('failed', 'retrying');

-- name: AdminRunningOperations :one
SELECT count(*) FROM operations WHERE status IN ('queued', 'running', 'waiting_provider', 'waiting_resource', 'verifying');

-- name: AdminNodesOffline :one
SELECT count(*) FROM nodes WHERE status <> 'online';

-- name: AdminActiveUsers :one
SELECT count(*) FROM users WHERE status = 'active';

-- name: AdminSuspendedUsers :one
SELECT count(*) FROM users WHERE status = 'suspended';

-- name: AdminCapacityWarnings :one
SELECT count(*)
FROM nodes n
JOIN node_groups g ON g.id = n.node_group_id
WHERE n.status = 'online'
  AND g.cpu_cores_total > 0
  AND (g.cpu_cores_total - n.cpu_cores_available) * 100.0 / g.cpu_cores_total >= 80.0;

-- name: AdminRevenueTotal :one
SELECT COALESCE(sum(amount_minor), 0)::bigint FROM payments WHERE status = 'succeeded';

-- name: AdminPaymentsByStatus :many
SELECT status, count(*) AS count FROM payments GROUP BY status;

-- ---------------------------------------------------------------------- users --

-- name: AdminListUsers :many
SELECT id, email, status, locale, created_at FROM users ORDER BY created_at DESC LIMIT 100;

-- name: AdminUserByID :one
SELECT id, email, status, locale, timezone, created_at FROM users WHERE id = $1;

-- name: AdminSetUserStatus :execrows
UPDATE users SET status = $2, updated_at = $3 WHERE id = $1 AND status <> $2;

-- name: AdminUserWallets :many
SELECT id, currency, available_balance_minor FROM wallets WHERE user_id = $1 ORDER BY currency;

-- ------------------------------------------------------------------- products --

-- name: AdminListProducts :many
SELECT * FROM products ORDER BY sort_order, slug;

-- name: AdminListPlans :many
SELECT id, product_id, slug, status, memory_mb, disk_gb, traffic_gb, bandwidth_mbps,
       ipv4_count, ipv6_count, nat_port_count, virtualization, billing_cycle,
       price_minor, currency,
       cpu_cores::text AS cpu_cores_text
FROM plans ORDER BY price_minor, slug;

-- --------------------------------------------------------------------- orders --

-- name: AdminListOrders :many
SELECT o.id, o.order_no, o.user_id, u.email AS user_email, o.status,
       o.subtotal_minor, o.discount_minor, o.total_minor, o.currency, o.paid_at, o.created_at
FROM orders o
JOIN users u ON u.id = o.user_id
ORDER BY o.created_at DESC
LIMIT 100;

-- name: AdminOrderByID :one
SELECT o.*, u.email AS user_email
FROM orders o
JOIN users u ON u.id = o.user_id
WHERE o.id = $1;

-- name: AdminOrderItems :many
SELECT * FROM order_items WHERE order_id = $1 ORDER BY created_at;

-- name: AdminOrderPayments :many
SELECT id, payment_no, gateway, status, amount_minor, currency, created_at
FROM payments WHERE order_id = $1 ORDER BY created_at;

-- name: AdminListPayments :many
SELECT p.id, p.payment_no, p.order_id, p.gateway, p.status, p.amount_minor, p.currency,
       p.created_at, o.order_no
FROM payments p
JOIN orders o ON o.id = p.order_id
ORDER BY p.created_at DESC
LIMIT 100;

-- --------------------------------------------------------------------- ledger --

-- name: AdminLedgerMovements :many
SELECT lt.type AS transaction_type, lt.reference_type, lt.reference_id,
       lt.description, le.account_type, le.account_id, le.direction,
       le.amount_minor, le.currency, le.created_at
FROM ledger_entries le
JOIN ledger_transactions lt ON lt.id = le.transaction_id
ORDER BY le.created_at DESC
LIMIT 100;

-- name: AdminWalletBalances :many
SELECT w.id, w.user_id, w.currency, w.available_balance_minor
FROM wallets w
ORDER BY w.user_id, w.currency
LIMIT 100;

-- -------------------------------------------------------------- subscriptions --

-- name: AdminListSubscriptions :many
SELECT s.id, s.user_id, u.email AS user_email, s.status, pl.name_i18n AS plan_name_i18n,
       s.billing_cycle, s.price_minor, s.currency, s.current_period_start, s.current_period_end, s.created_at
FROM subscriptions s
JOIN users u ON u.id = s.user_id
JOIN plans pl ON pl.id = s.plan_id
ORDER BY s.created_at DESC
LIMIT 100;

-- ------------------------------------------------------------------ instances --

-- name: AdminListInstances :many
SELECT i.id, i.name, i.desired_state, i.observed_state, i.cpu_cores::text AS cpu_cores_text,
       i.memory_mb, i.disk_gb, i.provider_instance_id, i.last_synced_at, i.created_at,
       u.email AS user_email, n.name AS node_name, p.name AS provider_name
FROM instances i
JOIN subscriptions s ON s.id = i.subscription_id
JOIN users u ON u.id = s.user_id
LEFT JOIN nodes n ON n.id = i.node_id
LEFT JOIN providers p ON p.id = i.provider_id
WHERE i.deleted_at IS NULL
ORDER BY i.created_at DESC
LIMIT 100;

-- name: AdminInstanceDetail :one
SELECT i.*, u.email AS user_email, s.status AS subscription_status,
       pl.name_i18n AS plan_name_i18n, pl.virtualization AS plan_virtualization,
       n.name AS node_name, p.name AS provider_name
FROM instances i
JOIN subscriptions s ON s.id = i.subscription_id
JOIN users u ON u.id = s.user_id
JOIN plans pl ON pl.id = s.plan_id
LEFT JOIN nodes n ON n.id = i.node_id
LEFT JOIN providers p ON p.id = i.provider_id
WHERE i.id = $1 AND i.deleted_at IS NULL;

-- name: AdminOperationsByResource :many
SELECT * FROM operations
WHERE resource_type = $1 AND resource_id = $2
ORDER BY created_at DESC
LIMIT 50;

-- name: AdminAuditByResource :many
SELECT id, actor_type, actor_id, action, resource_type, resource_id, created_at
FROM audit_events
WHERE resource_type = $1 AND resource_id = $2
ORDER BY created_at DESC
LIMIT 50;

-- -------------------------------------------------------------------- tickets --

-- name: AdminListTickets :many
SELECT t.id, t.ticket_no, t.subject, t.status, t.priority, t.created_at, t.updated_at,
       u.email AS user_email
FROM tickets t
JOIN users u ON u.id = t.user_id
ORDER BY t.updated_at DESC
LIMIT 100;

-- name: AdminTicketByID :one
SELECT t.*, u.email AS user_email
FROM tickets t
JOIN users u ON u.id = t.user_id
WHERE t.id = $1;

-- name: AdminTicketReplyStamp :execrows
UPDATE tickets
SET status = 'answered', updated_at = $2
WHERE id = $1 AND status <> 'closed';

-- name: AdminTicketClose :execrows
UPDATE tickets
SET status = 'closed', closed_at = $2, updated_at = $2
WHERE id = $1 AND status <> 'closed';

-- ---------------------------------------------------------------------- audit --

-- name: AdminAuditRecent :many
SELECT id, actor_type, actor_id, action, resource_type, resource_id, created_at
FROM audit_events
ORDER BY created_at DESC
LIMIT 100;

-- ------------------------------------------------------- admins/roles/settings --

-- name: AdminListAdmins :many
SELECT a.id, a.email, a.status, a.display_name, a.two_factor_enabled, a.created_at
FROM admins a
ORDER BY a.created_at DESC;

-- name: AdminRolesByAdmin :many
SELECT r.id, r.key, ar.admin_id
FROM admin_roles ar
JOIN roles r ON r.id = ar.role_id;

-- name: AdminListRoles :many
SELECT id, key, name_key FROM roles ORDER BY key;

-- name: AdminPermissionsByRole :many
SELECT rp.role_id, p.key
FROM role_permissions rp
JOIN permissions p ON p.id = rp.permission_id;

-- name: AdminListSettings :many
SELECT * FROM system_settings ORDER BY key;

-- name: AdminListOperations :many
SELECT * FROM operations ORDER BY created_at DESC LIMIT 100;
