# WorkBuddy — V1 全自动施工提示词

你的父任务是：把当前 VPS 财务计费与资源管理平台仓库完整交付到 V1 Release Candidate。

**父任务未完成时，任何子任务、模块或 Phase 完成都不能结束会话。**
不要在完成一个 TODO、一个模块或一个 Phase 后询问“是否继续”。

### 启动时建立当前事实
先阅读规范，再检查：
- git status / diff / log
- `TASKS.md`
- 当前 build/test
- 已完成代码与文档是否一致

不要只相信勾选状态；必须以“代码 + 测试 + 文档”三者一致作为完成依据。


## 已冻结工程契约
开始前必须重新阅读并遵守仓库中的：
- `AGENTS.md`
- `MASTER_PROMPT.md`
- `TASKS.md`
- `docs/` 全部工程规范
- `docs/adr/`
- `docs/openapi/`
- `docs/schemas/`

技术栈不得擅自改变：
- 后端：Go + chi + pgx/v5 + sqlc + golang-migrate
- 架构：Modular Monolith
- 数据库：PostgreSQL
- 缓存/队列：Redis
- 前端：Vite + React + TypeScript + Tailwind CSS + TanStack Query + i18next
- 应用：`user-web`、`admin-web`、`server`、`worker`
- 语言：`zh-CN`、`en-US`

禁止未经 ADR 引入 GORM、AutoMigrate、Kafka、Kubernetes、Service Mesh 或大规模微服务拆分。

## 核心架构硬约束
- Business PostgreSQL 是唯一商业真相源。
- `Order != Payment != Subscription != Instance`。
- Wallet Balance 是 Ledger Projection；禁止直接修改余额。
- Instance 必须同时维护 `desired_state` 与 `observed_state`。
- 所有长任务必须：`API -> Operation -> Queue -> Worker -> Workflow -> Provider -> 状态同步 -> SSE/Polling`。
- 所有虚拟化后端必须经过 Provider SDK。
- Business Core 不得包含 CLICD/LXDAPI/Runman/Proxmox 专有字段和命令。
- Payment 成功必须同事务完成 Payment、Order、Ledger、Outbox。
- Reconciler 必须处理超时、断线、状态不一致、Worker 崩溃后的恢复。

## Phase 顺序
严格持续完成：
0. Engineering Foundation
1. Identity
2. Commerce
3. Subscription
4. Infrastructure Domain
5. Operation System
6. Provision Workflow
7. Direct Provider
8. User Web
9. Admin Web
10. Runman
11. Resilience
12. Release Hardening

每完成一个 Phase 必须自动：
1. 按 Definition of Done 验收
2. 运行 format/lint/test/build
3. 运行 migration validation
4. 适用时运行 Provider Contract Test / E2E / Failure Test
5. 修复全部阻塞问题
6. 更新 `TASKS.md`
7. 同步 docs/OpenAPI/status registry/ADR
8. 若 Git 环境允许，创建清晰 checkpoint commit
9. **立即进入下一个 Phase，不等待用户确认**

## Provider
先完成 MockProvider 和 Provider Contract Test。
然后至少完成一个真实 Direct Provider（CLICD 或 LXDAPI）。
之后完成 Runman Gateway + RunmanProvider，包括 Agent Auth、gRPC 双向流、Heartbeat、Command、CommandResult、VM 状态、Traffic、NAT、断线重连。
如果第三方 API 不确定，查官方文档/源码，不得猜。能力不支持则 `capabilities=false`，不得伪造。

## UI / UX
- 所有 UI 支持 `zh-CN` / `en-US`，禁止硬编码用户可见文案。
- 所有页面必须有 Loading / Loaded / Empty / Error / Partial Error / Permission Denied。
- 所有点击立即有反馈。
- 长任务必须展示 queued/running/phase/progress/success/failed，并自动刷新。
- 用户前台站在用户视角：状态、连接信息、流量、到期、续费、操作进度。
- 管理后台站在管理员/运维视角：节点健康、Provider、失败任务、Workflow Steps、Trace ID、Raw Error（受 RBAC 控制）。

## 禁止假完成
禁止：
- TODO 代替 V1 核心逻辑
- 返回假成功
- 用静态 Mock 冒充生产实现
- 删除/注释测试换取通过
- 吞掉 error 或空 catch
- 绕过 RBAC / Ledger / Operation / Workflow / Provider SDK / Reconciler
- Handler 直接 SQL 或直接调用 Provider
- 前端直接调用 Provider/Agent
- 直接修改 wallet balance
- 写死 User/Node/Provider
- 硬编码中英文 UI
- Provider 失败却返回成功

## 只有以下情况允许暂停询问
仅限：
- 必须由用户提供真实 API Secret / 商户凭据
- 必须访问当前不可获得的 private repo
- 第三方官方文档与源码存在不可判定重大冲突
- 两种方案会永久改变公开 API/数据模型，现有规范没有裁决
- 涉及不可逆生产操作/数据删除

编译失败、测试失败、lint 失败、migration 失败、前端 build 失败都不是暂停理由，应自行定位修复。

## 高风险验收
必须自动化验证：
- 100 次相同 Payment Callback 只能产生 1 次付款、1 次 Ledger 变化、1 次 Order Paid、1 个 Provision Workflow
- Provider Create 成功但响应丢失时不能重复创建实例
- Worker Crash 后任务可恢复
- Redis Restart 不得永久丢商业事件
- Agent Offline -> Node offline、Instance unknown，不能误判 deleted
- Reinstall 重复提交必须 409，不得生成第二任务
- Admin Balance Adjustment 必须产生 Ledger + Audit

## Final Audit
Phase 12 后继续执行：
- 搜索 TODO/FIXME/HACK
- 搜索硬编码 UI 文案
- 搜索 Handler 直接 SQL/Provider
- 检查未处理 error / 空 catch
- 检查 RBAC、支付幂等、Ledger 平衡、Outbox、Operation Recovery、Reconciler、Capabilities
- 检查 migrations、OpenAPI、i18n、Docker Compose、secret 泄漏
- 完整运行 lint/test/build/E2E/failure tests
发现问题自行修复并重跑。

## V1 FINAL ACCEPTANCE
只有全部满足才能停止：
- Phase 0–12 全部完成
- `TASKS.md` V1 全部完成
- migrations 完整
- OpenAPI 与实现一致
- zh-CN / en-US 完整
- User Web / Admin Web 核心流程完整
- MockProvider Contract Tests 通过
- 至少一个真实 Direct Provider 完成
- Runman Provider / Gateway 完成
- Integration / E2E / Failure Tests 通过
- Docker Compose 可运行
- health checks 正常
- 无已知 P0/P1 blocker
- 无 silent failure
- 无重复收费
- 无重复实例
- 无权限越权
- 无永久丢任务


## WorkBuddy 专用父任务/子任务执行模型
把整个 V1 当成一个父任务，Phase 0–12 是连续子任务：

```text
PARENT: Complete V1
  ├─ Phase 0
  ├─ Phase 1
  ├─ ...
  └─ Phase 12 + Final Audit
```

执行循环：

```text
while parent_task_not_done:
    choose_first_incomplete_or_invalid_task()
    implement()
    validate()
    fix()
    synchronize_docs_and_TASKS()
    if phase_completed:
        run_phase_gate()
        create_checkpoint_if_allowed()
        continue_immediately()
```

不要把“下一步”交还用户。

## 工作方式
- 一次处理可验证的小批次，不要堆大量未经测试的代码。
- 每个批次后尽快跑相关测试。
- 不要跨 Phase 大规模提前开发；只允许建立编译/接口所需最小骨架。
- 已经正确且验收通过的实现不要重做。
- 发现早期 Phase 缺陷时，先修复，再恢复当前 Phase。

## 最终汇报
只有父任务 V1 完成后才统一汇报：
- Phase 0–12 完成情况
- 主要实现
- 数据库/API/Provider
- 前后台
- 测试、E2E、故障注入
- 安全检查
- 遗留的非阻塞项
- V1.1/V2 项
- 启动、部署、初始化和 Provider 配置说明

**现在开始，从第一个实际未完成/未通过验收的任务继续，一直运行到 V1 FINAL ACCEPTANCE。**
