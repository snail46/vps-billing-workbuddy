# 03 — Domain Model

## 核心对象
Order：购买行为。  
Payment：真实付款。  
Invoice：应收。  
Subscription：商业服务权益。  
Instance：真实 VPS。  
Operation：异步动作。  
Workflow：动作步骤。  
Ledger：资金事实。  
Node/Provider：基础设施。

## 必须分离
Order ≠ Payment ≠ Subscription ≠ Instance。

## 关键关系
User→Orders/Payments/Wallet/Subscriptions/Tickets  
Product→Plans→Subscriptions  
Subscription→Instance  
Provider→Nodes→Instances  
Instance→Networks/Ports/Traffic/Operations
