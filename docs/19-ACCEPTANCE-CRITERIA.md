# 19 — Acceptance Criteria

Release blocker：
重复入账、重复实例、越权、任务永久丢失、Provider 掉线误删资产、关键错误无提示、长任务无状态、Ledger 无法对账、关键 Audit 缺失。

发布前至少验证：
100 完整购买开通
100 重复支付回调
100 restart
50 reinstall
50 Provider timeout
20 worker crash
20 Redis restart
20 Agent disconnect
20 node offline
20 duplicate create

必须 0 duplicate charge / duplicate instance / auth bypass / permanent lost task / silent failure。
