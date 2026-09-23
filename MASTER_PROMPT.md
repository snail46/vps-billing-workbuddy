# MASTER PROMPT

你正在建设一个 **VPS 财务计费与自动化资源管理平台**。

这不是一个临时演示面板，而是一个可靠的 VPS Business Control Plane。

## 用户需要完成
- 注册/登录
- 浏览商品与套餐
- 下单/支付
- 自动开通并看到进度
- 管理 VPS：开机、关机、重启、重装
- 查看连接信息、流量、NAT/网络
- 查看订单、账单、余额
- 续费
- 通知
- 工单

## 管理员/运维需要完成
- 看平台健康状态
- 管理用户、商品、套餐
- 管理订单、支付、Invoice、Ledger
- 管理订阅、实例、节点、Provider
- 定位 Operation/Workflow 失败原因
- 看 Provider 原始诊断
- 管理工单、通知、RBAC、Audit

## 最高原则
1. Business PostgreSQL 是唯一商业数据真相源。
2. Provider 是基础设施实际状态来源，不负责商业归属和计费。
3. 前端不直接调用 Provider。
4. Business Core 不直接执行宿主机命令。
5. 所有底层通过 Provider Contract。
6. 所有长任务通过 Operation + Workflow + Worker。
7. 所有状态必须可见、可解释、可恢复。
8. Order / Payment / Subscription / Instance 必须分离。
9. 资金变动必须 Ledger。
10. 高危/财务管理操作必须 Audit。
11. 用户界面简单；管理后台高可观察性。
12. 内部 English；UI `zh-CN` / `en-US`。
13. V1 模块化单体。

## Provider
Direct Provider：平台主动 API 调用，如 LXDAPI/CLICD。
Agent Provider：Agent 主动连接平台，如 Runman Agent。

Runman 必须通过 Runman Gateway 接入；Business Core 不得感知其专有 Command 名。

## 长操作流程
API 校验
→ 创建 Operation
→ HTTP 202 + operation_id
→ Outbox/Queue
→ Worker
→ Workflow
→ Provider
→ 持续更新 progress/status
→ SSE
→ 成功/失败
→ 必要时 Reconciler

超时不得盲目重复 Create，必须先 Verify。

## 施工顺序
严格按照 `TASKS.md`。
不要为了先展示 UI 而跳过财务、状态机、Operation、MockProvider 等基础设施。

现在先阅读：
- `AGENTS.md`
- `TASKS.md`
- `docs/01-PRD.md`
- `docs/02-ARCHITECTURE.md`
- `docs/04-DATABASE-SCHEMA.md`
- `docs/05-STATE-MACHINES.md`
- `docs/06-PROVIDER-CONTRACT.md`
- `docs/07-WORKFLOW-SPEC.md`
- `docs/08-API-CONTRACT.md`
- `docs/20-AI-IMPLEMENTATION-RULES.md`

从第一个未完成 Phase 开始。
