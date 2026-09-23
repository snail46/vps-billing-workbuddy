# 02 — Architecture

## 四层
Experience：User Web / Admin Web  
Business Control Plane：用户、商品、订单、支付、账本、订阅、实例、工单、权限  
Orchestration：Operation、Workflow、Queue/Worker、Scheduler、Reservation、Reconciler  
Infrastructure：Provider SDK、Direct Provider、Agent Provider

## 依赖
HTTP Handler → Application Service → Domain → Repository/Provider。

禁止 Handler 直接 SQL/Provider。

## 数据真相
- 商业：PostgreSQL
- 实际基础设施：Provider
- 期望状态：PostgreSQL desired_state

## V1
模块化单体 server + worker；PostgreSQL + Redis；React 前后台。
