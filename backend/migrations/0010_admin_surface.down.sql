-- 0010_admin_surface (down) — remove what the up script added.
DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE key IN ('orders.read', 'products.read'));

DELETE FROM permissions WHERE key IN ('orders.read', 'products.read');

DROP TABLE IF EXISTS system_settings;
