-- 0003_identity_rbac_seed — the default roles and permissions.
--
-- `docs/15` fixes the role set and the permission keys but does not say which role
-- holds which permission, so the mapping below is a stated default rather than a
-- transcription. It is intentionally explicit and reviewable, and it is data: a
-- deployment that needs different entitlements changes rows, not code.
--
-- The mapping:
--
--   super_admin  every permission
--   operations   the infrastructure and support surface: instances, nodes,
--                providers, operations, and the ability to read users and answer
--                tickets. It cannot touch money.
--   finance      payments and ledger, plus read access to users, instances and
--                operations for context. It cannot provision or delete.
--   support      users, tickets and read access to instances and operations. It
--                can update a user but cannot suspend one, and cannot touch money
--                or infrastructure.
--   read_only    every permission ending in `.read`, derived rather than listed so
--                a permission added later is not silently missing from the
--                read-only role.
--
-- `admins.manage`, `roles.manage` and `settings.manage` are held only by
-- super_admin: they are the permissions that can change who holds permissions, so
-- no other role can escalate itself.
--
-- Every statement is idempotent, so re-running is safe and an environment where an
-- operator has already created a role is not damaged. `gen_random_uuid()` is used
-- for identifiers because these rows have natural keys — `key` — which are what
-- every lookup and the grants below actually use.

INSERT INTO roles (id, key, name_key) VALUES
  (gen_random_uuid(), 'super_admin', 'admin.roles.super_admin'),
  (gen_random_uuid(), 'operations',  'admin.roles.operations'),
  (gen_random_uuid(), 'finance',     'admin.roles.finance'),
  (gen_random_uuid(), 'support',     'admin.roles.support'),
  (gen_random_uuid(), 'read_only',   'admin.roles.read_only')
ON CONFLICT (key) DO NOTHING;

INSERT INTO permissions (id, key) VALUES
  (gen_random_uuid(), 'users.read'),
  (gen_random_uuid(), 'users.update'),
  (gen_random_uuid(), 'users.suspend'),

  (gen_random_uuid(), 'instances.read'),
  (gen_random_uuid(), 'instances.start'),
  (gen_random_uuid(), 'instances.stop'),
  (gen_random_uuid(), 'instances.restart'),
  (gen_random_uuid(), 'instances.reinstall'),
  (gen_random_uuid(), 'instances.delete'),

  (gen_random_uuid(), 'payments.read'),
  (gen_random_uuid(), 'payments.refund'),

  (gen_random_uuid(), 'ledger.read'),
  (gen_random_uuid(), 'ledger.adjust'),

  (gen_random_uuid(), 'nodes.read'),
  (gen_random_uuid(), 'nodes.update'),
  (gen_random_uuid(), 'nodes.delete'),

  (gen_random_uuid(), 'providers.read'),
  (gen_random_uuid(), 'providers.manage'),

  (gen_random_uuid(), 'operations.read'),
  (gen_random_uuid(), 'operations.retry'),

  (gen_random_uuid(), 'tickets.read'),
  (gen_random_uuid(), 'tickets.reply'),
  (gen_random_uuid(), 'tickets.manage'),

  (gen_random_uuid(), 'audit.read'),

  (gen_random_uuid(), 'admins.manage'),
  (gen_random_uuid(), 'roles.manage'),
  (gen_random_uuid(), 'settings.manage')
ON CONFLICT (key) DO NOTHING;

-- super_admin holds everything. Expressed as a cross join so a permission added by
-- a later phase is granted to it without this file needing an edit.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.key = 'super_admin'
ON CONFLICT DO NOTHING;

-- read_only is derived from the naming convention in docs/15, where every read
-- permission ends in `.read`.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.key = 'read_only'
  AND p.key LIKE '%.read'
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.key = 'operations'
  AND p.key IN (
    'instances.read', 'instances.start', 'instances.stop', 'instances.restart',
    'instances.reinstall', 'instances.delete',
    'nodes.read', 'nodes.update', 'nodes.delete',
    'providers.read', 'providers.manage',
    'operations.read', 'operations.retry',
    'tickets.read', 'tickets.reply',
    'users.read'
  )
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.key = 'finance'
  AND p.key IN (
    'payments.read', 'payments.refund',
    'ledger.read', 'ledger.adjust',
    'users.read', 'instances.read', 'operations.read'
  )
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.key = 'support'
  AND p.key IN (
    'users.read', 'users.update',
    'tickets.read', 'tickets.reply', 'tickets.manage',
    'instances.read', 'operations.read'
  )
ON CONFLICT DO NOTHING;
