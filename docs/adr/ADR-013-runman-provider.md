# ADR-013 — The Runman Agent Provider

Status: Accepted (Phase 10)
Extends: ADR-007 (provider port), ADR-008 (operation system), ADR-010 (direct provider).

## Context

The direct provider (ADR-010) reaches machines whose control plane is
network-reachable. Nodes behind NAT need the other topology docs/06 names: an
agent on the node dials out to the platform, and the platform's provider calls
ride that connection. Runman is that agent and its gateway. The provider
interface does not change — only the transport underneath it does.

## Decisions

1. **The gateway is pull-based, long-poll HTTP — not a socket to keep honest.**
   The agent authenticates with a node token, then heartbeats and claims
   queued commands and posts results over plain requests. No connection state
   lives in memory, so any replica can serve any agent and a reconnect is
   just the next request. "Online" is a derived fact — `last_seen_at` within
   a freshness window — not a socket registry that lies after a proxy
   timeout.

2. **The connection registry is the database.** `runman_agents` holds the
   node's credential (hashed token, never the token) and its last-seen stamp;
   `runman_commands` is the delivery queue with its own idempotency key and a
   state machine of `queued → delivered → succeeded | failed`. Redis stays
   out: a command is a durable request to real hardware, not a cache entry.

3. **The provider adapter is a translator with a deadline.** Every
   `provider.Provider` method becomes one command payload; the adapter
   enqueues, then waits for the row to reach a terminal state. A wait that
   outlives its budget is `PROVIDER_TIMEOUT` (retryable — the command may
   still land); a result that names a failure maps to docs/06's vocabulary,
   once, at the boundary. Create stays idempotent by key: the gateway
   redelivers the same command, the agent answers "already exists" as the
   same acceptance.

4. **Traffic flows back through the same channel.** The agent's state and
   traffic reports are commands in the agent→platform direction — the
   gateway accepts them beside results, and the reconciler reads what the
   agent observed rather than trusting the agent's push as a write to truth.

5. **The contract suite is the gate, again.** Phase 4's suite runs against
   the runman adapter with a fake agent driving the gateway's state machine
   in-process; the gateway's own HTTP surface is tested at the envelope and
   authentication layer. The business core is not imported, and the mock's
   proof carries to the third transport.
