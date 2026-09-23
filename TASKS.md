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

> **状态：已关闭。Gate 已在 CI 真实通过（run #5，commit `5d891a0`）。**
>
> 5 个 job 全部 success，其中 `foundation-gate` 执行序列与耗时：
> `docker compose up -d --build`（**57 秒**）→ `/health/live` → `/health/ready`
> （断言 postgres 与 redis 均 up）→ `migrate` 退出码 0 → `schema_migrations`
> 为 `version=1 dirty=f` → 两个前端 3000/3001 返回 200 → `down -v`。
> 该 job 无 `continue-on-error`。
>
> 关闭前修掉三个真实缺陷（详见各 commit 与 `ADR-003`）：
>
> 1. 集成测试从**日志记录**读 schema 版本，而 job 设 `LOG_LEVEL=warn` 抑制了 INFO →
>    改为从 stdout 读命令**结果**。
> 2. 两个前端镜像**没有 COPY** `frontend/tsconfig.base.json`，而所有 tsconfig
>    `extends` 它 → 镜像内 `tsc` 报 `TS5083` 并级联 `TS6142/TS17004`。
> 3. `backend/Dockerfile` 用 `ENTRYPOINT ["/app/server"]` 而 compose 用 `command`
>    选择二进制（**compose 覆盖 CMD、却追加到 ENTRYPOINT**）→ migrate 容器实际执行
>    `/app/server /app/migrate up`，起来的是 API 且永不退出，所有
>    `service_completed_successfully` 的依赖者永久等待。**全程无任何 error**，
>    表现为静默挂死 24 分钟。
>
> 由此新增两个本地守门（`scripts/check-docker-context.py`、
> `scripts/check-compose.py`，均由 `scripts/verify-local.sh` 调用），
> 并把 Gate 的 compose 命令外包 `timeout 480`、job 级 `timeout-minutes: 20`
> 且加入 `if: always()` 的 digest（取消/超时也会发布耗时归因）。
>
> 本机无 Docker 且 WSL 被安全策略禁用，Gate 只能在 CI 执行；本地项由
> `bash scripts/verify-local.sh` 覆盖。架构决策见 `ADR-001` / `ADR-002` / `ADR-003`。

## Phase 1 — Identity / RBAC
- [x] users/admins
- [x] independent sessions
- [x] roles/permissions
- [x] CSRF/rate limit
- [x] audit foundation
- [x] zh-CN/en-US

> **状态：已关闭。** CI run #14（提交 `b207c47`）五个 job 全绿。
>
> 已由 CI 实证的部分：
> - 迁移在真实 PostgreSQL 上前滚+回滚（`migrations applied, version: 3`），身份表与种子
>   由 Gate 断言（7 张表 / 5 角色 / 27 权限 / 授权行数）。
> - `TEST_DATABASE_URL` 门控的集成测试：身份语句往返、真实唯一约束映射为地址冲突、
>   CHECK 约束不被误报为冲突、`super_admin`/`read_only` 权限集合、审计行落库。
> - `TEST_REDIS_URL` 门控的集成测试：会话往返与过期、跨身份空间拒绝、按 subject 撤销、
>   限流计数与窗口。
> - 端到端（HTTP + 真实 PG/Redis）：注册→登录→`/me`→登出后会话失效、
>   停用即时生效、管理员登录解析权限并写入审计、重复失败登录被限流。
>
> **Phase 1 未定义任何页面**（属 Phase 8/9）；i18n 只补了错误键。
> 决策见 `docs/adr/ADR-004-identity-sessions-rbac.md`。

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
