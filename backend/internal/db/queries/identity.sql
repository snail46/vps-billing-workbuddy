-- Identity queries for Phase 1.
--
-- These are the only statements that read or write credentials. Nothing outside
-- this file touches `users.password_hash` or `admins.password_hash`, so the hash
-- has one reader (the login path) and one writer (registration and any future
-- password change), which is what keeps the verification logic from drifting.
--
-- Email is compared against the stored value after the application has normalised
-- it (trimmed and lower-cased). Normalising in SQL as well would make every lookup
-- non-sargable for no benefit, since the write path is the only writer.

-- name: CreateUser :one
INSERT INTO users (
  id, email, password_hash, status, locale, timezone
) VALUES (
  $1, $2, $3, $4, $5, $6
)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: RecordUserLogin :exec
UPDATE users
SET last_login_at = now(), updated_at = now()
WHERE id = $1;

-- name: CreateAdmin :one
INSERT INTO admins (
  id, email, password_hash, status, display_name
) VALUES (
  $1, $2, $3, $4, $5
)
RETURNING *;

-- name: GetAdminByEmail :one
SELECT * FROM admins WHERE email = $1;

-- name: GetAdminByID :one
SELECT * FROM admins WHERE id = $1;

-- name: RecordAdminLogin :exec
UPDATE admins
SET last_login_at = now(), updated_at = now()
WHERE id = $1;

-- name: ListPermissionsForAdmin :many
-- The effective permission set is resolved from the database on each request
-- rather than cached in the session, so revoking a role takes effect immediately
-- instead of at the next login.
SELECT DISTINCT p.key
FROM admin_roles ar
JOIN role_permissions rp ON rp.role_id = ar.role_id
JOIN permissions p ON p.id = rp.permission_id
WHERE ar.admin_id = $1
ORDER BY p.key;

-- name: ListRolesForAdmin :many
SELECT r.key
FROM admin_roles ar
JOIN roles r ON r.id = ar.role_id
WHERE ar.admin_id = $1
ORDER BY r.key;

-- name: ListPermissionKeys :many
SELECT key FROM permissions ORDER BY key;

-- name: CreateAuditEvent :exec
INSERT INTO audit_events (
  id, actor_type, actor_id, action, resource_type, resource_id,
  before_data, after_data, ip_address, user_agent, request_id, trace_id
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
);
