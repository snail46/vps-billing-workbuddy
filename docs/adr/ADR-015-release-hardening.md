# ADR-015 — Release Hardening

Status: Accepted (Phase 12)
Extends: ADR-004 (identity), ADR-003 (topology), ADR-014 (resilience).

## Context

Phase 12 is the last gate before V1 acceptance: security, admin 2FA, backup,
metrics, performance, upgrade. The rule this phase obeys is the one AGENTS.md
has enforced all along — 不做半吊子: a security control that exists but is not
enforced, or a metric no one can read, is worse than an honest absence. What
V1 ships:

## Decisions

1. **Admin 2FA is real TOTP, enforced at the sign-in path.** RFC 6238 with
   the stdlib's HMAC — no dependency. Enrollment mints a secret and an
   otpauth URL, `enable` verifies one code before the flag flips, and once
   enabled `LoginAdmin` refuses a sign-in whose TOTP does not verify (±1
   window for clock drift). The secret is stored beside the admin row; the
   flag and the secret move in one write.

2. **Metrics are a scrape of truth, computed on request.** `/metrics` renders
   Prometheus text from live queries — operations by status, instances by
   observed state, pool statistics, process uptime. No aggregation cache to
   go stale, no pull on the write path, and an operator can read it with
   curl before any dashboard exists.

3. **Backup is pg_dump on a schedule the deployment owns; restore is a
   rehearsed script.** The scripts do one thing each and fail loudly. Backup
   is logical (pg_dump), because the V1 deployment is a single PostgreSQL and
   the restore that has never been rehearsed is not a restore.

4. **Upgrade is the migration history; rollback is `migrate down`.** The CI
   already proves every migration reverses; the runbook documents the
   sequence (backup → pull → migrate up → compose up) and its one-way doors.

5. **Performance is the indexes and the pages, asserted where they matter.**
   Every list is bounded, every page-1 query has its index, and the Gate
   asserts the named indexes exist. A load test is not shipped in V1; the
   acceptance suite documents what was measured and what was not — 不做半吊子
   cuts both ways.

6. **The acceptance suite is a script that runs the proofs.** `scripts/
   acceptance.sh` executes the full local verification and prints the docs/19
   checklist with the evidence pointer for each line, so the final acceptance
   is an invocation, not an essay.
