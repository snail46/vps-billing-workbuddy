# 14 — Security

普通用户、管理员、Agent/Provider credential 完全隔离。

优先 Secure HttpOnly Session Cookie；Cookie 修改请求启用 CSRF；生产 CORS allowlist。

密码强哈希；管理员 2FA；RBAC；Rate Limit。

Secrets 不进日志，不明文进普通配置；使用 credential_ref。

Provider/Agent 生产通信必须 TLS。

所有 ownership/RBAC 后端强校验，不能靠前端隐藏按钮。
