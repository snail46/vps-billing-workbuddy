# 16 — Observability

JSON structured logging。
标准：request_id、trace_id、actor、action、resource、operation_id、provider、node。

Trace：request→operation→workflow→provider task。

Metrics：
HTTP、queue、outbox、operation、workflow、provider、heartbeat、provision、payment callbacks、capacity。

Audit 与普通日志分离。
