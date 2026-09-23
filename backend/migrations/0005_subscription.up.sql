-- 0005_subscription — the indexes and permissions the subscription machine needs.
--
-- The tables already exist: 0004 created `subscriptions` as a foreign-key target
-- before this phase owned its behaviour, and that is the ordering the roadmap
-- chose. What this phase adds is what its machine enforces.

-- The sweep reads subscriptions by status and period end. Without this index a
-- sweep is a scan of every subscription for every tick, forever.
CREATE INDEX ix_subscriptions_sweep
  ON subscriptions (status, current_period_end)
  WHERE current_period_end IS NOT NULL;

-- One open renewal invoice per subscription (ADR-006 §6). Two concurrent sweeps
-- can both try to open the bill for a period that has ended; one commits, and
-- this index is what the other reports. `status = 'open'` keeps the index to the
-- single live invoice, so history stays index-free.
CREATE UNIQUE INDEX ux_invoices_open_renewal
  ON invoices (subscription_id)
  WHERE subscription_id IS NOT NULL AND status = 'open';

-- The administrator's hand on the machine. `subscriptions.read` follows the
-- naming convention docs/15 uses for read permissions, and both are granted to
-- super_admin here the same way 0003 grants its own: explicitly, because the
-- cross join in 0003 runs once, at that migration, and sees only its own keys.
INSERT INTO permissions (id, key) VALUES
  (gen_random_uuid(), 'subscriptions.read'),
  (gen_random_uuid(), 'subscriptions.terminate')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.key IN ('subscriptions.read', 'subscriptions.terminate')
WHERE r.key = 'super_admin'
ON CONFLICT DO NOTHING;
