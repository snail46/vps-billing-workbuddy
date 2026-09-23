# ADR-003 — Foundation Topology, Migration Ownership and Gate Execution

Status: Accepted
Date: 2026-09-23

## Context

Three questions had to be settled before any Phase 0 code was written, because
each one is expensive to change once later phases depend on it:

1. **Which services does "基础环境" contain?** `TASKS.md` Phase 0 lists
   PostgreSQL / Redis / Compose, and its Gate is "一条 compose 命令启动基础环境".
   `docs/17-DEPLOYMENT.md` lists a longer V1 set: reverse-proxy, user-web,
   admin-web, server, worker, postgres, redis.

2. **Who owns the initial schema?** `db/schema.sql` is a *consolidated V1
   reference schema* covering every domain object in the product — orders,
   payments, ledger, subscriptions, instances, nodes, outbox, audit. Phase 0 has
   no domain objects of its own. Loading that whole file as `0001_init` would
   mean Phase 0 creating tables that Phases 1–11 are specified to introduce,
   which directly violates the "不得跨 Phase" instruction and makes each later
   phase's schema work a no-op review.

3. **Where does the Gate actually run, and is it real?** The Gate must be
   genuinely executed. A Gate that is marked green without the stack having
   started would be exactly the "假成功" that `AGENTS.md` forbids.

### Constraint discovered during reconnaissance

The local development machine has:

- Go 1.26.5, Node 22.22.2, npm 10.9.7, Git 2.55 — usable.
- **No Docker runtime**, and **no WSL** (`wsl.exe` is blocked by security
  policy, and the block is not overridable from a command).
- Reachable registries: `goproxy.cn`, `registry.npmmirror.com`.
  Unreachable: `proxy.golang.org`, `registry.npmjs.org`.

Consequence: the compose stack **cannot be started on the local machine**. It
can, however, be started on GitHub Actions `ubuntu-latest` runners, which ship a
Docker daemon, and both registries in use are publicly reachable from there.

## Decision

### 1. Foundation service set

`deploy/docker-compose.yml` defines the Phase 0 environment as:

`postgres` · `redis` · `migrate` (one-shot) · `server` · `worker` · `user-web` · `admin-web`

- Every stateful service declares a `healthcheck`; `migrate`, `server` and
  `worker` gate on `service_healthy` / `service_completed_successfully` rather
  than on bare `depends_on`, so startup order is asserted, not assumed.
- `server`, `user-web` and `admin-web` publish ports so the Gate can probe them
  from the runner.
- `migrate` is a **separate one-shot service**, not something the server does on
  boot. `docs/17` requires production to use versioned migrations only; letting a
  server process mutate the schema on start would put schema change on the
  request-serving path.

#### One image, three roles

`migrate`, `server` and `worker` all run `vps-billing-backend:local` and select
their binary through compose's `command`. `backend/Dockerfile` therefore declares
`CMD` and **not** `ENTRYPOINT`.

That is load-bearing, not stylistic. Compose *overrides* CMD with `command`, but
*appends* `command` to ENTRYPOINT. With `ENTRYPOINT ["/app/server"]` the migrate
service actually ran `/app/server /app/migrate up`: the API started in place of
the migration and never exited, and `server` and `worker` — both gated on
`service_completed_successfully` — waited for it.

The failure mode is what makes this worth recording: **nothing reported an error**.
All images built in about 90 seconds, postgres and redis went healthy, the
migration container started, and then the step was silent for 24 minutes until the
run was cancelled. A wrong binary in a shared image produces no diagnostic by
itself; the container's own command line does, which is why the Gate's digest
prints `ps -a`.

`scripts/check-compose.py` asserts both halves of the invariant — no ENTRYPOINT in
the image, and an explicit `command` naming the binary for each service — so the
regression cannot return unnoticed.

**Reverse proxy is deferred to Phase 12.** `docs/17` lists it as a V1 deployment
component, but it carries no Phase 0 Gate requirement, and adding an nginx hop
would widen the Gate's failure surface without proving anything about the
foundation. Recorded here so the deferral is a decision rather than an omission.

### 2. Migration ownership

`db/schema.sql` remains the **reference** document — the consolidated description
of the target schema — and is not executed by anything.

`backend/migrations/0001_init.{up,down}.sql` is the migration **history root**.
Phase 0 owns no domain tables, so it contains no DDL.

The path is inside the module rather than at the repository root because the
migrations are embedded with `go:embed`, which cannot reference files outside the
module directory. Embedding is what makes the migrate binary self-contained, so
the location follows from that decision rather than being arbitrary.

Rationale: migration history should reflect when each object actually entered the
system. Phases then add their own migrations in order
(Phase 1 identity/RBAC, Phase 2 commerce/finance, Phase 4 infrastructure, …),
each one reviewable against the phase that specified it. The Gate still proves the
migration mechanism end to end, because running it creates `schema_migrations`
and records version 1.

### 3. Gate execution

The Gate runs in `.github/workflows/ci.yml` as a dedicated `foundation-gate` job
on `ubuntu-latest`, and performs the real sequence:

```
compose up -d --build  →  wait for healthy  →  GET /health/live  →  GET /health/ready
                       →  assert migrate completed  →  compose down -v
```

The job fails if any step fails; there is no `continue-on-error` and no
"assume it works" step. Phase 0 is not considered closed until this job passes on
the pushed commit.

Both the command and the job are bounded: `timeout 480` around the compose command
and `timeout-minutes: 20` on the job. `up -d` waits for its `depends_on`
conditions, so a service that starts but never satisfies one blocks indefinitely;
the inner bound turns that into a fast, diagnosable failure instead of an hour of
runner time.

A digest step runs with `if: always()` and reports elapsed time, the slowest build
steps, the tail of the compose output and container state. It runs after
cancellation and timeout as well, because a cancelled step never reaches its own
error handling — which is why the first hung attempt produced no evidence at all.

Locally, the developer can verify everything that does not need a container
runtime: `gofmt`/`go vet`/`go build`/unit tests, frontend `typecheck`/`lint`/
`build`, and YAML validity of the compose file and workflow. The report for each
phase states explicitly which checks were executed locally and which are
CI-only — see `docs/18-TEST-PLAN.md` integration tier.

#### Build-context completeness is checked without a container runtime

A Docker build stage sees only what its Dockerfile `COPY`s. A file the build
reads but the Dockerfile never copies is therefore **invisible to every local
check**: the host build passes and only the image build fails, in a job whose
logs require authentication to read. This was not hypothetical — it is how the
Gate first failed, with `frontend/tsconfig.base.json` absent from both web
images, so `tsc` aborted with `TS5083` before checking a single file.

`scripts/check-docker-context.py` closes that gap. It parses the `COPY`
instructions out of a Dockerfile's build stage, stages exactly those files, and
runs the same `tsc --noEmit` the image build runs. It derives the file list from
the Dockerfile rather than hardcoding one, so it follows the Dockerfile instead
of drifting from it. It is wired into `scripts/verify-local.sh`, and its failure
path is itself verified: against the pre-fix `COPY` set it reproduces `TS5083`
and exits non-zero.

Consequence for the CI-only surface: the *set of failures that can only be
discovered on a runner* is now smaller than "anything in the image build" — it is
limited to container-runtime semantics (start order, healthchecks, port binding,
`npm ci` inside the image) rather than build-input completeness.

### 4. Dependency registries

Registry access is environment-specific, so it is configured where it belongs and
nowhere else:

- Go: `GOPROXY` is already `https://goproxy.cn,direct` in this machine's
  `go env`. CI sets `https://proxy.golang.org,https://goproxy.cn,direct`, which
  resolves on the runner and degrades gracefully.
- npm: `frontend/.npmrc` (workspace-scoped) points at
  `https://registry.npmmirror.com`, which is reachable both locally and from CI.

## Alternatives

1. **Install Docker Desktop / enable WSL locally.** Would allow a local Gate
   run. Rejected as the primary mechanism: it requires changing the machine's
   security configuration outside the repository, and CI is the correct place
   for a reproducible Gate anyway. It remains a valid future convenience.

2. **Run PostgreSQL and Redis as native local processes.** Would remove the
   container dependency. Rejected: it makes the local environment diverge from
   the deployed topology, and the Gate is specifically about the compose
   environment.

3. **Load `db/schema.sql` as `0001_init`.** Simplest way to have "real" tables in
   Phase 0. Rejected: it would have Phase 0 create every domain table, collapsing
   the per-phase migration history and pre-empting phases that are specified to
   introduce them — a direct violation of the phase discipline in `TASKS.md`.

4. **Have the server apply migrations on startup.** Fewer moving parts.
   Rejected: contradicts `docs/17`, puts schema change on the serving path, and
   makes the migration an implicit side effect of a restart.

5. **External migration image (`migrate/migrate`) in compose.** Avoids a
   third binary. Rejected: it would give local/CI/production two different
   migration mechanisms (CLI vs. whatever else), and embedding via `iofs` makes
   the binary self-contained with no file mount.

## Consequences

Positive:

- "Phase 0 done" has a falsifiable definition: the `foundation-gate` job passes.
- Migration history stays honest — each table appears in the phase that owns it.
- One migration mechanism across local, CI and production.
- Registry constraints are encoded in the repo, so a fresh clone works on this
  machine without undocumented setup.

Negative:

- Contributors on this machine cannot run the full stack; the Gate feedback loop
  is a push to GitHub rather than a local command. Mitigated by keeping the
  CI-only surface small (compose startup + dependency connectivity) and running
  everything else locally.
- CI-only verification means the first push is a genuinely meaningful event;
  compose errors will surface there rather than locally. Accepted; the compose
  file is reviewed and YAML-validated locally.
- Reverse proxy is absent from the V1 compose file until Phase 12, so the
  topology in `docs/17` is not fully realised in Phase 0.

## Migration / Compatibility

Greenfield. `deploy/docker-compose.example.yml` from the starter pack is left
untouched as the original specification; `deploy/docker-compose.yml` is the
authoritative, executable environment.

## Test Plan

- `foundation-gate` job: compose up, health probes, migrate completion, teardown.
- `compose-validate` job: `docker compose config -q` rejects a malformed file.
- `backend` job: format, vet, lint, build, unit tests.
- `backend-integration` job: PostgreSQL 17 + Redis 8 service containers;
  migrations apply forward and roll back; `TEST_DATABASE_URL`-gated tests run.
- `frontend` job: install, typecheck, lint, build.
- `scripts/check-docker-context.py`, run locally by `scripts/verify-local.sh`: the
  build stage of each web image type-checks from its declared `COPY` set.
- `scripts/check-compose.py`, likewise: the service set, each backend service's
  `command`, the absence of an ENTRYPOINT in the image, the healthchecks and the
  `service_completed_successfully` gates. Its failure path is verified by
  reintroducing each defect and confirming it is reported.
