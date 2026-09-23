# 09 — Event Contract

事件必须版本化，例如 `payment.succeeded.v1`、`operation.updated.v1`。

所有关键业务事件先写 Transactional Outbox，再发布 Queue。

Event envelope：
event_id、event_type、occurred_at、aggregate_type、aggregate_id、data。

Consumer 必须 event_id 去重。

SSE 只能推送授权后的用户安全 payload。
