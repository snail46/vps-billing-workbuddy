# server

The Business API process.

```sh
go run ./cmd/server
```

## Responsibilities

- Serves the HTTP surface: `/health/live`, `/health/ready`, and the product API
  under `/api/v1` (docs/08-API-CONTRACT.md).
- Owns the middleware chain: correlation identifiers, access logging, panic
  recovery, request timeout, security headers and CORS. The order is load-bearing
  and is documented in `internal/httpapi/router.go`.

## Explicit non-responsibilities

- **No host commands.** The platform never executes shell commands from the
  business layer (docs/02-ARCHITECTURE.md).
- **No direct Provider calls.** Infrastructure work is expressed as an Operation
  and executed by the worker through the Provider Contract.
- **No schema changes.** Migrations are applied by `cmd/migrate` as an explicit
  deployment step (docs/17-DEPLOYMENT.md), never on boot.

## Failure behaviour

The process refuses to start if configuration is invalid or if PostgreSQL or
Redis is unreachable. Starting anyway would mean serving traffic that cannot
work, and the readiness probe exists precisely to prevent that.
