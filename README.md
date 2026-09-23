# VPS Billing Platform

VPS 财务计费与自动化资源管理平台 V1。

这是一个 Business Control Plane，不是一个演示面板。商业真相源是 PostgreSQL；
基础设施的实际状态来自 Provider；两者通过 Operation + Workflow 对账。

## 当前状态

| Phase | 状态 |
| --- | --- |
| **Phase 0 — Foundation** | 实现完成，**Gate 待 CI 实跑**（见下方说明） |
| Phase 1 — Identity / RBAC | 未开始 |
| Phase 2 及之后 | 未开始 |

Phase 0 的 8 项交付已实现，并通过 `scripts/verify-local.sh` 在本机验证。
Gate（「一条 compose 命令启动基础环境」）由 CI 的 `foundation-gate` job 真实执行，
**尚未在 CI 上跑过**，因此 Phase 0 未关闭。

本机没有 Docker，且 `wsl.exe` 被安全策略禁用，所以 compose 环境无法在本地启动。
原因、备选方案与决策记录在
`docs/adr/ADR-003-foundation-topology-and-gate.md`。

## 目录结构

```
backend/                 Go 模块：三个二进制 + 内部包
  cmd/server             业务 API 进程
  cmd/worker             Outbox/Queue/Workflow/Reconciler 进程
  cmd/migrate            版本化迁移执行器
  internal/              config、logging、httpx、middleware、health、db、redisx、
                         migrate、provider（仅接口）、version、httpapi
  migrations/            嵌入二进制的版本化 SQL
frontend/                npm workspaces
  shared/                @vps/shared：i18n、API 契约、设计令牌、页面状态组件
  user-web/              用户端（docs/10）
  admin-web/             运维端（docs/11）
db/schema.sql            V1 合并参考 schema（文档，不执行）
deploy/docker-compose.yml 基础环境
scripts/verify-local.sh   本机可验证范围
docs/                    规格与 ADR
.github/workflows/ci.yml  CI 与 Phase 0 Gate
```

## 快速开始

```sh
cp .env.example .env
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build
```

启动后：

- 业务 API <http://localhost:8080>，探针 `/health/live`、`/health/ready`
- 用户端 <http://localhost:3000>
- 运维端 <http://localhost:3001>

`.env` 可省略：compose 中每个值都有开发用默认值。生产必须覆盖密码与域名。

## 验证

```sh
bash scripts/verify-local.sh
```

本机可验证：gofmt、go vet、golangci-lint、go build、单元测试、前端 typecheck / lint /
test / build、compose 与 workflow 的 YAML 语法。

**只能由 CI 验证**（需要容器运行时）：compose Gate 启动与探活、集成测试
（PostgreSQL + Redis）、`docker compose config -q`。

## 工程约束（不可协商）

- Business PostgreSQL 是唯一商业数据真相源；Provider 不承担商业归属与计费。
- 前端不直接调用 Provider；HTTP Handler 不直接访问 SQL 或宿主机 shell。
- 所有长操作走 Operation + Workflow + Worker，HTTP 返回 202 + `operation_id`。
- 资金变动必须过 Ledger；`Payment + Order + Ledger + Outbox` 同事务；历史不可 UPDATE，
  纠错新增 adjustment。
- 高危/财务管理操作必须 Audit。
- 内部代码 / DB / API / 状态一律 English；UI 支持 `zh-CN` 与 `en-US`，禁止硬编码文案。
- V1 是模块化单体；不引入 Kubernetes、Kafka、Service Mesh。

完整条款见 `AGENTS.md`。

## 决策记录

核心架构变更必须先写 ADR（`docs/adr/`）。

| ADR | 内容 |
| --- | --- |
| ADR-001 | 后端栈：chi + pgx/v5 + sqlc + golang-migrate，**禁止 ORM** |
| ADR-002 | 前端栈：Vite + React + TS + Tailwind v4 + TanStack Query + i18next |
| ADR-003 | 基础拓扑、迁移归属、Gate 执行方式 |

## 首批 Provider 方向

实现任何 Adapter 前必须核对第三方**当前**官方文档与源码，不得凭记忆假设 API；
平台 Contract 不因第三方变化而改变。

Direct Provider：

- LXDAPI <https://github.com/xkatld/lxdapi-web-server>
- CLICD <https://cli.cd/>

Agent Provider：

- Runman Agent <https://github.com/narwhal-cloud/runman-agent>

## 交给代码 Agent

> 请严格按照 `AGENTS.md` 和 `MASTER_PROMPT.md` 施工。先阅读全部 P0 文档与当前任务相关文档，
> 从 `TASKS.md` 第一个未完成任务开始。不要跨 Phase，不要猜第三方 API。
> 每完成一个阶段必须运行 format/lint/test/typecheck/build，
> 并汇报修改文件、测试结果、兼容影响与已知风险。
