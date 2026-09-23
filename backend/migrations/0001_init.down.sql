-- 0001_init (down) — reverses the migration history root.
--
-- The up migration creates no schema objects, so there is nothing to drop. The
-- file exists because every versioned migration must be reversible: the CI
-- integration job applies the full history forward and then rolls it back, and
-- a missing down script would make that check impossible.
SELECT 1;
