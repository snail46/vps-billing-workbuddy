# ADR-012 — The Admin Web and the Operator's Surface

Status: Accepted (Phase 9)
Extends: ADR-004 (RBAC), ADR-008 (operation system), ADR-011 (user surface).

## Context

Phase 9's Gate is the operator's console: a dashboard that surfaces anomalies
first, the management lists (users, orders, payments, ledger, subscriptions,
instances, operations, tickets, audit, admins, roles, settings), an instance
detail that can answer "whose machine is this and what has happened to it",
and an operation detail with steps, retries and trace ids. The permission
vocabulary of docs/15 already names what each screen needs; the seed lacks
two keys the screens must declare (`orders.read`, `products.read`), and an
administrative surface that grants nothing is the failure ADR-004 §3 was
written about.

## Decisions

1. **Every screen's read is one permission, declared where the route is
   mounted.** The admin surface keeps the phase 1 rule — no handler without a
   declaration — and extends it: overview reads need `operations.read`
   (the dashboard's job is anomaly discovery), orders fall under the new
   `orders.read`, the catalogue under `products.read`. The two missing keys
   are seeded by migration 0010 and granted on the same shape as their
   siblings; the vocabulary of docs/15 is examples, and a screen that cannot
   name its permission is a screen that cannot be granted.

2. **The dashboard is computed, not collected.** One endpoint, one query
   family: failed operations in flight, nodes offline, providers unhealthy,
   reservations near capacity, and the commerce totals. It is a read of
   current rows — no new table, no cache to go stale, because an operator's
   first screen that lies is worse than one that is slow.

3. **The admin's instance detail is the record joined, not a new truth.** The
   operator screen joins instance, subscription, plan snapshot, user, node,
   provider, network rows, traffic and the audit trail that touched the
   instance. Everything the user surface hides behind "not yours", the admin
   surface shows behind `instances.read` — the join is the same shape with a
   different gate.

4. **Writes stay minimal and permissioned.** Phase 9 adds user
   suspend/activate (`users.suspend`) and the support reply/close
   (`tickets.reply` / `tickets.manage`). Payment refunds, ledger adjustments
   and operations retries remain unbuilt rather than half-built: each is a
   money-or-state machine decision that deserves its own phase gate, and a
   read-only button is an honest absence a console can carry.

5. **Settings and roles are read-only screens in V1.** The tables exist and
   the permissions name them; the mutation flows (role grants, setting
   changes, admin invitations) are administrative writes that would each need
   their own audit trail and confirmation surface. Reading them — with the
   audit list beside — is what an operator needs to detect a misconfiguration,
   which is the admin UX's stated first goal.
