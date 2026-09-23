# 10 — User UX

用户不关心 Provider/Agent/Workflow。

页面：
dashboard、products、checkout、instances/detail/network/traffic/activity、orders、invoices、wallet、notifications、tickets、account/security。

Dashboard 5 秒内回答：服务器数量/运行/需处理/余额/快到期/流量预警。

Instance Detail：状态、OS、IP/SSH、CPU/RAM/Disk、流量、到期、Start/Stop/Restart/Reinstall/Renew。

异步操作必须即时反馈、自动进度、刷新后恢复 Operation。

所有页面必须考虑 loading/loaded/empty/error/partial_error/permission_denied。
