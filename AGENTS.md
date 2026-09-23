# AGENTS.md

本文件对所有代码 Agent 具有最高工程执行优先级。

## 开始任何任务前
1. 阅读 `MASTER_PROMPT.md`。
2. 阅读 `TASKS.md`。
3. 阅读当前任务涉及的 `docs/`。
4. 涉及 DB：读 `docs/04-DATABASE-SCHEMA.md` + `db/schema.sql`。
5. 涉及异步任务：读 `docs/05-STATE-MACHINES.md` + `docs/07-WORKFLOW-SPEC.md`。
6. 涉及 Provider：读 `docs/06-PROVIDER-CONTRACT.md` + `backend/internal/provider/provider.go`。
7. 涉及前端：读用户/后台 UX、Design System、i18n。
8. 涉及权限：读 Security/RBAC。
9. 核心架构变更必须先写 ADR。

## 不得改变的设计
不得擅自：
- 合并 Order / Payment / Invoice / Subscription / Instance。
- 删除 Ledger、Operation、Workflow、Provider Layer、Reconciler、Audit。
- 让前端直接调用 Provider。
- 让 HTTP Handler 直接调用 Provider 或宿主机 shell。
- 使用 Provider 数据库作为商业真相源。
- 直接改 Wallet balance 绕过 Ledger。
- 把长任务做成同步阻塞 HTTP。
- 在业务层散落 `if provider == xxx`。
- 为通过测试而删测试、吞错误、返回假成功。

## 语言
内部：English。
UI：`zh-CN` + `en-US`。
禁止硬编码用户可见中文/英文。

## 异步操作
创建、删除、开关机、重启、重装、密码重置、NAT 端口变更：
- 创建 Operation
- HTTP 202
- 返回 operation_id
- Worker/Workflow 执行
- SSE 更新
- 保存 error_code/trace_id
- 支持安全 retry/reconcile
- 禁止 silent failure

## 财务
- 金额使用 minor unit BIGINT。
- Payment webhook：验签、金额、币种、订单、幂等。
- Payment + Order + Ledger + Outbox 同事务。
- Ledger 历史不可 UPDATE；纠错新增 adjustment。
- 同一 webhook 重复 100 次只允许入账一次。

## Provider
新增 Provider 必须：
- 实现 Provider Contract
- Health + Capabilities
- 状态映射
- 错误标准化
- 幂等
- Contract Tests
- 不支持能力返回 `UNSUPPORTED_OPERATION`

## 前端
所有页面/核心卡片考虑：
- loading
- loaded
- empty
- error
- partial_error
- permission_denied

用户点击后 <100ms 必须有视觉反馈。
异步请求 <1s 应得到 Operation ID。

## 任务结束前
运行受影响范围：
- format
- lint
- unit tests
- integration tests
- frontend typecheck（如涉及）
- build

报告：
1. 修改文件
2. 实现内容
3. 测试结果
4. 兼容/迁移影响
5. 已知风险
