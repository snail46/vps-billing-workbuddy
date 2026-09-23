# user-web

Customer-facing web client.

Follow `docs/10-USER-UX.md`, `docs/12-DESIGN-SYSTEM.md`, `docs/13-I18N-SPEC.md`.

```sh
npm run dev --workspace @vps/user-web   # from frontend/
```

## Phase 0 scope

An application shell plus a foundation status screen. `TASKS.md` scopes Phase 0
to the application skeleton and the shared layer; the catalog, checkout, instance
and order screens belong to Phase 8 and must not be pre-built here.

The status screen is not a product feature. It exists to exercise the foundation
end to end with real conditions: a request through the shared API client, the
response envelope and its error model, both locales, the design tokens, and the
`loading` / `loaded` / `empty` / `error` / `partial_error` states docs/10
requires.

## Not yet present

- **Routing.** Deliberately absent: a router wired against a single screen would
  be speculative. It is introduced with the first real pages, in the phase that
  owns them.
- **Authentication.** Phase 1.
