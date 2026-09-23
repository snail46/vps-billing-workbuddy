-- 0003_identity_rbac_seed (down) — removes the seeded roles and permissions.
--
-- Only the rows this migration owns are deleted: the roles and permissions that
-- were inserted above, matched by their natural keys, and the grants that
-- reference them.
--
-- Deleting a role that an admin still holds fails on the foreign key from
-- `admin_roles`, and that failure is the correct outcome: it means the role is in
-- use, and silently removing it would leave an administrator with fewer rights
-- than the rollback intended. An operator who genuinely wants to undo this
-- migration must first detach the role.
--
-- Permissions are removed after the grants, for the same reason: a permission
-- still referenced by `role_permissions` must not disappear underneath it.

DELETE FROM role_permissions
WHERE role_id IN (
  SELECT id FROM roles
  WHERE key IN ('super_admin', 'operations', 'finance', 'support', 'read_only')
);

DELETE FROM roles
WHERE key IN ('super_admin', 'operations', 'finance', 'support', 'read_only');

DELETE FROM permissions
WHERE key IN (
  'users.read', 'users.update', 'users.suspend',
  'instances.read', 'instances.start', 'instances.stop', 'instances.restart',
  'instances.reinstall', 'instances.delete',
  'payments.read', 'payments.refund',
  'ledger.read', 'ledger.adjust',
  'nodes.read', 'nodes.update', 'nodes.delete',
  'providers.read', 'providers.manage',
  'operations.read', 'operations.retry',
  'tickets.read', 'tickets.reply', 'tickets.manage',
  'audit.read',
  'admins.manage', 'roles.manage', 'settings.manage'
);
