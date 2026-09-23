# ADR-005 — Commerce, Money and the Ledger

Status: Accepted
Date: 2026-09-23

## Context

Phase 2 delivers the commercial core: products and plans, orders with price
snapshots, invoices, payments, wallets and the ledger, a fake payment gateway, and
the transactional outbox. Its Gate is narrow and unforgiving:

> 同一支付回调重复 100 次只入账一次。

*The same payment callback, repeated a hundred times, credits exactly once.*

The specification constrains the shape but leaves the mechanism open, and in one
place the two constraints pull against each other:

- `AGENTS.md` lists what must not be changed: Order, Payment, Invoice, Subscription
  and Instance stay separate; money goes through the ledger; **`Payment + Order +
  Ledger + Outbox` are written in one transaction**; ledger history is never
  `UPDATE`d, and a correction is a new adjustment entry; using a Provider database as
  the commercial source of truth is forbidden; and changing a wallet balance while
  bypassing the ledger is forbidden.
- `db/schema.sql` declares `wallets.available_balance_minor` — a stored balance.

Those are consistent only under one reading: the balance column is a **projection**
derived from the ledger, never the record of what a customer holds. This ADR states
that reading, because leaving it implicit is how a codebase ends up with two
answers to "how much does this customer have" and no way to tell which is right.

The reference also declares `ledger_entries.direction` with a `CHECK` over
`debit`/`credit`, which is a double-entry shape rather than a single-sided log.

## Decision

### 1. The ledger is the source of truth; a wallet balance is a projection

A financial fact is a `ledger_transactions` row with its `ledger_entries`. The
`wallets.available_balance_minor` column is maintained **in the same transaction** as
the entries it summarises, and it is rebuildable from them at any time.

Two consequences follow, and both are deliberate:

- Every balance is derivable from the ledger, and `Ledger.RebuildBalance` does
  exactly that. It is what makes the projection *checkable* rather than *trusted*,
  and it is the operation a reconciliation has to be able to perform.
- The balance is a read optimisation and a fast available-funds check. It is never
  read as the authority for a historical or reporting question, because it only ever
  holds the present.

The balance is signed and the ledger's amounts are not: `ledger_entries.amount_minor`
carries a `CHECK (amount_minor >= 0)` and the direction carries the sign, so a
projection is a signed sum over the entries. Storing a magnitude plus a direction is
what makes an unbalanced transaction visible — the entries of one transaction must
sum to zero.

### 2. Every transaction balances, per currency

`ledger_transactions` rows are **not** created directly. `Ledger.Post` takes a set of
entries and refuses the set unless, for every currency in it, the debits and the
credits are equal and there are at least two entries. A single-sided entry is
refused, not accepted and tolerated.

The reason is not bookkeeping aesthetics. With one-sided entries, "where did this
money come from" has no answer in the data, a double-post is indistinguishable from
two legitimate posts, and an error can only be found by noticing that a number looks
wrong. With balanced entries, a mistaken post fails a check that the same function
performs for every caller.

Platform-level accounts have no row anywhere — the reference declares no accounts
table — so they are identified by fixed, well-known identifiers defined in the ledger
package. `account_id` is opaque to the schema, and a constant is the honest way to
name an account the platform itself holds.

### 3. Idempotency is a database property, not a code path

The Gate is about exactly-once under repetition, and repetition includes
*concurrent* repetition. A "check whether it was already processed, then process it"
sequence is wrong for that, because two callbacks can pass the check together.

So the settlement is gated on a **conditional state transition**:

```
UPDATE payments SET status = 'succeeded', paid_at = now()
WHERE gateway = $1 AND gateway_payment_id = $2 AND status IN ('pending', 'processing')
RETURNING ...
```

Zero rows returned means someone else already performed that transition. That is a
single atomic statement, so it holds under concurrency, and it is the only thing that
decides whether the ledger write happens. On zero rows the handler writes **nothing**
and answers as a success — the caller's obligation was to deliver the notification,
and it has been met.

Three independent guards back it up, and they exist because each catches a different
mistake:

- `payments.idempotency_key` is `UNIQUE`, which is what stops a retried *creation*
  from becoming two payments.
- `ux_payments_gateway_external` on `(gateway, gateway_payment_id)` is declared by the
  reference, which is what stops one gateway payment from becoming two payments.
- A partial `UNIQUE (type, reference_type, reference_id)` on
  `ledger_transactions` is added by this phase. It makes "one settlement per payment"
  a property of the schema, so a hole in the state machine cannot become a double
  credit; the write fails loudly instead.

A callback whose amount or currency disagrees with the stored payment is refused with
a conflict rather than treated as a replay. Silently accepting it would mean the
platform's record and the gateway's disagree and nobody is told.

### 4. An order snapshot is immutable; the price lives in the order

`order_items` stores `product_snapshot` and `plan_snapshot` as JSON, alongside the
`product_id` and `plan_id` they came from. Both are kept, and the pair is the point:
the identifiers are for reporting across products, and the snapshots are what the
customer actually agreed to.

Editing a plan therefore cannot change an order that already exists, which is the
behaviour `AGENTS.md` means by "order snapshots". The snapshots are written once,
when the order is created, and nothing in the codebase updates them — the same
discipline the ledger has, applied to a price.

### 5. The outbox is written inside the transaction; delivery is not this phase

`docs/09` requires the key business events to be written to the transactional outbox
first and published second, and the consumer to deduplicate by `event_id`. Phase 2
writes the row in the same transaction as the change it describes — that is the part
the ordering guarantee rests on, and the part that cannot be added later without
moving every write.

Publishing is Phase 5's, which owns the queue and the worker. A Phase 2 that
published events would be implementing a queue by accident, and the relay needs the
worker's retry and backoff policy to be worth having.

### 6. The fake gateway is an adapter behind a port, and it verifies signatures

A payment gateway is not the compute `Provider` contract of `docs/06` — different
verbs, different failure modes — so it gets its own port rather than being forced
into that interface. The fake implementation signs its callbacks with HMAC-SHA256
over the **raw request body**, and the verifier refuses a body whose signature does
not match.

Signing the raw bytes rather than a parsed form is not a detail: a signature over
re-serialised JSON verifies things that were never signed, and the classic failure is
that two different byte strings parse to the same object.

## Alternatives

1. **Treat `wallets.available_balance_minor` as the authoritative balance.** Simple,
   and the column is right there. Rejected: `AGENTS.md` forbids changing a wallet
   balance while bypassing the ledger, so the column cannot be the thing that decides
   — and a stored balance with no derivation gives no answer to "why is it this
   number", which is the first question asked about money.

2. **A single-sided ledger** — one row per movement, with a signed amount. Half the
   rows and simpler queries. Rejected: it makes an unbalanced post undetectable, and
   the reference's `direction` column with a `debit`/`credit` check already declares
   the double-entry shape.

3. **Idempotency by a dedicated `processed_webhooks` table.** Explicit and easy to
   read. Rejected as the primary mechanism: it is a second record of the same fact,
   so it can disagree with the payment's own status, and the disagreement is exactly
   the case that matters. The payment's conditional transition is the fact itself.
   The three unique constraints are kept because they are the schema's way of holding
   the invariant, not a parallel bookkeeping of it.

4. **A wallet balance column maintained by a trigger.** Guarantees it cannot be
   forgotten. Rejected: a trigger cannot see whether the write went through the
   ledger, so it would make the balance update happen for any `INSERT` into
   `ledger_entries` — including the ones a future bug writes — and would remove the
   single place where the projection is maintained and can be rebuilt.

5. **Synchronous gateway confirmation instead of webhooks.** No callback verification
   needed. Rejected: it contradicts `docs/06`'s asynchronous posture and would make
   the Gate itself unrepresentable — there would be no callback to repeat.

6. **Publishing events directly from the request.** Fewer moving parts. Rejected:
   `docs/09` requires the outbox, and the requirement exists precisely because the
   publish and the write are not atomic with each other.

## Consequences

- A ledger post is refused if it does not balance. That check runs on every post, so
  a future phase that adds a new kind of movement gets it without asking.
- Settlement is a conditional transition, so a callback that arrives twice — or
  concurrently — writes once. The second caller is told the notification was
  accepted, which it was.
- The wallet balance can always be checked against the ledger, and the check is
  available as an operation rather than as a query someone has to write correctly
  under time pressure.
- Phase 2 creates `node_groups` and `subscriptions` as tables, because `plans` and
  `invoices` reference them and a foreign key needs its target. It creates **no rows**
  in them and implements no behaviour for them: node groups are Phase 4's and
  subscription lifecycle is Phase 3's. Stating this here so that the presence of the
  tables is not mistaken for the phases having been done.
- `invoices.status` is defined here as `open`, `paid`, `void`. `docs/05` fixes the
  order, payment, subscription, node, instance and operation machines but says nothing
  about invoices, so this is this repository's vocabulary. Extending it is a migration
  because the values are constrained.
- Committing is a separate act from settlement: `ledger_transactions` are append-only
  and a mistake is corrected by an adjustment entry, never by an `UPDATE`. Nothing in
  this phase writes an `UPDATE` to a ledger table.
- Money is in minor units throughout, as `BIGINT`. No floating point appears anywhere
  in the path, and a currency's exponent is a presentation concern that stops at the
  frontend.

## Migration / Compatibility

`0004_commerce` creates thirteen tables: `node_groups`, `products`, `plans`,
`orders`, `order_items`, `payments`, `wallets`, `ledger_transactions`,
`ledger_entries`, `outbox_events`, `subscriptions`, `invoices` and `invoice_items`.
Columns follow `db/schema.sql`; what is added is what the reference leaves implicit:

- `CHECK` constraints for the status columns whose vocabulary this phase can
  state: orders, payments and subscriptions from `docs/05`, invoices from the three
  values named above, products and plans as `draft`/`active`/`archived`, and the
  outbox as `pending`/`published`/`failed`. The same treatment Phase 1 gave
  `users.status`, for the same reason: a vocabulary that is only in a document is a
  vocabulary that will acquire a typo.
- Arithmetic constraints, which the reference leaves implicit: an order total that is
  not subtotal less discount, a line total that is not unit price times quantity,
  `paid_at` disagreeing with a paid status, and an invoice with neither an order nor
  a subscription behind it are all refused by the schema rather than by a convention.
- `node_groups.status` is **not** constrained, deliberately. Phase 4 owns that
  machine and `docs/05` does not define it; guessing another phase's vocabulary here
  would be exactly the cross-phase shortcut the instructions forbid.
- `CHECK (amount_minor >= 0)` on `ledger_entries` is declared by the reference and is
  carried through; the sign lives in `direction`.
- `CHECK (quantity > 0)` on `order_items` and `invoice_items` is declared by the
  reference.
- The partial unique index on `ledger_transactions`, which is the structural half of
  the idempotency decision.
- Indexes on the columns the queries actually filter by: `orders.user_id`,
  `payments.order_id`, `payments.gateway_payment_id`, `ledger_entries.transaction_id`,
  `ledger_entries` by account, `outbox_events` by status and next attempt,
  `invoices.user_id`, `subscriptions.user_id`.

`0004_commerce` is reversible. Its `down` drops the tables in reverse dependency
order. Dropping it discards commercial history, which is why the script says so
explicitly rather than being omitted — the CI integration job rolls the whole history
back, and a migration without a `down` makes that impossible to run.

Greenfield: no products, orders, payments or ledger rows exist.

## Test Plan

- **Unit, no database.** Money arithmetic and the exponent handling; `Ledger.Post`
  refusing an unbalanced set, a single-sided entry, an empty set, a set mixing
  currencies, and a negative amount; the balance projection's sign convention;
  snapshot construction from a plan; order numbering and payment numbering; the fake
  gateway's signature verification, including a tampered body, a wrong secret and a
  re-serialised payload that parses identically but differs in bytes.
- **Integration, `TEST_DATABASE_URL`-gated.** The full history applies forward and
  rolls back; a settlement writes the payment transition, the order transition, the
  invoice transition, the ledger transaction with its entries, the balance projection
  and the outbox row; the partial unique index refuses a second settlement for the
  same payment; an amount mismatch is a conflict rather than a replay.
- **The Gate, `TEST_DATABASE_URL`-gated.** The same callback delivered a hundred
  times — sequentially, and again concurrently — leaves exactly one payable
  transition, one ledger transaction, one pair of entries, one outbox event and one
  invoice transition. Repeated a hundred times must also answer 200 every time, since
  the caller has met its obligation.
- **HTTP, in-process.** The order endpoints behind a user session, the catalog
  endpoints, and the webhook endpoint's signature refusal and replay behaviour.
- **CI composition.** The Gate job already starts the stack; the new tables are proven
  by `migrate` exiting 0 with `schema_migrations` clean, and the Gate asserts the
  thirteen tables and the new constraints exist.
