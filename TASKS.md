# TASKS — V1 Implementation Roadmap

## Phase 0 — Foundation
- [x] Go module / server / worker
- [x] React+TS user-web / admin-web
- [x] shared ui / i18n / api types
- [x] PostgreSQL / Redis / Compose
- [x] health endpoints
- [x] structured logs + request_id + trace_id
- [x] migrations
- [x] CI
**Gate:** 一条 compose 命令启动基础环境。

> **状态：实现完成，Gate 待 CI 实跑，Phase 0 尚未关闭。**
>
> 上述 8 项已实现，并通过 `scripts/verify-local.sh` 在本机验证
> （gofmt / vet / golangci-lint / build / unit test / typecheck / lint / test / build）。
>
> Gate 由 CI 的 `foundation-gate` job **真实执行**：
> `docker compose up -d --build` → 探活 `/health/live` 与 `/health/ready`
> → 校验 `migrate` 退出码与 `schema_migrations` 状态 → 两个前端返回 200 → 拆除。
> 该 job 无 `continue-on-error`，未通过即失败。
>
> **本机无 Docker 且 WSL 被安全策略禁用，因此 Gate 无法在本地执行**；
> 首次 push 触发 CI 并通过后，Phase 0 方可关闭并进入 Phase 1。
> 详见 `docs/adr/ADR-003-foundation-topology-and-gate.md`。
>
> 架构决策：`ADR-001`（后端栈）、`ADR-002`（前端栈）、`ADR-003`（拓扑与 Gate）。

## Phase 1 — Identity / RBAC
- [ ] users/admins
- [ ] independent sessions
- [ ] roles/permissions
- [ ] CSRF/rate limit
- [ ] audit foundation
- [ ] zh-CN/en-US

## Phase 2 — Commerce / Finance
- [ ] products/plans
- [ ] orders/order snapshots
- [ ] invoices/payments
- [ ] wallets/ledger
- [ ] fake payment provider
- [ ] transactional outbox
- [ ] duplicate webhook tests
**Gate:** 同一支付回调重复 100 次只入账一次。

## Phase 3 — Subscription
- [ ] lifecycle
- [ ] renewal
- [ ] due/grace/suspend/cancel

## Phase 4 — Infrastructure Domain
- [ ] provider/node group/node
- [ ] capabilities
- [ ] reservation
- [ ] deterministic scheduler
- [ ] MockProvider

## Phase 5 — Operation System
- [ ] operations/steps
- [ ] queue/worker
- [ ] retries
- [ ] SSE
- [ ] OperationProgress component

## Phase 6 — Provision Vertical Slice
- [ ] paid order → subscription
- [ ] provision operation
- [ ] scheduler/reservation
- [ ] MockProvider create
- [ ] instance running
- [ ] notification
- [ ] E2E
**Gate:** 浏览→下单→假支付→自动开通→自动进度→运行。

## Phase 7 — Direct Provider
- [ ] CLICD 或 LXDAPI Adapter
- [ ] Contract tests
- [ ] timeout/idempotency
**Gate:** 替换 MockProvider 不修改 Business Core。

## Phase 8 — User Web
- [ ] dashboard/catalog/checkout
- [ ] instances/detail/network/traffic
- [ ] operation progress
- [ ] orders/invoices/wallet
- [ ] notifications/tickets/account
- [ ] all page states
- [ ] bilingual

## Phase 9 — Admin Web
- [ ] health dashboard
- [ ] users/products/orders/payments/ledger
- [ ] subscriptions/instances/nodes/providers
- [ ] operations/tickets/audit
- [ ] admins/roles/settings

## Phase 10 — Runman Provider
- [ ] Gateway
- [ ] auth/connection registry
- [ ] heartbeat/command/result/state
- [ ] traffic/NAT
- [ ] reconnect
- [ ] Contract tests

## Phase 11 — Reconciler / Resilience
- [ ] desired vs observed
- [ ] stuck operations
- [ ] expired reservations
- [ ] node heartbeat expiry
- [ ] create-success-but-timeout
- [ ] Redis restart / worker crash

## Phase 12 — Release Hardening
- [ ] security
- [ ] admin 2FA
- [ ] backup/restore
- [ ] metrics/alerts
- [ ] performance
- [ ] upgrade/rollback
- [ ] acceptance suite
