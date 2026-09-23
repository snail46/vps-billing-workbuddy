# ADR-001 — Backend Stack

Status: Accepted
Date: 2026-09-23

## Context

`docs/02-ARCHITECTURE.md` fixes the shape: a V1 modular monolith composed of a
`server` process and a `worker` process, backed by PostgreSQL and Redis. It also
fixes the dependency direction:

```
HTTP Handler → Application Service → Domain → Repository/Provider
```

and explicitly forbids a Handler from touching SQL or a Provider directly.

`AGENTS.md` adds hard constraints that the stack must support rather than fight:

- Money is `BIGINT` minor units and every funds movement goes through the Ledger;
  Ledger history is immutable and corrections are new adjustment rows.
- `Payment + Order + Ledger + Outbox` must commit in a single transaction.
- Reservation must re-check capacity under a DB row lock.
- `payments` / `operations` carry unique idempotency keys.
- Long operations must be Operation/Workflow/Worker driven, never synchronous HTTP.
- Financial correctness and duplicate-webhook behaviour must be provable by tests.

The starter pack specifies the language and datastores but not the libraries.
`AGENTS.md` requires an ADR before core architecture is committed, and the choice
is expensive to reverse once twelve phases of code depend on it.

An ORM was considered and is explicitly rejected: the domain requires precise,
auditable SQL (row locks, `ON CONFLICT`, partial unique indexes, transaction
scoping) and an ORM that hides statement generation would erode the very
boundaries `docs/02` mandates.

## Decision

**Go 1.26** modular monolith with the following libraries:

| Concern | Choice | Rationale |
| --- | --- | --- |
| HTTP router | `github.com/go-chi/chi/v5` | `net/http`-native (`http.Handler`), zero magic, composable middleware groups, per-route middleware — maps cleanly onto "every Admin handler declares its permission explicitly". |
| DB driver / pool | `github.com/jackc/pgx/v5` (`pgxpool`) | Native PostgreSQL protocol, real prepared statements, `pgx.Tx` gives explicit transaction control required for the single-transaction financial path, `BIGINT` handled as `int64` without reflection. |
| Typed queries | `github.com/sqlc-dev/sqlc` | Generates typed Go from hand-written SQL. SQL stays reviewable and greppable; no runtime statement synthesis; repository interfaces remain hand-written so the Domain never imports generated code. |
| Migrations | `github.com/golang-migrate/migrate/v4` as a **library**, driven by `cmd/migrate` with `embed.FS` | One migration mechanism for local, CI and production (versioned SQL only, per `docs/17`). Embedding makes the binary self-contained: no file mount, no external migration image, and CI can migrate without Docker-in-Docker. |
| Redis client | `github.com/redis/go-redis/v9` | Session/queue/cache backing store; `PING`-based readiness check. |
| Config | `github.com/caarlos0/env/v11` | Struct-tagged env parsing with validation; keeps `internal/config` declarative and testable. |
| Logging | standard library `log/slog` | JSON handler out of the box, no dependency; `request_id`/`trace_id` carried via `context` and injected by a handler wrapper. |
| IDs | `github.com/google/uuid` + in-house UUIDv7 generator | `docs/04` requires UUIDv7 core IDs; PostgreSQL 17 has no `uuidv7()`, and app-side generation is explicitly permitted. |
| Tests | stdlib `testing` + `github.com/stretchr/testify` | Unit tests run without infrastructure; integration tests are gated on an env var so they run in CI and are skipped locally. |

Explicitly **not** used: any ORM (GORM, ent, Bun). Introducing one later requires
a superseding ADR.

### Process layout

Three binaries from one module (`github.com/snail46/vps-billing-workbuddy/backend`):

- `cmd/server` — Business API. HTTP only; never executes host commands, never
  talks to a Provider directly.
- `cmd/worker` — Outbox/Queue/Workflow/Reconciler process.
- `cmd/migrate` — versioned migration runner. Kept out of the server so that a
  normal server start can never silently mutate the schema, and so production
  migration stays an explicit, auditable step.

### Layer enforcement

```
backend/internal/
  config/     env loading + validation
  logging/    slog setup, context-scoped request_id/trace_id
  httpx/      API envelope (docs/08), error codes, JSON helpers
  middleware/ request_id, trace_id, recover, access log, CORS, security headers
  health/     /health/live, /health/ready + dependency checks
  db/         pgxpool lifecycle
  db/queries/ hand-written SQL consumed by sqlc
  db/sqlc/    generated code — imported only by repository implementations
  redisx/     redis client lifecycle
  version/    build metadata injected via -ldflags
  provider/   Provider Contract (docs/06) — interfaces only in Phase 0
```

Domain and application packages are added by the phase that owns them. Phase 0
deliberately ships **no** business routes and **no** domain tables.

## Alternatives

1. **`net/http` with the Go 1.22+ pattern router, no third-party router.**
   Fewest dependencies and a genuinely defensible choice. Rejected because
   per-route permission middleware for ~40 admin routes, route groups for
   `/api/v1` and `/api/v1/admin`, and sub-router mounting would be hand-rolled
   and hand-tested — cost paid early for no domain benefit.

2. **`gin` + SQL builder (no ORM).** Fast CRUD, large ecosystem. Rejected: gin's
   `context.Context` wrapper propagates a framework type through service
   signatures, and its binding/validator encourages validation logic at the
   transport layer where `docs/20` wants it answered explicitly per feature.

3. **`gin` + GORM.** Fastest to first feature. Rejected: hidden statement
   generation conflicts with row-lock-based reservation, the single-transaction
   financial path, and the requirement that Ledger history is immutable;
   `AutoMigrate` also directly contradicts "production only allows versioned
   migrations".

4. **`goose` or `atlas` for migrations.** Both are fine. `golang-migrate` chosen
   for its explicit up/down pairing and first-class `iofs` source, which is what
   makes the embed-and-ship-single-binary approach work.

## Consequences

Positive:

- Every financial guarantee in `AGENTS.md` is expressible directly: explicit
  `pgx.Tx` scoping, `FOR UPDATE` row locks, `ON CONFLICT DO NOTHING` for webhook
  idempotency, and partial unique indexes — all authorable and testable as SQL.
- No statement-generation layer between the code and the database, so reviewers
  can audit exactly what runs.
- `cmd/migrate` shares one code path across local dev, CI and production.

Negative:

- More hand-written SQL than an ORM would require; mitigated by sqlc generating
  the scan/struct layer.
- Four binaries' worth of wiring (`server`, `worker`, `migrate`) rather than one.
- The team must know PostgreSQL semantics (row locks, partial indexes) rather
  than delegating to an ORM — accepted deliberately, since that knowledge is
  load-bearing for this product.

## Migration / Compatibility

Greenfield: no existing code, data or API. No migration path required.

The module path `github.com/snail46/vps-billing-workbuddy/backend` is fixed here
and baked into every import. Changing it later is a repository-wide rewrite; it
is recorded in ADR-001 precisely so it is treated as a decision, not a default.

## Test Plan

- `gofmt -l`, `go vet ./...`, `golangci-lint run` — must be clean.
- `go build ./...` — all three binaries compile.
- Unit: `internal/config` (env parsing, validation failures), `internal/httpx`
  (envelope shape matches `docs/08` for success and error), `internal/health`
  (ready reports degraded when a dependency check fails).
- Integration (CI only, gated on `TEST_DATABASE_URL`): migrations apply and
  roll back cleanly; `pgxpool` and Redis clients connect.
- The Phase 0 Gate in `.github/workflows/ci.yml` starts the full stack with one
  compose command and asserts `/health/live` and `/health/ready` return 200.
