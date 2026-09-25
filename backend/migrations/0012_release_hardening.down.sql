-- 0012_release_hardening (down) — reverse of the up script.
ALTER TABLE admins DROP COLUMN IF EXISTS two_factor_secret;
