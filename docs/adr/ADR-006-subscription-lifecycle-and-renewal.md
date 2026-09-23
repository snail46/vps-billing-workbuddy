# ADR-006 — Subscription lifecycle, renewal and the sweep

Status: accepted (Phase 3)
Supersedes: none
Complements: ADR-004 (identity), ADR-005 (money and ledger)

## The problem

`docs/05` gives the subscription a vocabulary — `pending / active / past_due /
suspended / cancelled / expired / terminated` — and no edges. `docs/07` gives the
renewal chain as words: `invoice → payment → ledger → extend subscription →
update due → if suspended set desired running`. `db/schema.sql` gives the columns:
`current_period_end`, `next_due_at`, `grace_until`, `cancel_at_period_end`,
`version`. None of it says what makes one state become another, who drives the
change, or what happens when two drivers race. A state machine whose edges are
unwritten is a state machine each future caller draws for itself.

## Decisions

### 1. The edges

```
             settlement of the order that bought the plan
   (none) ───────────────────────────────────────▶ active

   active   ── period ends, renewal invoice unpaid ──▶ past_due
   past_due ── renewal paid within grace ───────────▶ active
   past_due ── grace_until passes, still unpaid ────▶ suspended
   suspended ── renewal paid ───────────────────────▶ active
   suspended ── expiry window passes, still unpaid ─▶ expired
   active / past_due ── cancel_at_period_end, period ends ─▶ cancelled
   active / past_due / suspended ── admin terminates ─────▶ terminated

   cancelled / expired / terminated are terminal.
```

`pending` is part of the reference vocabulary and the column's CHECK, and this
phase does not use it: a subscription is born from a settled order, and a row
that exists before its money arrived would need a second activation path and a
second thing to forget. If a later phase needs a pre-payment subscription
(reservations, trials), the state is already legal and this decision is the one
to reopen — not the schema.

### 2. Born at settlement

Buying a plan is an order; when that order's payment settles, the subscription
comes into existence **inside the same transaction** — Payment + Order + Ledger +
Outbox + Subscription (ADR-005's one-transaction rule extended by one writer).
`started_at` and `current_period_start` are the settlement instant; the period
length comes from the plan's `billing_cycle`; `next_due_at` is the period's end.

The alternative — creating a `pending` row at order time and activating it at
settlement — spends a second write and a second code path to record a state the
customer never observes, and makes "order settled but subscription missing" a
state the platform can be in.

### 3. Period arithmetic is calendar-aware

`monthly` adds a calendar month, `quarterly` three, `semiannually` six,
`annually` twelve — `AddDate(0, n, 0)`, so January 31st renews into February 28th
rather than into a March date that skips a month of service. A fixed 30-day month
would silently drift a subscription's anniversary away from the day the customer
chose.

### 4. Renewal is sweep-driven, and the money comes from the wallet

`payments.order_id` is NOT NULL: a gateway payment is bound to an order. A
renewal has no order — it has an invoice and a subscription — so the gateway
path cannot carry it without either bending that column or minting shadow
orders. The wallet path can: a renewal invoice is settled by a ledger movement
(debit the customer's wallet, credit revenue), recorded with
`reference_type='invoice'`, and the wallet projection follows the entries in the
same transaction, exactly as ADR-005 prescribes.

The **sweep** drives the calendar. Given a `now`, it:

1. opens one renewal invoice per subscription whose period has ended and which
   has neither an open renewal invoice nor `cancel_at_period_end`;
2. attempts to pay it from the wallet immediately, and on success extends the
   period and returns the subscription to `active`;
3. marks subscriptions `past_due` whose period ended and whose invoice is
   unpaid, while `now < grace_until`;
4. marks `past_due` subscriptions `suspended` once `grace_until` passes;
5. marks `suspended` subscriptions `expired` once the expiry window passes;
6. cancels subscriptions whose `cancel_at_period_end` is set when their period
   ends — before any invoice is opened, because a cancelled subscription must
   not generate a bill.

The sweep is a **pure function of the records and `now`**: it takes the instant
as an argument, so it is testable without a clock and runnable by whichever
worker Phase 5 ships. The periodic loop is not this phase's.

### 5. Timing policy, stated as constants

Grace is **72 hours** past the period's end; expiry is **14 days** after
suspension. They are constants in the domain, next to the machine they belong
to, with the reasoning that a renewal window is a policy the product chose, not
a number the schema guessed. Making them per-plan columns is a product decision
that has not been made; encoding it before it is made would be guessing.

### 6. One writer per subscription, enforced by the schema

The sweep can run concurrently with itself (a retry, a second worker, a test) —
"two sweeps, one renewal" is the same class of property the Gate of Phase 2
asserts for callbacks, so it gets the same kind of answer:

- **One open renewal invoice per subscription**: a partial unique index on
  `invoices (subscription_id) WHERE subscription_id IS NOT NULL AND status =
  'open'`. Two concurrent sweeps can both try; one commits, one reports 23505.
- **One settlement per invoice**: the ledger's unique reference index
  (ADR-005) already refuses a second `reference_type='invoice'` transaction.
- **One extension per invoice**: the extension is gated on a conditional UPDATE
  of the invoice's status inside the same transaction as the ledger movement and
  the period extension, so the payer of record and the extender are the same
  caller.
- **One spend past the balance**: the wallet debit is gated on a conditional
  UPDATE — `SET available_balance_minor = available_balance_minor - $n WHERE
  available_balance_minor >= $n` — so two concurrent renewals cannot spend a
  balance that backs one. The projection stays an inside-the-transaction write;
  the ledger records what the gate allowed.

### 7. Cancellation is at period end; termination is administrative

V1 offers one customer-facing cancellation: `cancel_at_period_end`, honoured by
the sweep before it opens the next invoice. The customer has paid for the
current period; immediate cancellation would either owe them money or take
service they own, and both are refund/adjustment questions the ledger already
has a vocabulary for, in a later phase if the product asks.

`terminated` is reached only by an administrator, through a permission-guarded
route (`subscriptions.terminate`, seeded with this phase). It is the answer to
abuse and to chargebacks, not a customer control — which is why it is not on the
user surface.

### 8. The records stay separate; the package does not split

`AGENTS.md` forbids merging the Order / Payment / Invoice / Subscription
**records**, and they remain four tables. In code, the subscription machine
lives in `internal/commerce` beside them, for one reason: activation happens
inside the settlement transaction, and renewal opens invoices and moves ledger
money. Splitting the package would put one transaction's writers behind two
Store interfaces and two adapters, and the transaction is the property that
matters.

### 9. Events

Every transition writes an outbox row in the same transaction as the change:
`subscription.activated.v1`, `subscription.renewed.v1`, `subscription.past_due.v1`,
`subscription.suspended.v1`, `subscription.expired.v1`,
`subscription.cancelled.v1`, `subscription.terminated.v1` — docs/09's versioning
rule, applied the way `payment.succeeded.v1` already is.

## Consequences

- The provisioning workflow (Phase 6) reads `subscription.activated.v1` and never
  polls the subscription table; a subscription that exists is one whose money
  arrived, because rows are born settled.
- `docs/07`'s "if suspended set desired running → reconcile" is Phase 6's part of
  the renew chain; this phase stops at the subscription's own state and the
  event, which is the handover point the workflow spec defines.
- The `version` column stays reserved for optimistic concurrency at the API
  boundary (a customer editing a subscription while the sweep runs); the
  machine's own writers are serialised by the conditional updates above, and
  adding `version` checks on top would guard a write path that does not exist
  yet.

## Alternatives considered

- **Gateway-payable renewals** (shadow order per renewal): rejected — it mints
  an order that is not an order to satisfy a column, and the wallet is the
  balance the dashboard already promises.
- **Cron in the database** (pg_cron / a scheduled statement): rejected — the
  sweep's transitions write outbox rows and ledger entries through the domain;
  SQL alone would rebuild the machine outside it.
- **Per-plan grace windows**: deferred until the product asks; a constant is a
  decision, a column is a commitment.
