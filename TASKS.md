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
- [x] products/plans
- [x] orders/order snapshots
- [x] invoices/payments
- [x] wallets/ledger
- [x] fake payment provider
- [x] transactional outbox
- [x] duplicate webhook tests
**Gate:** 同一支付回调重复 100 次只入账一次。

> **状态：已关闭。** CI run #17（提交 `3cc55d3`）六个 job 全绿，Gate 在真实
> PostgreSQL + Redis 上实证通过。
>
> 结构：`internal/money`（小写金额 + 币种）、`internal/ledger`（复式记账 + 平衡校验 +
> 投影接口）、`internal/commerce`（状态机 / 订单快照 / 结算）、
> `internal/payment` + `internal/payment/fakegateway`（网关端口与假网关）、
> `internal/storage/commerce`（sqlc 适配器）。
>
> **Gate 测试**（`internal/httpapi/commerce_e2e_test.go`，CI-only）：同一回调
> 顺序 100 次 + 并发 100 次，断言**恰好一次**订单支付、发票支付、
> ledger 交易（两条平衡分录）与 outbox 事件；伪造回调（改金额 / 改状态，以
> 攻击者密钥正确签名）被拒且不动账。
>
> 结算是**一次条件 UPDATE**（`status IN ('pending','processing') RETURNING`），
> 另有三道唯一约束兜底（幂等键、`(gateway, gateway_payment_id)`、
> ledger 交易引用的部分唯一索引）。
> 决策见 `docs/adr/ADR-005-commerce-money-and-ledger.md`。
>
> run #16 暴露并修掉三个真实缺陷（详见 commit `3cc55d3`）：
>
> 1. `mapWriteError` 把 nil 也包装 → 每次成功的发票/支付写入被报为失败 →
>    回滚 → 500。现在 nil→nil，并有单测守门。
> 2. webhook 路由挂成字面量、处理器读 `{gateway}` 参数 → 所有回调 404。
>    改挂 `/webhooks/payments/{gateway}`；契约 walk 扩为全量装配后顺带抓到
>    OpenAPI 里 `/products` 重复键。
> 3. `internal/migrate` 的可逆性测试对**共享**集成库执行 `Down(1)`，而
>    `go test ./...` 各包并发 → 商业表在 Gate 测试中途被删。改为在自建
>    临时 probe 库里证明可逆性。
>
> 本地调试设施：便携 PostgreSQL（zonky 二进制）+ miniredis，可在无 Docker 的
> 本机复现整套集成测试（redisx 的实时过期测试除外——miniredis 语义差异，
> CI 真实 Redis 通过）。

## Phase 3 — Subscription
- [x] lifecycle
- [x] renewal
- [x] due/grace/suspend/cancel

> **状态：已关闭。** CI run #19（提交 `66592a4`）全绿，生命周期与并发恰一次续费
> 在真实 PostgreSQL + Redis 上实证通过。
>
> `docs/05` 只给词表不给边，`ADR-006` 固化了转移表与续费机制：
> 订阅在**订单结算的同事务内**诞生并激活（Payment+Order+Ledger+Outbox+Subscription）；
> 续费由 **sweep**（`Sweep(now)`，纯函数、时钟可注入）驱动：周期结束开续费发票 →
> **钱包余额**结算（网关支付按 schema 绑定订单，不适用）→ 同事务延长周期；
> 未付逾期 → past_due（宽限 72h）→ suspended（14 天后 expired）；
> `cancel_at_period_end` 在开账前被兑现；terminated 仅限管理员
> （首个**权限门控**管理端点，`subscriptions.terminate` 由 0005 种子）。
>
> 重复性由 schema 回答（与 Phase 2 同一纪律）：每订阅**至多一张开放续费发票**
> （部分唯一索引）→ 并发 sweep 收敛到同一张账单；发票条件转移仲裁付款人；
> past_due/cancelled 转移额外要求周期确实已结束（防陈旧快照把刚续费的订阅打标）。
> 钱包闸门是 `SELECT ... FOR UPDATE` + 余额检查，投影由分录**单次**维护
> （初版的预扣写法会把同一笔借记入账两次——生命周期测试当场抓住）。
>
> 0005：sweep 索引 + 开放续费发票唯一索引 + 2 个订阅权限；Gate 断言同步扩展。
> OpenAPI 5 条新路径 + Subscription schema；i18n 双语错误键。
> 事件：`subscription.activated/renewed/past_due/suspended/expired/cancelled/terminated.v1`。
>
> 本地验证：生命周期全套（出生/钱包续费/逾期/宽限/暂停/过期/取消/并发恰一次/
> 手动续费/终止）在真实 PostgreSQL 上全绿；全仓并发套件通过（redisx 实时过期
> 测试仍为本地唯一分歧，CI 真实 Redis 通过）。verify-local 32 项全绿，lint 0 issues。

## Phase 4 — Infrastructure Domain
- [x] provider/node group/node
- [x] capabilities
- [x] reservation
- [x] deterministic scheduler
- [x] MockProvider

> **状态：已关闭。** CI run #22（提交 `97d81f9`）全绿：0006 在真实 PostgreSQL 上
> 前滚+回滚，15 表 / 7 约束 / 6 索引断言通过，预留并发测试实证。
>
> 关键发现：`providers` 与 `nodes` 表**从未被创建**——0004 的 13 张表只是
> FK 目标集，Gate 的 13 表断言也是证据。0006 补齐两表 + 三个状态词表 CHECK
> （nodes 用 docs/05 的机器；groups/providers 是 on/off 开关）+ 容量约束
> （allocated/reserved 不可负、合计不超容量）。
>
> **ADR-007**：预留 = node 行上的条件转移（无 reservations 表——参考 schema
> 刻意没有，泄漏的 reserved 由 Reconciler 从在途 operation 重建）；调度器
> **确定性**（负载比 → weight → id），同样的库状态必选同样的节点；
> 能力驱动（Capability）而非 provider 名称判断；契约测试套件
> （`provider/contracttest`）是任何 Provider 实现的必经检验，
> MockProvider 是第一个租户，Phase 7 的直连 Provider 复用同一套。
>
> 管理面只读（providers/node-groups/nodes 列表，权限门控），写入随 admin web。
> Gate 断言同步：tables 13→15、constraints 6→7、indexes 5→6。

## Phase 5 — Operation System
- [x] operations/steps
- [x] queue/worker
- [x] retries
- [x] SSE
- [x] OperationProgress component

> **状态：已关闭。** CI run #25（提交 `e3cbcc9`）五 job 全绿：0007 在真实
> PostgreSQL 上前滚+回滚，引擎生命周期集成测试与预留回执测试实证通过。
>
> **ADR-008**：数据库即队列（`FOR UPDATE SKIP LOCKED` 认领，N worker 互不等待）；
> **引擎独占状态机**——执行上下文不提供状态转移方法，Run 返回 nil 即引擎收口
> succeeded，错误按 `StepFailure.Retry` 分类 + 指数退避（2s 起步封顶 60s）；
> 进度**只**由 `SyncOperationProgress` 从 steps 推导（phase setter 连 progress
> 参数都没有，docs/07 的"不按时间伪造"由 schema 强制）。
>
> **预留修正（ADR-008 §5 修正 ADR-007 §2）**：参考 schema 确有
> `resource_reservations` 表——预留是**持久化回执**（按 operation 键控、带
> expires_at、部分唯一索引保证每操作至多一张开放回执），计数器与回执同事务。
> 死 worker 的容量承诺由 sweep 释放（SKIP LOCKED 批量认领）。
>
> **Outbox 投递**（Phase 2 ADR-005 移交给本阶段）：按注册的 event type 认领
> 到期事件，无 handler 的事件类型留在 pending（丢弃即静默失败）；
> 失败按操作退避重试，消费者按 event_id 去重。
>
> SSE：`GET /admin/operations/{id}`（记录+steps）与
> `GET /admin/operations/events?operation_id=`（按秒读行、变更才推、
> 终态即断流），docs/09 信封 `operation.updated.v1`。
> 前端 `OperationProgress`（shared/ui）：状态徽标+派生进度条+步骤清单，
> 双语键齐全，等待态=warning、取消=neutral。
>
> 0007 修订（pre-Gate）：加 `run_after` 列承载退避（updated_at 每写都动，
> 两个语义会互相污染——曾致退避完全失效）；retrying 允许携带 error_code。

## Phase 6 — Provision Vertical Slice

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
