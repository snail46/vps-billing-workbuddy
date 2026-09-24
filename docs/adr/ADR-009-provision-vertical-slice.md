# ADR-009 — Provision Vertical Slice

Status: Accepted (Phase 6)
Supersedes: nothing. Extends ADR-007 (scheduler/reservation) and ADR-008 (operations).

## Context

The phase's Gate is the user's whole journey: browse the catalogue, place an
order, pay through the fake gateway, and watch the platform provision the
instance automatically — with progress that arrives on its own — until the
instance reports running. docs/07 fixes the provision chain:
`validate_subscription → select_node → reserve_resources → create_instance →
wait_provider → configure_network → verify_running → persist_network →
commit_reservation → activate_subscription → notify → finish`.

## Decisions

1. **The trigger is `subscription.activated.v1`, not the payment event.**
   Phase 3 settles Payment + Order + Ledger + Outbox + Subscription in one
   transaction, and the subscription's own activation event carries everything
   provisioning needs (subscription, user, plan). Provisioning per subscription
   — not per payment — is also what keeps renewals from re-provisioning and a
   multi-line order from collapsing into one instance.

2. **The bridge is an outbox handler, and its idempotency is the operation
   store's key.** The handler for `subscription.activated.v1` enqueues a
   provision operation whose idempotency key is
   `provision:subscription:<subscription_id>`. The acceptance criterion "the
   same callback a hundred times produces one provision workflow" is answered
   by the same property the settlement already uses: the key is unique, the
   retry keeps the operation it already created, and nothing runs twice.

3. **One subscription is one instance.** `instances.subscription_id` is NOT
   NULL in the reference, and the vertical slice keeps that shape: the runner
   reads the plan snapshot's capacity and image from the subscription's plan,
   creates exactly one instance row, and drives it to running.

4. **The runner walks docs/07's chain, with the network steps skipped, not
   deleted.** The mock provider has no network to configure, so
   `configure_network` and `persist_network` are recorded as `skipped` — the
   workflow reached them and decided — which keeps the progress denominator
   identical to the document's chain. When a real provider arrives in Phase 7,
   the steps become live without changing the machine or the progress
   arithmetic.

5. **The reservation is the receipt, and the commit closes it.** The runner
   reserves through the durable receipt (ADR-008 §5) after the scheduler picks
   the node, creates the instance at the provider only after the capacity is
   promised, and commits the receipt — reserved becomes allocated — once the
   instance verifies as running. A provider that dies between create and
   verify leaves a committed row and a retryable operation, and the retry finds
   the instance by its subscription rather than creating a second one: the
   provider-instance identifier's uniqueness (the reference's
   `ux_instance_provider_id`) plus the subscription lookup are the two guards.

6. **Notification is a row and an event, not an email.** `notifications` (the
   reference's table) gets one row per transition the user should see, and
   `instance.provisioned.v1` goes to the outbox. Delivery channels are a later
   phase's problem; the record and the event are the part that must not be
   lost.

7. **The user reads instances through their own surface.** The Gate's last step
   is a user seeing their instance running, so `GET /instances` exists in this
   phase, scoped to the caller, with the machine's state on it.

## Consequences

- The 100-callback Gate assertion extends to provisioning: one operation, one
  instance, one notification — the receipt of everything Phase 2 already
  guarantees, one level higher.
- Phase 7's direct provider replaces the mock behind the same runner, and the
  contract suite it must pass is already in place.
