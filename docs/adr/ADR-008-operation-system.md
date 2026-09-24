# ADR-008 — The operation system: queue, worker, retries and SSE

Status: accepted (Phase 5)
Amends: ADR-007 §2 (the reservation is a persisted row, not only a transition)
Complements: ADR-005, ADR-006, ADR-007

## The problem

AGENTS.md fixes the shape of every long action: Operation + Workflow + Worker,
HTTP 202 with an `operation_id`, no synchronous blocking, no silent failure.
`docs/05` gives the operation a vocabulary — `queued / running /
waiting_provider / waiting_resource / verifying / retrying / succeeded / failed /
cancelled` — and `docs/07` composes workflows from steps. What is left to
decide: what claims work and how, what a retry moves and when, how a client
watches progress without polling the API to death, and — discovered while
implementing Phase 4 — what the reservation primitive owes the reference
schema, which does define a `resource_reservations` table that ADR-007 §2
declared absent.

## Decisions

### 1. The database is the queue

No broker. `docs/01` excludes Kafka and friends, and the operations table is
already the durable record of what was asked; a queue in front of it would be a
second copy of the same fact with its own failure modes. A worker claims a
queued operation with `SELECT ... FOR UPDATE SKIP LOCKED`, so N workers on one
database each take distinct operations and none of them waits. The outbox
publisher (the queue's other half, docs/09) reads `outbox_events` the same way:
claim, publish, mark — already shaped by the columns 0004 gave it
(status/attempts/next_attempt_at).

### 2. The machine, edge by edge

```
queued     → running            (a worker claims it)
running    → waiting_provider   (the provider accepted an async action)
running    → waiting_resource   (capacity or a node is temporarily unavailable)
waiting_*  → running            (the wait resolved)
running    → verifying          (the provider reports done; the platform verifies)
verifying  → succeeded          (verified)
running / verifying / waiting_* → retrying   (a transient failure)
retrying   → running            (the backoff elapsed; attempt+1)
retrying   → failed             (max_retries exhausted, or the error is terminal)
queued / running / waiting_* / verifying → cancelled  (customer or admin)

succeeded / failed / cancelled are terminal.
```

Every step of a workflow records its own row (`operation_steps`), so progress
is a fact per step rather than a percentage someone estimated: the operation's
progress is the completed steps' share, and `docs/07`'s "进度按步骤映射，不按
时间伪造" is enforced by computing it from the steps and never accepting it
from a caller.

### 3. Retries are classified, then backed off

A provider error arrives carrying `Retryable` (the contract's own field,
ADR-007 §5) — the operation records it, moves to `retrying` while attempts
remain, and the worker re-claims it after a backoff. The backoff is exponential
with a cap: `min(2^attempt, 60) seconds`, stated as a constant beside the
machine. A non-retryable error, or an attempt beyond `max_retries`, is `failed`
with the provider's error code preserved — `error_code`/`error_message` are
columns of the operation, and "支持安全 retry/reconcile" starts with the
failure being readable.

### 4. Watching is SSE, and the delivery is the row

`GET /operations/{id}/events` streams `operation.updated.v1` payloads as
server-sent events, scoped to the operation's owner or an administrator. The
stream reads the operation and its steps on an interval and emits on change —
no second notification channel between the worker's write and the client's
read, because a second channel is a second copy of the state to keep honest.
One second is slower than a push and faster than a human refresh; when a real
push is needed, `LISTEN/NOTIFY` is the migration path and nothing client-side
changes. `docs/09`'s rule stands either way: only the owner's authorized
payload is written to the stream.

### 5. ADR-007 §2 is amended: the reservation is a row

The reference schema does define `resource_reservations` — keyed by node and
operation, carrying the reserved amounts and an `expires_at` — and the grep
that missed it wrote ADR-007's §2 on the premise that it did not exist. The
decision is amended, not defended:

- **Reserve** writes a `reserved` row and moves the node's counters in one
  transaction. The row is what makes a reserve idempotent (one open reservation
  per operation — a partial unique index) and what makes the workflow's retry
  of a half-finished reserve safe instead of double-counting.
- **Commit** marks the row `committed` and moves reserved into allocated.
- **Release** marks it `released` and gives the counters back.
- **Expiry**: a reservation past `expires_at` is released by the sweep —
  a worker that died mid-work stops holding capacity, which is the property
  the leak analysis in ADR-007 §2 wanted, with a mechanism the schema
  actually provided.

The counters on the node stay the scheduler's read model; the rows are the
audit and the idempotency. The ADR-007 test that asserted the counters without
rows now asserts both, and the amendment is recorded here rather than quietly
rewritten, because a decision log that silently changes is not a log.

## Consequences

- Phase 6's provision workflow registers a runner with this phase's registry and
  owns none of the machinery: claiming, retries, progress, events and the
  reservation lifecycle are already built.
- `error_code` on an operation is the provider's standardized code, so
  support can search failures by what actually went wrong.
- The SSE stream and the operation row agree by construction — one is derived
  from the other — and the reconciler's question ("what is really happening?")
  has a first answer in `operation_steps`.
