-- 0012_release_hardening — the column admin TOTP needs (ADR-015 §1). The
-- secret is stored base32 beside the admin it protects; two_factor_enabled is
-- what makes sign-in demand a code, and both move in one write.

ALTER TABLE admins ADD COLUMN two_factor_secret varchar(64);
