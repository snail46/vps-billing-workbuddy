# ADR-007 — Infrastructure domain: scheduling, reservation and the provider seam

Status: accepted (Phase 4)
Complements: ADR-005 (money), ADR-006 (subscription)

## The problem

The provisioning chain (`docs/07`) begins `validate_subscription → select_node →
reserve_resources`, and the reference schema carries `providers`, `node_groups`
and `nodes` — the last with paired *_total / *_allocated / *_reserved capacity
columns and no reservations table. What none of it fixes: which node a plan
lands on, what a reservation is when there is no table for one, what statuses
the three tables may hold (0004 deliberately left `node_groups.status`
unconstrained for this phase), and how a provider implementation proves it
honours the contract before it touches a customer.

## Decisions

### 1. Status vocabularies

`nodes.status` takes `docs/05`'s node machine: `online / degraded / draining /
maintenance / offline`. `providers.status` and `node_groups.status` have no
machine in any document — they are on/off switches for whether the platform
reads from and schedules onto them — so both are `active / disabled`. All three
become CHECK constraints in 0006, the treatment every other vocabulary in this
repository has had.

### 2. A reservation is a transition on the node row, not a table

The reference defines no reservations table, and the columns it does define —
three paired counters per node — say what a reservation is: capacity moved from
"free" to "promised" until the promise is kept or withdrawn.

- **Reserve**: one transaction, node row locked, conditional on the free
  capacity actually covering the request (`total - allocated - reserved >=
  spec`). The loser of a race re-reads and fails cleanly — two concurrent
  reservations cannot both promise the same vCPU.
- **Commit**: allocated += spec, reserved -= spec, in the transaction that
  records the instance the promise became.
- **Release**: reserved -= spec, when creation fails or is cancelled.

A reservation therefore has no identity of its own to persist, and its
idempotency rides on the operation record that drives it (Phase 5): the
workflow's step state says whether reserve already ran, the way the settlement
gate says whether a payment already settled. A reserve whose process died
between bump and commit leaks reserved capacity — and that leak is exactly what
the Reconciler (docs/02) exists to rebuild from in-flight operations, not
something this phase papers over with a second bookkeeping table whose only job
would be to disagree with the operation record.

### 3. The scheduler is deterministic, and that is testable

`select_node(group, spec)` picks among the group's `online` nodes that support
the spec's virtualization and have the free capacity, ordered by:

1. load ratio — allocated ÷ total, lowest first (packing balance, not bin
   packing: the platform spreads load rather than filling racks),
2. weight — higher first, the operator's hand,
3. id — so two nodes tied on every measure have a stable order and the same
   database state always yields the same answer.

Determinism is the requirement: a reconcile that re-runs selection on unchanged
state must not choose a different node, and a test can assert the choice
without seeding a timing race.

### 4. Capabilities drive, names never match

`docs/06` forbids scattered `if provider == xxx`. The seam is the
`provider.Provider` interface plus the `Capabilities` struct it reports; a node
carries its own capability set, the scheduler consults it, and a plan whose
virtualization a node cannot run simply never matches. A capability that
matters to a flow is checked by reading `Capabilities`, never by comparing a
provider's name.

### 5. The contract test is the seam's proof, and MockProvider is its first tenant

`internal/provider/contracttest` runs one suite against any `provider.Provider`
factory: health, capabilities, image listing, create (and its idempotency — the
same key returns the same instance), the lifecycle actions, usage, traffic, and
port forwards — asserting the error codes `docs/06` fixes for the failure paths
each test provokes. `internal/provider/mockprovider` implements the interface
in memory, records its calls, and is configurable to fail on demand (later
resilience tests will need a provider that times out). Phase 7's direct
provider passes the same suite against the real thing; a provider that skips
the suite has not proven anything about the contract.

### 6. Read-only administrative surface now; CRUD when there is a consumer

Phase 4 mounts `GET /admin/providers`, `GET /admin/node-groups` and
`GET /admin/nodes` under the permissions the seed already grants
(`providers.read`, `nodes.read`), because the scheduler and the workflows need
the data to be real and inspectable. Creating providers and nodes is an
operator act with credentials attached (`credential_ref`, `config`) — it lands
with the admin web that owns forms for it, not as an API a script must get
exactly right on the first try.

## Consequences

- The provision workflow (Phase 6) composes this phase's three primitives —
  select, reserve, commit — and owns none of their logic.
- `docs/18`'s unit-test list names the scheduler; its determinism makes those
  tests table-driven rather than probabilistic.
- The capacity counters are denormalisation, and the reconciler is their
  audit: allocated is rebuildable from instances, reserved from in-flight
  operations, exactly as the wallet projection is rebuildable from the ledger.
