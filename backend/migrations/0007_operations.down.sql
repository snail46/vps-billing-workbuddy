-- 0007_operations (down) — drops the operation system's tables.

DROP INDEX IF EXISTS ix_resource_reservations_expiry;
DROP INDEX IF EXISTS ix_resource_reservations_node;
DROP INDEX IF EXISTS ux_resource_reservations_open;
DROP TABLE IF EXISTS resource_reservations;
DROP INDEX IF EXISTS ix_operations_resource;
DROP INDEX IF EXISTS ix_operations_status;
DROP TABLE IF EXISTS operation_steps;
DROP TABLE IF EXISTS operations;
