# 20 — AI Implementation Rules

每个 Feature 开始前回答：
1. 哪个 Domain？
2. 谁能执行？
3. 数据在哪？
4. 是否影响财务？
5. 是否需要 Operation？
6. 是否需要 Audit？
7. 是否涉及 Provider？
8. 失败如何恢复？
9. 用户看什么？
10. 管理员看什么？

Definition of Done：
backend、frontend（如可见）、zh-CN/en-US、页面状态、Audit/Metrics（如适用）、测试、API schema、无关键 TODO。

禁止：
删测试、吞错误、假成功、Mock 冒充生产、硬编码 ID、绕过 Ledger/RBAC/Operation、前端直连 Provider、未核对第三方 API 就猜。

核心架构变更必须 ADR。
