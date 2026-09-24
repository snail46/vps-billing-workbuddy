-- 0006_infrastructure (down) — drops what this phase created.

DROP INDEX IF EXISTS ix_nodes_group_status;
DROP TABLE IF EXISTS nodes;
DROP TABLE IF EXISTS providers;
ALTER TABLE node_groups DROP CONSTRAINT IF EXISTS node_groups_status_known;
