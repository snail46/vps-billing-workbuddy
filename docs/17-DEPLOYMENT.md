# 17 — Deployment

V1：reverse-proxy、user-web、admin-web、server、worker、postgres、redis。

环境：development/staging/production。

Health：
`/health/live`
`/health/ready`（检查 PostgreSQL/Redis，不因单个 Provider 离线让平台整体 unready）。

备份 PostgreSQL + 安全配置；必须定期恢复演练。

生产只允许版本 migration。
