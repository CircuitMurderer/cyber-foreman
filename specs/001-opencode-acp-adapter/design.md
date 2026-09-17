# SPEC-001 技术设计

## 方案

采用两层结构：

```text
internal/agent/opencode
        │ 生命周期与领域事件
        ▼
internal/acp
        │ JSON-RPC 2.0 / NDJSON
        ▼
项目内 opencode acp 子进程
```

`internal/acp` 只处理传输：请求 ID、并发等待、通知、反向请求、断联和逐行 JSON。它不知道 OpenCode 或任务状态。

`internal/agent/opencode` 负责：

- 启动和停止 OpenCode；
- ACP initialize 和 session/new；
- Prompt、Cancel 和 session config；
- 权限请求的 fail-closed 响应；
- ACP 消息到 `domain.Event` 的映射。

## 进程模型

第一版每个 Cyber Foreman Session 对应一个 `opencode acp` 进程和一个 OpenCode Session。这样隔离最清晰，进程断开也不会影响其他任务。后续如果启动成本成为瓶颈，再评估一个进程承载多个 ACP Session。

## 并发模型

- 一个 goroutine 读取 ACP stdout。
- 一个 goroutine 读取 stderr 并发布诊断事件。
- 一个 goroutine 等待子进程退出。
- JSON-RPC 写入由 mutex 串行化。
- pending map 以请求 ID 关联响应 channel。
- 每个 OpenCode Session 使用 prompt mutex 保证单轮执行。

## 断联语义

以下任一情况触发连接关闭：

- stdout EOF；
- ACP 行无法解析为 JSON-RPC 消息；
- 子进程退出；
- 父 context 取消。

关闭动作通过 `sync.Once` 执行，并向所有 pending 请求返回同一个连接错误。

## 权限策略

ACP 客户端必须处理 `session/request_permission`。第一版从 Agent 提供的 options 中按以下顺序寻找拒绝项：

1. `reject_once`
2. `reject_always`
3. 名称含 `deny` 或 `reject` 的选项
4. 若没有拒绝项，返回 `cancelled`

未来由 Supervisor Policy 替换该默认决策器。

## Provider 解耦

Adapter 不感知 Gemini/OpenAI/Anthropic 请求格式。模型由 OpenCode Provider 配置和 ACP session config 选择：

```text
Cyber Foreman ──ACP──> OpenCode ──Provider──> Model API
```

密钥通过环境继承，配置文件只允许保存 provider、model 和 endpoint 等非敏感信息。

## 回滚

OpenCode Adapter 是独立包；不选择 `opencode` adapter 时，现有 process adapter 行为不变。删除注册即可回滚，不迁移已有数据。
