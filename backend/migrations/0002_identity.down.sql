-- 0002_identity (down) — reverses the identity and audit tables.
--
-- Dropped in reverse dependency order: the join tables reference `admins` and
-- `roles`, and `roles`/`permissions` are referenced by them.
--
-- Dropping `audit_events` necessarily discards audit history. That is why this
-- script exists at all rather than being omitted: the CI integration job applies
-- the whole history forward and rolls it back, and a migration without a down
-- script makes that check impossible to run. The loss is acceptable only because
-- the rollback happens on a database created moments earlier by the same job.
-- Rolling back a production database past this migration loses the audit trail,
-- which is a deliberate act rather than a routine one.

DROP TABLE audit_events;
DROP TABLE role_permissions;
DROP TABLE admin_roles;
DROP TABLE permissions;
DROP TABLE roles;
DROP TABLE admins;
DROP TABLE users;
