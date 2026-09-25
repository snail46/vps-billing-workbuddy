# ADR-014 — The Reconciler and the Resilience Posture

Status: Accepted (Phase 11)
Extends: ADR-008 (operation system), ADR-009 (provision slice), ADR-011/012 (surfaces), ADR-013 (agent provider).

## Context

Phase 11's Gate is the platform's ability to survive itself: desired drifts
from observed, workers die mid-operation, reservations expire unclaimed,
nodes go quiet, and a create that succeeded on the provider but timed out on
the platform must not strand a machine. The worker loop already ticks the
operation queue, delivers the outbox and sweeps expired reservations; this
phase adds the checks that notice what no individual workflow can notice.

## Decisions

1. **The reconciler is a set of small reads with conditional writes, run on
   the worker's tick** — not a second state machine. Each check is
   idempotent, bounded (a page of rows per pass) and safe to run
   concurrently with itself, because every write names the state it moves.
   A reconciler that could double-fire is just another thing to reconcile.

2. **Stuck operations are cancelled, not resurrected.** An operation whose
   live state has not moved past the staleness threshold is cancelled with
   the phase note the record carries; a retry the customer can see belongs to
   a fresh enqueue, because a workflow mid-flight has progress and steps a
   resurrected copy could not reconstruct honestly. A worker crash is
   survivable precisely because this check exists.

3. **Drift is corrected by observation, not by guesswork (docs/05: 不得
   猜).** An instance whose desired and observed states disagree — and that
   has no live workflow — is re-observed: the provider is asked for the
   machine's state and the record is set to what the provider reports. The
   reconciler never writes the state it wishes were true.

4. **A create that timed out but succeeded is adopted, not rebuilt.** When a
   provision operation failed on `PROVIDER_TIMEOUT` and the provider reports
   the instance running, the reconciler adopts it: the record is marked
   provisioned and observed running, and the customer is notified. A machine
   that exists is a fact; deleting it would be the platform refusing to see
   its own provider's answer.

5. **Node silence is measured, and measured only.** An agent whose
   last-seen stamp ages past the freshness window is reported stale — the
   offline fact is derived at read time (ADR-013), so the reconciler's job is
   to surface the count to the operator's dashboard, not to invent a state
   transition for it.

6. **Redis losing its memory is not an incident.** Sessions, rate limits and
   attempt counters are all rebuildable by their users (re-login, wait) or
   derivable; nothing that money depends on lives there. The worker's
   dependency probes already tolerate the outage; the platform continues on
   PostgreSQL alone.
