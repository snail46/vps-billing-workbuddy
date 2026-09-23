# admin-web

Operations console.

Follow `docs/11-ADMIN-UX.md`, `docs/12-DESIGN-SYSTEM.md`, `docs/13-I18N-SPEC.md`.

```sh
npm run dev --workspace @vps/admin-web   # from frontend/
```

## Phase 0 scope

An application shell plus a platform status view. `TASKS.md` scopes Phase 0 to
the application skeleton and the shared layer; the health dashboard, user,
product, order, payment, ledger, subscription, instance, node, provider,
operation, ticket and audit screens belong to Phase 9.

## How it differs from user-web

`docs/11` makes the console's first job finding and locating anomalies, so the
same foundational data is presented differently:

- raw per-dependency diagnostic text is shown verbatim, because an operator needs
  the reason rather than a summary;
- latency per dependency is tabulated, at higher information density;
- the shell is a separate deployment from the user client, which
  `docs/14-SECURITY.md` requires for surface isolation.

Both clients share one bundle of i18n resources, API types and design tokens via
`@vps/shared`. No privileged logic lives in the shared package.

## Not yet present

- **Routing.** Deliberately absent for the same reason as in `user-web`.
- **Authentication and RBAC.** Phase 1. Authorisation is always enforced on the
  backend (`docs/14`); this client will only reflect the outcome.
