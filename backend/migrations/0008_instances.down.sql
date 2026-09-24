-- 0008_instances (down) — drops the vertical slice's tables.

DROP INDEX IF EXISTS ix_notifications_user;
DROP TABLE IF EXISTS notifications;
DROP INDEX IF EXISTS ix_instances_observed;
DROP INDEX IF EXISTS ix_instances_subscription;
DROP TABLE IF EXISTS instances;
