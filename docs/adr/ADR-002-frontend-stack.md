# ADR-002 — Frontend Stack

Status: Accepted
Date: 2026-09-23

## Context

`TASKS.md` Phase 0 requires **two** React + TypeScript applications plus a shared
layer:

```
- [ ] React+TS user-web / admin-web
- [ ] shared ui / i18n / api types
```

The two applications have different audiences and different success criteria:

- `docs/10-USER-UX.md` — User Web answers five questions within five seconds and
  must stay simple; the user "不关心 Provider/Agent/Workflow".
- `docs/11-ADMIN-UX.md` — Admin Web optimises for finding anomalies, explicitly
  allows higher information density, and must surface raw provider diagnostics.

What they share is substantial and must not be duplicated: the i18n setup, the
Design System tokens, the API envelope types, and the page-state components.

Non-negotiable requirements from the docs:

- `docs/13` — every user-visible string is i18n'd, including **statuses, errors,
  toasts, notifications and progress**; money and datetime format per
  locale/timezone; internal keys stay English.
- `docs/12` — semantic colours (green success / blue info / yellow waiting /
  red danger / gray inactive), and **colour must never be the only status cue**.
- `docs/10`, `docs/11` — every page and core card handles `loading`, `loaded`,
  `empty`, `error`, `partial_error`, `permission_denied`.
- `AGENTS.md` — user interaction must produce visual feedback in <100 ms; async
  requests must yield an Operation ID in <1 s; no hardcoded UI strings.

`docs/10` also requires that after a page refresh an in-flight async operation is
**restored**, which means the progress UI cannot live in component state alone.

## Decision

**Vite + React + TypeScript** single-page applications, two of them, over a
shared npm-workspaces package.

| Concern | Choice | Rationale |
| --- | --- | --- |
| Build tool | Vite | Fast HMR, trivial multi-app setup, first-class `tsc` typecheck integration. |
| UI runtime | React 19 + TypeScript (strict) | Mandated by `TASKS.md`. || Styling | Tailwind CSS v4 | `docs/12` defines a small semantic token set, not a component zoo. Tailwind's CSS-first `@theme` maps design tokens 1:1 and keeps them in one file both apps import. |
| Server state | TanStack Query | Gives the six page states a single correct implementation (per-query `isPending`/`isError`, `placeholderData` for `partial_error`, retry policy) instead of six hand-rolled variants. Its cache is also what lets a refresh restore an in-flight operation. |
| i18n | i18next + react-i18next | Namespaced keys (`instance.status.running`, `operation.reinstall.preparing`) exactly as `docs/13` prescribes, with interpolation for `parameters` payloads from notifications. |
| Routing | react-router | Standard; nested layouts per app. |
| API access | `fetch` wrapper in the shared package | The envelope in `docs/08` is fixed and small; a generated client would add a codegen step for no benefit in Phase 0. Shared types keep both apps honest. |

### TypeScript version constraint

TypeScript is pinned to `^6.0.3` rather than to the current latest (`7.0.2`).

`typescript-eslint` currently declares `typescript >=4.8.4 <6.1.0`, and no
released version accepts TypeScript 7. Adopting 7 would mean either installing
with `--legacy-peer-deps` — accepting a resolution the linter itself says is
unsupported, so the type-aware rules could fail or misreport — or dropping
type-aware linting entirely. Neither is worth it for a language version this
project gains nothing from yet.

6.0.3 is a released stable version inside the supported range, so the whole
toolchain stays on its supported path. When `typescript-eslint` widens its peer
range, raising this pin is a one-line change in four `package.json` files.

### Repository layout

```
frontend/
  package.json          npm workspaces root: shared, user-web, admin-web
  shared/               @vps/shared
    src/api/            envelope types, ApiClient, error-code → i18n key mapping
    src/i18n/           i18next init + locales/{zh-CN,en-US}
    src/ui/             design tokens + page-state primitives
  user-web/             consumes docs/10, docs/12, docs/13
  admin-web/            consumes docs/11, docs/12, docs/13
```

Both applications depend on `@vps/shared` via npm workspaces — no publishing, no
version skew, one `npm ci` at the root.

### Status rendering

`docs/12` forbids colour-as-only-signal and `docs/13` requires statuses to be
translated. Therefore the shared package owns a single `StatusBadge` that pairs a
**locale key + label text + icon/shape + semantic colour**. New statuses are
added to the shared status registry, not re-implemented per page. This is the
frontend counterpart of the capability-driven rule in `docs/06`.

## Alternatives

1. **Next.js (App Router), two apps.** Better SSR/SEO and file-convention
   routing. Rejected: this is an authenticated control plane behind a login;
   SEO carries no value, while SSR adds a Node runtime, a second caching layer,
   and server/client component boundaries to reason about for no domain benefit.

2. **Single SPA with an `/admin` route tree.** Least code, one build. Rejected:
   `docs/14-SECURITY.md` requires the ordinary user, admin, and
   Agent/Provider credential surfaces to be **completely isolated**. Separate
   bundles mean a bug or dependency compromise in the user app cannot expose
   admin routes; the shared package contains no privileged code.

3. **Ant Design.** Components are ready-made and admin density comes free.
   Rejected: `docs/12` defines its own compact semantic palette and a specific
   shared-component list, so a large component library would be fought rather
   than used, and its themed defaults would fight the "colour is not the only
   status cue" rule.

4. **A shared component library published to a registry.** Cleaner boundaries at
   scale. Rejected for V1: publishing overhead with two consumers; npm
   workspaces deliver the same single-source-of-truth without a release process.

## Consequences

Positive:

- The six page states, i18n, and status rendering exist exactly once.
- Separate bundles satisfy the isolation requirement in `docs/14`.
- Tailwind v4 tokens are the single place the Design System is expressed, so
  `docs/12` changes are one-file changes.

Negative:

- npm workspaces + Tailwind v4 + Vite requires correct config in three
  `package.json` files and a shared `@theme`; a mistake surfaces as missing
  styles rather than a build error.
- Two dev servers and two builds to maintain.
- Bundle output is duplicated for the shared layer in both apps (acceptable;
  the shared layer is small).

## Migration / Compatibility

Greenfield: no existing frontend. `TASKS.md` Phase 0 explicitly scopes this to
the app shell plus the shared layer — **no business pages**. Catalog, checkout,
instances and admin screens belong to Phase 8 and Phase 9 and must not be
pre-built here.

`frontend/.npmrc` pins the registry to a China-accessible mirror. This is a
local-environment necessity (see ADR-003); it is a workspace-scoped file and
does not affect any other project on the machine.

## Test Plan

- `npm ci` at `frontend/` installs all three workspaces.
- `npm run typecheck` — `tsc --noEmit` in strict mode, all three packages.
- `npm run lint` — ESLint across all three packages, with a rule failing on
  hardcoded JSX text literals so `docs/13` is enforced mechanically rather than
  by review.
- `npm run build` — both applications produce production bundles.
- A parity test asserts `zh-CN` and `en-US` expose an identical key set, so a
  missing translation fails CI instead of silently falling back.
