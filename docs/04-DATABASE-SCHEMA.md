# 04 — Database Schema

数据库 PostgreSQL；参考 `db/schema.sql`。

规则：
- 核心 ID UUIDv7（应用层生成可接受）
- 时间 TIMESTAMPTZ/UTC
- 金额 BIGINT minor unit
- 财务数据不物理删除
- Payment+Order+Ledger+Outbox 同事务
- Reservation 使用 DB 行锁再次检查容量
- payments/operations 使用唯一 idempotency key

核心表见 schema.sql。
