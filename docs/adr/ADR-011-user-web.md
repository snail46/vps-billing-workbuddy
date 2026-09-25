# ADR-011 — The User Web and the Customer's Own Surface

Status: Accepted (Phase 8)
Extends: ADR-005 (wallet/ledger), ADR-008 (operation system), ADR-009 (provision slice), ADR-010 (provider).

## Context

Phase 8's Gate is the customer's web client: dashboard, catalog, checkout,
instances with detail/network/traffic, operation progress that survives a
refresh, orders/invoices/wallet, notifications/tickets/account — every page in
its six states, in both languages. The backend user surface so far is what the
vertical slice needed: auth, catalogue, orders, payments, subscriptions, the
instance list. docs/08 names the rest — Wallet, Invoices, Notifications,
Tickets — and docs/10 fixes the shape of every page. The openapi document has
declared `/instances/{id}`, its restart and reinstall, `/operations/{id}` and
`/events` since Phase 4; none of them is served yet.

## Decisions

1. **Ownership is a join in SQL, not a check in the handler.** Every
   customer-scoped read is one query that reaches the caller's user id through
   the ownership chain — instance → subscription → user, invoice → user,
   wallet → user, notification → user, ticket → user. A store method that
   cannot express the join does not get a handler; there is no "fetch first,
   compare after" second step to forget, and no window where another
   customer's row is in memory.

2. **Instance actions are operations, arrived at through the engine.**
   Restart and reinstall enqueue `instance.restart` / `instance.reinstall`
   with their own runners registered beside the provision runner — same
   engine, same backoff, same SSE progress. The handler validates ownership
   and the state the action makes sense from, then answers 202 with the
   operation id and returns. The provider is not called from HTTP, ever.

3. **The user's event stream is the operator's machine behind a different
   gate.** `/events?operation_id=` runs the same read-row-push-if-changed loop
   the admin stream runs; the difference is the ownership resolution before
   the stream opens, and a `404` — not `403` — when the operation is not the
   caller's, because the existence of someone else's operation is itself
   information a customer should not get.

4. **Tickets are a small support service, not a commerce flow.** Migration
   0009 adds the four tables the reference schema has carried since the
   beginning (`port_forwards`, `traffic_usage`, `tickets`,
   `ticket_messages`) — the schema document was written before the migrations
   and this phase is where the customer's pages finally need them. Tickets
   open with a first message, gain messages from either side, close by their
   owner; they never touch money, so they live in `internal/support` behind
   the same store discipline, not in `commerce`.

5. **The wallet page reads the ledger through the wallet.** The balance shown
   is `wallets.available_balance_minor` — the projection — and the movement
   list is `ledger_entries` joined on the caller's wallet account ids. The
   projection answers "what can I spend"; the ledger answers "what happened".
   Neither is recomputed for display, and the page makes no write.

6. **The web client is a routed SPA that treats server state as a cache.**
   One router, one query client, one SSE hook. Every page renders the six
   states from the shared layer — loading, loaded, empty, error,
   partial_error, permission_denied — because a page that only looks right
   when the network is healthy is not finished. Operation progress is the
   shared `OperationProgress` fed by the user's own `/events` stream, so a
   refresh resumes what was already running: the operation row is the state,
   and the page re-derives it rather than remembering anything.

7. **Everything user-visible is a key.** The backend already emits i18n keys
   in errors, notifications and operation messages; the web client completes
   the contract by keying all of its own text, both locales shipped together,
   with a shape test that keeps them in lockstep.
