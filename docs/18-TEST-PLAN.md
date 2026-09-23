# 18 — Test Plan

Unit：价格、Ledger、状态机、权限、Scheduler。  
Integration：Postgres、Redis、Outbox、Payment、Provider Adapter。  
Provider Contract：health/capabilities/create/get/actions/traffic/NAT/idempotency/error normalization。  
E2E：注册→购买→支付→开通→操作→重装→流量→续费→暂停/恢复。  
Failure Injection：CREATE_TIMEOUT、CREATE_SUCCESS_BUT_TIMEOUT、NODE_OFFLINE、REINSTALL_FAIL、DELETE_NOT_FOUND、NETWORK_FAIL。
