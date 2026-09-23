# 01 — PRD

## 定位
VPS/NAT VPS 商业销售、计费、自动开通、生命周期管理平台。

## 核心用户
普通用户：购买、连接、流量、续费、重装、账单。
管理员/运维：健康、异常、财务、资源、任务、诊断。

## V1 主链路
浏览商品 → 下单 → 支付 → Ledger → Subscription → Provision Operation → Scheduler → Provider → Instance → 流量 → 续费 → 到期 → 暂停 → 恢复/删除。

## V1 必须
Auth/RBAC、Product/Plan、Order/Payment/Invoice、Wallet/Ledger、Subscription、Instance、Node/Provider、Operation/Workflow、Scheduler/Reservation、Reconciler、Provider SDK、User Web、Admin Web、i18n、Notification、Ticket、Audit、Compose、CI/Test/Observability。

## 不做
无必要 Kubernetes/Kafka/Service Mesh、自研虚拟化、自研替代成熟 Agent、复杂代理商系统。

## 成功
0 silent failure；0 重复入账；0 重复实例；Provider 可替换；用户无需理解底层；管理员能在平台定位故障。
