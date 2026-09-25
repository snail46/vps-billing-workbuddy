#!/usr/bin/env bash
# The acceptance invocation (ADR-015 §6): run every proof the repository
# carries, then print the docs/19 checklist with its evidence pointer.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "== full local verification =="
bash scripts/verify-local.sh

echo
echo "== V1 acceptance checklist (docs/19) =="
cat <<'LIST'
[x] browse -> order -> pay -> provision -> running   backend/internal/httpapi/provision_e2e_test.go
[x] 100 duplicate callbacks, one workflow            backend/internal/httpapi/provision_e2e_test.go
[x] same provider contract, three transports         internal/provider/contracttest + mockprovider/lxdapi/runman suites
[x] migrations forward and back, every version       backend/internal/migrate + CI integration job
[x] identity isolation (user/admin), RBAC per route  backend/internal/httpapi/admin_routes_test.go
[x] ledger balanced per currency, single settlement  backend/internal/commerce, Gate duplicate-callback check
[x] reconciler: drift, stale ops, adoption, agents   backend/internal/reconciler/integration_test.go
[x] admin 2FA enforced at sign-in                    backend/internal/identity/totp_test.go
[x] metrics scrape                                   GET /metrics on the server
[ ] load test                                        NOT MEASURED in V1 (ADR-015 §5)
LIST
echo
echo "acceptance: the local tier is green; the CI tier is the compose Gate."
