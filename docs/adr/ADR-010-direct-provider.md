# ADR-010 — The Direct Provider (LXD API)

Status: Accepted (Phase 7)
Extends: ADR-007 (provider port), ADR-009 (provision chain).

## Context

The Gate for this phase: replace the MockProvider without modifying the
business core. docs/06 fixes the interface, the error vocabulary, and the two
rules that survive every adapter: Create must be idempotent, and after a
timeout the platform verifies rather than creates again.

## Decisions

1. **LXD's REST API is the target.** `direct` providers speak LXD's
   `/1.0` JSON surface — instances, images, state, proxy devices — over HTTP
   with a token credential. LXD is async by design: every mutation returns an
   operation the adapter waits on, and the wait carries its own timeout so a
   slow provider parks the operation in `waiting_provider` (the machine's own
   state) instead of blocking a worker.

2. **The instance's provider-side name IS the idempotency dimension.** LXD has
   no idempotency keys; it has unique instance names. The adapter derives the
   LXD name deterministically from the platform's idempotency key, so a
   retried create meets "instance already exists" — which the adapter answers
   as the same accepted create it answered the first time, with the same
   operation identifier. The provider's uniqueness constraint and the
   platform's are the same property seen from two sides.

3. **Errors map to docs/06's vocabulary at the boundary, once.** Timeouts are
   `PROVIDER_TIMEOUT`, transport failures `PROVIDER_UNAVAILABLE` — both
   retryable, because the operation engine's backoff is what they are for.
   LXD's 404 on an instance is `INSTANCE_NOT_FOUND`, its auth failures are
   `PROVIDER_AUTH_FAILED`, and what LXD cannot do at all — password reset via
   the API — is `UNSUPPORTED_OPERATION`, stated rather than faked.

4. **The contract suite is the only proof that counts.** The suite Phase 4
   wrote against the mock is the suite the LXD adapter passes — against a
   fake LXD server that speaks the real API's shapes, so the adapter's
   transport, envelope parsing, error mapping and idempotency are all
   exercised. The business core is not imported: the adapter depends on
   `internal/provider` alone, and the Gate's demonstration is the provision
   chain running against the LXD adapter through the same runner the mock
   used.

5. **Network steps come alive with the adapter's devices.** LXD's proxy
   devices are the port-forward surface: add is a device on the instance,
   delete removes it, list reads them back. `instance_networks` rows and the
   two skipped steps land with this adapter, and the progress denominator
   does not move — the steps were already counted.
