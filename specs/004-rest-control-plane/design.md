# SPEC-004 技术设计

## 组件边界

```text
Browser / CLI
     │ REST commands + SSE events
     ▼
internal/api DTO + validation
     │ app request / action
     ▼
app.Service ── task runtime mailbox
     │ adapter name
     ▼
agent.Registry
  ├── process.Adapter
  └── opencode.Adapter ── ACP
```

API 层拥有公开 JSON schema，并显式映射到应用层请求。领域对象可以继续为内部规则优化，不因前端兼容性被冻结。

## Adapter registry

Registry 在进程启动时注册长生命周期 Adapter。Adapter 自己管理多个 Session，任务 runtime 保存选中的 Adapter 引用，因此停止、Prompt、Cancel 和事件读取不会错误地路由到默认 Adapter。重复名称在启动时失败。

第一阶段使用实例 registry，而不是 factory：现有 process 与 OpenCode Adapter 已按 session 隔离且支持并发。需要每任务配置独立二进制或环境时再升级为 factory，不改变公开 API。

## 异步任务创建

`SubmitTask` 分为两步：

1. 同步校验输入、解析 Adapter、记录 `queued` task、发布 `task.created`；
2. goroutine 捕获 workspace baseline、启动 Adapter、握手、设置模型并转为 `running`。

同步错误不会创建任务；第二步错误把已分配 ID 的任务转为 `failed`。旧 CLI 继续使用同步 `StartTask`，便于把启动错误直接显示在终端。

## 动作 mailbox

REST `interrupt` 不直接调用 Adapter。`app.Service.InterruptTask` 将带结果 channel 的动作放入任务 mailbox；mailbox 验证 prompt active 且没有待发 follow-up，再通过 Supervisor Executor 执行 Cancel，并保存 follow-up。HTTP 仅在 mailbox 明确接受后返回 202。

`cancel` 使用 `StopTask`，取消任务 context、停止选中 Adapter，再执行合法状态迁移。

## 公开事件边界

Event Bus 保留内部结构化事实，HTTP 层负责公开契约。输出前递归遮蔽名称包含 api key、token、secret、password、authorization 或 credential 的字段，并处理常见 `KEY=value` 文本。单个 `data` 编码后最多 64 KiB；更大的 Adapter 原始事件变为 `{truncated, original_bytes}`。`task.created` 映射为不含 prompt/command 的 task response，而不是直接暴露领域对象。

## 事件 replay

Event Bus 在 Publish 时分配进程内全局 sequence 和 `evt-{sequence}` ID，并保留同时受事件数与约 16 MiB 内存预算约束的 history；超过 256 KiB 的单个历史 payload 只留摘要。`SubscribeSince` 在同一锁内建立订阅并复制 replay，避免 replay 与 live subscription 之间丢事件。

当请求 cursor 已早于最老 retained event，API 先发 `stream.gap`。前端随后读取 `GET /tasks/{id}`，再从新 cursor 继续。live subscriber 队列溢出时 Event Bus 主动断开该消费者，使其用最后收到的 ID 从 history 重连；事件不能在保持连接的同时被静默跳过。SQLite 阶段将把有限内存窗口替换为持久 cursor。

## REST DTO

- duration 使用字符串，由 `time.ParseDuration` 严格解析且必须为正数；
- prompt/command 放在 `input` 中，且恰好一个存在；
- task response 不回显 prompt/command；
- workspace 使用绝对路径语义，路径授权在后续安全规格中加入；
-错误使用 `{ "error": { "code": "...", "message": "..." } }`。

## 凭据

REST schema 不包含 token。当前 OpenCode 子进程继承守护进程环境中的 Provider key；后续用 `provider_profile` 指向只存在后端的命名配置。事件和任务快照不得包含环境。

## 回滚

旧 `foreman run` 与 `foreman opencode` 仍调用应用服务，因此 REST 层可以独立回滚。Adapter interface 未改变；registry 只是组合层。
