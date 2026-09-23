# ADR-004 — Identity, Sessions and RBAC

Status: Accepted
Date: 2026-09-23

## Context

Phase 1 delivers identity and authorisation: users, admins, independent sessions,
roles and permissions, CSRF and rate limiting, the audit foundation, and bilingual
error keys. The specification constrains the shape but leaves the mechanisms open:

- `docs/14` requires user, admin and provider credentials to be **completely
  isolated**, prefers a Secure HttpOnly session cookie, requires CSRF on
  cookie-mutating requests, a CORS allowlist in production, a strong password
  hash, RBAC and rate limiting. It also requires every ownership and RBAC decision
  to be enforced on the backend.
- `docs/15` fixes the role set (super_admin, operations, finance, support,
  read_only) and the permission keys, and states that **every admin handler
  declares its permission explicitly**.
- `docs/16` requires JSON structured logs carrying actor, action and resource, and
  states that **audit is separate from ordinary logs**.
- `db/schema.sql` defines `users`, `admins`, `roles`, `permissions`,
  `admin_roles`, `role_permissions` and `audit_events`. It defines **no session
  table**.
- `docs/openapi/openapi.yaml` fixes `/auth/register`, `/auth/login` and
  `/auth/me`; it defines nothing for admins.
- `docs/08` fixes the response envelope and the permitted status codes.

Two constraints shape everything below. First, admin 2FA is scheduled for Phase 12,
not Phase 1, so the identity model must carry the flag without implementing the
flow. Second, the development machine has no container runtime (ADR-003), so
anything requiring a database can only be verified in CI; mechanisms that need a
database to be *reviewable* were therefore avoided where a self-contained
alternative exists.

## Decision

### 1. Sessions live in Redis, not in a table

A session record holds the subject (`user` or `admin`), the subject id, the
issued-at and expiry instants, and the CSRF secret. It is stored under a
namespaced key with a TTL, and `logout` deletes it.

Rationale: `db/schema.sql` is the consolidated reference for the target schema and
contains no session table, so a session table would either contradict it or make
the reference incomplete. `docs/14` prefers a session cookie, which needs
server-side state for revocation, and Redis is already a foundation dependency
with `--appendonly yes`, so a restart does not silently drop live sessions.
Sessions are also the one piece of state that is *meant* to expire and to be
revoked, which is exactly what a TTL store provides and what a table makes awkward
(rows that must be swept).

Sessions are **not** commercial events: the Phase 11 requirement that a Redis
restart must not permanently lose commercial events concerns orders, payments and
the outbox, which are durable in PostgreSQL. Losing a session costs a re-login.

### 2. User and admin sessions are separate in every dimension

| | user | admin |
|---|---|---|
| cookie name | `vb_user_session` | `vb_admin_session` |
| Redis prefix | `session:user:` | `session:admin:` |
| CSRF header | `X-CSRF-Token` | `X-CSRF-Token` |
| subject type in the record | `user` | `admin` |

Two cookies rather than one cookie with a role claim, because the isolation
required by `docs/14` then holds *structurally*: an admin endpoint reads only the
admin cookie and only the `session:admin:` namespace, so presenting a user session
is not a permission check that could be got wrong — there is nothing to present.
Both cookies are `HttpOnly`, `Secure` outside development, and `SameSite=Lax`.

### 3. Passwords use argon2id, encoded in PHC format

`golang.org/x/crypto/argon2` with OWASP-style parameters (64 MiB, 3 iterations,
4 lanes), producing `$argon2id$v=19$m=...,t=...,p=...$salt$hash`. Storing the
parameters in the hash means the parameters can be raised later without a schema
change: verification reads them from the stored string, and a successful login
with outdated parameters re-hashes the password.

Constant-time comparison; a login against an unknown account still performs a hash
comparison against a dummy record so that response time does not reveal whether an
address is registered.

### 4. CSRF is a synchroniser token bound to the session

A per-session CSRF secret is generated with the session, returned by
`/auth/me` (and the admin equivalent), and required in `X-CSRF-Token` on every
request whose method is not `GET`, `HEAD` or `OPTIONS` **when a session cookie is
present**. The secret is compared in constant time against the value stored with
the session.

`SameSite=Lax` alone is documented as insufficient — it does not protect against
same-site subdomain attacks or older browsers — so the header check is the
enforced control and `SameSite` is defence in depth. Storing the secret with the
session rather than in a second cookie follows from sessions already being
server-side: nothing is gained by a second, weaker channel.

### 5. Rate limiting is a Redis counter keyed by scope

Login and registration are limited per client IP **and** per submitted account
address, so neither a single IP nor a distributed attack on one account passes.
The counter is incremented and expired atomically, and exhaustion answers `429`
with `RATE_LIMITED` (permitted by `docs/08`).

Per-account limiting counts failures rather than attempts where the account is
known, so a legitimate user is not locked out by their own successful logins.

### 6. The permission set is data, and every admin route declares one

Roles and permission keys are rows, seeded idempotently from `docs/15`. An admin's
effective permissions are the union over their roles, resolved per request from the
database (short TTL cache in Redis).

Routes are registered through a helper that **requires** a permission key, so a
handler cannot be mounted without one. A test enumerates the admin router and
fails if any route was registered without a permission — turning "every admin
handler declares its permission" from a review habit into a build failure.

### 7. Audit is a separate table and a separate concern

`audit_events` is written by an audit recorder that takes the acting subject, the
action, the resource and the before/after snapshots, and fills IP, user agent,
`request_id` and `trace_id` from the request context. It writes through the same
transaction as the action where the action is transactional, so an audit record
cannot be lost while the change it describes survives.

Per `docs/16`, audit records are not emitted into the ordinary log stream; the log
carries the same correlation ids, and the audit row is the authoritative record.

### 8. Phase 1 defines no pages

`TASKS.md` assigns the user-facing and admin-facing screens to Phases 8 and 9. The
bilingual requirement here covers the error keys and any identity-related
user-visible strings, which are added to the shared i18n resources so both
locales stay complete. Building login screens now would be the cross-phase
shortcut the instructions forbid.

## Alternatives

1. **A `sessions` table.** Familiar and queryable, and it would survive a Redis
   flush. Rejected: `db/schema.sql` defines no such table, and it makes expiry a
   sweeping job rather than a property of the store.

2. **A signed stateless token (JWT) in a cookie.** No server-side session state.
   Rejected: revocation becomes impractical, `logout` cannot be immediate, and
   `docs/14`'s preference for a session cookie implies server-side sessions. It
   also invites the token to accumulate claims, which is how "one cookie with a
   role" — the isolation failure this ADR exists to prevent — tends to arrive.

3. **bcrypt instead of argon2id.** Simpler and extremely well understood.
   Rejected on balance: argon2id is the current OWASP first choice for new
   passwords and is memory-hard, which is the property that matters against
   GPU-based cracking. Both require `golang.org/x/crypto`, so the dependency cost
   is identical.

4. **A single cookie carrying the subject type.** Half the code. Rejected: it makes
   credential isolation a permission check on a claim, when two cookies make it a
   structural impossibility.

5. **Permissions as a static map in Go.** Faster and compile-time checked.
   Rejected: `docs/15` and the `roles`/`permissions`/`role_permissions` tables make
   roles administrable data, and Phase 9 has an admins/roles management screen.
   The compile-time guarantee is recovered at the route level instead, where it
   actually matters.

6. **Sessions in memory.** Trivially removable. Rejected: the server is expected to
   run multiple replicas and restarts must not sign everyone out.

## Consequences

Positive:

- Credential isolation is structural: there is no code path in which a user
  session can satisfy an admin requirement.
- `db/schema.sql` stays an accurate reference, because nothing was added to it.
- Hash parameters can be raised without a migration.
- "Every admin handler declares a permission" is enforced by the build, not by
  review.
- Redis restart costs logins, not money, and that is now an explicit decision
  rather than an accident.

Negative:

- Identity work that needs a database cannot be verified on this machine; the new
  migrations are proven by the `backend-integration` job, which applies the history
  forward and rolls it back.
- Two cookies and two session namespaces mean the frontend must be told which one
  to send, and the CORS/credentials configuration must allow both.
- Redis becomes a hard dependency of being logged in, not merely of async work.
- Admin 2FA remains unimplemented until Phase 12; the column exists and is
  reported, so the gap is visible rather than implied by its absence.

## Migration / Compatibility

`0002_identity` introduces the seven identity and audit tables, matching
`db/schema.sql`. `0003_identity_rbac_seed` inserts the five roles and the
permission keys from `docs/15` with `ON CONFLICT DO NOTHING`, so it is safe to
re-run and safe to apply to an environment where an operator has already created
roles. Both are reversible; `0003` removes only the rows it owns and leaves roles
referenced by `admin_roles` alone.

`docs/openapi/openapi.yaml` gains `/auth/logout` and the `/admin/auth/*` paths:
the contract previously omitted session termination and admin authentication
entirely, and it is required to match the implementation.

Greenfield: no existing users, admins or sessions.

## Test Plan

- Unit: argon2id hash/verify round-trip including parameter validation and a
  wrong password; session serialisation and expiry; permission-union resolution
  and the route-registry assertion that every admin route declares a permission;
  CSRF rejection on a mutating request without a matching token; rate-limit
  exhaustion answering 429; register/login/me behaviour including the
  unknown-account timing path.
- Integration (`TEST_DATABASE_URL`-gated, CI only): the migration history applies
  forward and rolls back; register then login then `me` against a real database;
  the seeded roles and permissions are present and idempotent; an audit row is
  written with the acting subject and the correlation ids.
- CI composition: the `foundation-gate` job already starts the stack, and the new
  tables are proven by `migrate` exiting 0 with `schema_migrations` clean at the
  new version.
