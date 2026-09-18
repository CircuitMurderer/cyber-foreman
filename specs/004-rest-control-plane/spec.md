# SPEC-004：REST 控制面 v1

- 状态：IMPLEMENTING
- 创建日期：2026-09-18
- 最后更新：2026-09-18
- 依赖：SPEC-001、SPEC-002、SPEC-003

## 背景与问题

Cyber Foreman 最终以本地后端守护进程和浏览器前端的形式运行。当前 HTTP 层直接使用应用层 Go 结构、服务实例固定绑定单一 Adapter、事件只做瞬时广播，因此无法作为稳定前端契约，也无法通过一个守护进程同时调度 process 与 OpenCode。

## 目标

- 提供版本化、前端友好的 REST JSON 契约。
- 每个任务显式选择已注册 Adapter。
- 创建任务立即返回任务 ID，耗时启动在后台执行。
- 通过 REST 对运行中任务执行 cancel 与 interrupt/follow-up。
- 通过 SSE 实时消费事件，并在短暂断线后按事件 ID 补发。

## 非目标

- 本规格不实现前端页面。
- 本规格不实现局域网认证与多用户授权；未认证服务仍只监听回环地址。
- 第一阶段不提供跨进程持久化；SQLite 由后续规格定义。
- 第一阶段不提供 retry、resume 或审批动作。

## 使用场景

### SCN-001：创建 OpenCode 任务

Given 后端已注册 `opencode` Adapter  
When 客户端向 `POST /api/v1/tasks` 提交 prompt、workspace 和监督策略  
Then 服务在响应适配器握手前返回 `202`、任务 ID、`queued` 状态和资源链接，并在后台启动任务。

### SCN-002：运行中纠偏

Given OpenCode Prompt 正在执行  
When 客户端提交 `interrupt` 动作和补充信息  
Then Foreman 取消当前 ACP turn，在同一 Session 中提交补充 Prompt，并产生监督与 Agent 事件。

### SCN-003：前端断线重连

Given 客户端已处理到 `evt-41` 后断开  
When 客户端携带 `Last-Event-ID: evt-41` 重新连接 SSE  
Then 服务先补发内存窗口中 sequence 大于 41 的事件，再继续发送实时事件。

## 功能要求

- REQ-001：所有公开控制接口必须位于 `/api/v1`，健康检查除外。
- REQ-002：API DTO 必须与 `internal/app`、`time.Duration` 和 Adapter 私有类型解耦；持续时间使用 Go duration 字符串，例如 `90s`、`10m`。
- REQ-003：服务必须通过 Adapter registry 按请求中的稳定名称选择 Adapter，并公开名称与能力列表。
- REQ-004：`POST /api/v1/tasks` 必须在完成请求校验与创建 queued 记录后返回 `202 Accepted`；启动失败必须记录在该任务状态中。
- REQ-005：API 必须提供任务列表、任务快照、取消、运行中 interrupt/follow-up 和任务事件流。
- REQ-006：事件必须包含 `id`、`version`、单调递增 `sequence`、`task_id`、`type`、`occurred_at` 与类型化 `data`。
- REQ-007：SSE 必须接受 `Last-Event-ID` 或 `after` cursor，在保留窗口内补发事件，并发送 keepalive。
- REQ-008：当 cursor 早于保留窗口时，SSE 必须产生 `stream.gap`，要求客户端重新读取任务快照。
- REQ-009：错误响应必须包含稳定的 `error.code` 和面向人的 `error.message`。
- REQ-010：前端请求不得携带 API Key；Provider 凭据只从后端环境或后续命名配置读取。
- REQ-011：公开 SSE 必须递归脱敏常见凭据字段，并将单事件 `data` 限制在 64 KiB；超限时返回结构化 truncated 标记。

## 状态与不变量

- INV-001：API 不能绕过 `internal/task` 的状态迁移。
- INV-002：一个 task runtime 只使用创建时选定的 Adapter。
- INV-003：任务创建响应不得等待外部模型完成 Prompt。
- INV-004：`interrupt` 只对 active prompt 生效；不适用时返回冲突错误，不能静默丢弃。
- INV-005：事件 sequence 在单个 Foreman 进程内严格递增；重启后第一阶段允许重新计数。
- INV-006：API 不返回 Prompt 正文、环境变量或凭据。

## 接口与事件

第一阶段接口：

```text
GET    /healthz
GET    /api/v1/adapters
POST   /api/v1/tasks
GET    /api/v1/tasks
GET    /api/v1/tasks/{id}
DELETE /api/v1/tasks/{id}
POST   /api/v1/tasks/{id}/actions
GET    /api/v1/tasks/{id}/events
GET    /api/v1/events
```

动作类型：

```json
{"type":"interrupt","message":"补充信息"}
{"type":"cancel"}
```

## 失败与恢复

- JSON schema、duration、Adapter 名称和动作错误在同步响应中返回 4xx。
- Adapter 启动、握手、workspace baseline 等异步失败写入任务状态和事件。
- SSE 断开不影响任务；客户端使用最后事件 ID 重连。
- 内存历史最多保留最近 4096 个事件和约 16 MiB；单个历史 payload 超过 256 KiB 时只保留 truncated 摘要。窗口溢出后客户端按 `stream.gap` 重新同步快照。

## 安全与权限

- HTTP 默认监听 `127.0.0.1`。
- 未实现认证前不得将 listener 改为非回环地址用于共享部署。
- 任务响应不回显 prompt 和 command，降低浏览器日志泄漏风险。
- `.api_key` 与 Provider token 不属于 REST schema。

## 验收标准

- AC-001：一个服务实例能列出并按任务使用 process 与 OpenCode Adapter。
- AC-002：合法创建请求返回 202、queued 快照、Location 和资源链接。
- AC-003：数字 duration、未知字段和不存在的 Adapter 返回稳定 400 错误。
- AC-004：运行中 interrupt 经 mailbox 执行 cancel → follow-up。
- AC-005：SSE 事件包含 v1 envelope，并支持 retained-event replay 和 gap 检测。
- AC-006：`./scripts/test`、`go vet` 和 race tests 通过。
- AC-007：公开事件不会回显 task command、常见 secret 值或超过 64 KiB 的原始 payload。

## 验证矩阵

| 要求 | 验收项 | 测试/命令 | 证据 |
|---|---|---|---|
| REQ-003 | AC-001 | `internal/agent/registry_test.go` | 自动化测试 |
| REQ-002, REQ-004, REQ-009 | AC-002, AC-003 | `internal/api/server_test.go` | 自动化测试 |
| REQ-005 | AC-004 | `internal/app/prompt_test.go` | 自动化测试 |
| REQ-006..REQ-008 | AC-005 | `internal/event/bus_test.go`, `internal/api/server_test.go` | 自动化测试 |
| REQ-011 | AC-007 | `internal/api/server_test.go` | 自动化测试 |
| 全部 | AC-006 | `./scripts/test`; `./scripts/go test -race ./...` | 2026-09-18 通过 |

## 开放问题

- Q-001：SQLite 事件存储采用单库 WAL 还是按 workspace 分库。
- Q-002：认证启用后使用本地 session cookie 还是 bearer token。
- Q-003：是否在 v1 稳定前增加 OpenAPI 生成物。
