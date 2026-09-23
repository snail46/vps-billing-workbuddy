# worker

The asynchronous process: outbox, queue, Workflow execution, Operations and the
Reconciler.

```sh
go run ./cmd/worker
```

## Responsibilities

Everything that must not happen inside a request (docs/02-ARCHITECTURE.md).
`AGENTS.md` requires create, delete, start, stop, restart, reinstall, password
reset and NAT changes to run as Operations driven by a workflow rather than as
synchronous HTTP calls.

## Phase 0 scope

The process exists, connects to PostgreSQL and Redis, and shuts down cleanly.

It has no scheduled work yet: the outbox arrives with the commerce phase. The
lifecycle is established now so that the container, its configuration and its
deployment wiring are proven before a queue exists, rather than being debugged
for the first time under business load.

While idle it verifies its dependencies on a fixed cadence and logs a failure
rather than exiting. docs/18-TEST-PLAN.md exercises Redis restarts and worker
crashes, so the worker has to tolerate a temporary loss of its backing services
instead of requiring an operator to restart it.
