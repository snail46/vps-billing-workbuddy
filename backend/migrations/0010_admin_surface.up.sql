-- 0010_admin_surface — the two permission keys the admin screens declare that
-- the seed did not carry (ADR-012 §1). docs/15's list is examples; a screen
-- that cannot name its permission is a screen that cannot be granted.

INSERT INTO permissions (id, key) VALUES
  (gen_random_uuid(), 'orders.read'),
  (gen_random_uuid(), 'products.read');

-- The same grant shape 0005 used: every role that holds a sibling read holds
-- these, except read_only, which holds every read by definition.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE p.key IN ('orders.read', 'products.read')
  AND r.key IN ('super_admin', 'operations', 'finance');

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE p.key IN ('orders.read', 'products.read')
  AND r.key = 'read_only';

-- The settings table the reference schema names and the settings screen reads.
-- V1 writes nothing here; the row vocabulary is the platform's, added when a
-- setting is first consumed rather than guessed in advance.
CREATE TABLE system_settings (
  key varchar(128) PRIMARY KEY,
  value text NOT NULL,
  description text,
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admins(id)
);
