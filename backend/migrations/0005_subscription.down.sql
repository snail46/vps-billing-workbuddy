-- 0005_subscription (down) — removes the indexes and permissions this phase added.
--
-- The subscriptions table itself is 0004's; this migration only ever added indexes
-- and permission rows, so rolling back is dropping exactly those.

DROP INDEX IF EXISTS ux_invoices_open_renewal;
DROP INDEX IF EXISTS ix_subscriptions_sweep;

DELETE FROM role_permissions
WHERE permission_id IN (
  SELECT id FROM permissions WHERE key IN ('subscriptions.read', 'subscriptions.terminate')
);

DELETE FROM permissions
WHERE key IN ('subscriptions.read', 'subscriptions.terminate');
