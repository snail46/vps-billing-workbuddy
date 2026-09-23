# 05 — State Machines

Order: pending→paid→fulfilling→fulfilled；pending→cancelled；paid→refund_pending→refunded  
Payment: pending/processing/succeeded/failed/partially_refunded/refunded  
Subscription: pending/active/past_due/suspended/cancelled/expired/terminated  
Node: online/degraded/draining/maintenance/offline  
Desired Instance: running/stopped/suspended/deleted  
Observed Instance: pending/provisioning/running/stopping/stopped/restarting/reinstalling/suspending/suspended/deleting/deleted/error/unknown  
Operation: queued/running/waiting_provider/waiting_resource/verifying/retrying/succeeded/failed/cancelled

Provider 不可达时实例为 unknown，不得猜 stopped/deleted。
Create timeout 先 verifying，不得盲目重复创建。
