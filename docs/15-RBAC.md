# 15 — RBAC

角色：super_admin、operations、finance、support、read_only。

Permission 示例：
users.read/update/suspend
instances.read/start/stop/restart/reinstall/delete
payments.read/refund
ledger.read/adjust
nodes.read/update/delete
providers.read/manage
operations.read/retry
tickets.read/reply/manage
audit.read
admins.manage
roles.manage
settings.manage

每个 Admin Handler 显式声明权限。
