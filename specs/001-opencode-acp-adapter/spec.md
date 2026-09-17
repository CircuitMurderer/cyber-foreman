# SPEC-001：OpenCode ACP Adapter

- 状态：ACCEPTED
- 负责人：Cyber Foreman
- 创建日期：2026-09-17
- 最后更新：2026-09-17
- 协议基线：Agent Client Protocol v1

## 背景与问题

Cyber Foreman 需要以结构化方式监督 OpenCode，而不是解析 TUI 文本。OpenCode 提供 `opencode acp`，通过标准输入输出交换逐行 JSON-RPC 2.0 消息，并在进程内运行私有 OpenCode 服务。

## 目标

- 在项目内启动 OpenCode ACP 子进程，不依赖全局安装。
- 完成 ACP v1 初始化与能力协商。
- 创建工作目录绑定的 OpenCode 会话。
- 发送文本 Prompt，并把流式更新转换为统一事件。
- 支持取消当前 Prompt。
- 检测进程退出、stdout 断开和协议错误，并上报结构化事件。

## 非目标

- 本规格不实现 OpenCode TUI 控制或终端屏幕解析。
- 本规格不实现会话恢复、分叉和跨进程持久化。
- 本规格不管理 Gemini、OpenAI 或 Anthropic 的密钥生命周期。
- 本规格不自动批准高风险工具调用。
- 本规格不实现本地 LLM 监督决策。

## 使用场景

### SCN-001：创建 ACP 会话

Given 项目内存在可执行的 OpenCode 二进制  
When 控制层以绝对工作目录启动 Adapter  
Then Adapter 必须启动 `opencode acp`、协商 ACP v1，并返回 OpenCode 会话 ID

### SCN-002：发送 Prompt

Given ACP 会话已经创建  
When 控制层发送文本 Prompt  
Then Adapter 必须发出 `session/prompt`，持续转发 `session/update`，并返回停止原因

### SCN-003：取消 Prompt

Given Prompt 正在执行  
When 控制层请求取消  
Then Adapter 必须发送 `session/cancel` 通知，并等待 Agent 以 `cancelled` 或等价失败结束当前轮次

### SCN-004：连接断开

Given ACP 会话已经创建  
When OpenCode 退出、stdout 关闭或返回不可解析消息  
Then Adapter 必须关闭所有待处理请求，并产生断联或退出事件

## 功能要求

- `REQ-001`：Adapter 必须通过可配置绝对路径启动 OpenCode，默认使用项目内 `.tools` 版本。
- `REQ-002`：Adapter 必须将 `initialize` 作为首个 ACP 请求，声明协议版本 1 和 Cyber Foreman 客户端信息。
- `REQ-003`：当 Agent 返回非 1 协议版本时，Adapter 必须终止启动并返回明确错误。
- `REQ-004`：初始化成功后，Adapter 必须使用绝对 `cwd` 和空 `mcpServers` 创建会话。
- `REQ-005`：Adapter 必须支持文本类型的 `session/prompt`，并返回 `stopReason`。
- `REQ-006`：Adapter 必须原样保留未知 `session/update` 变体，避免协议增加事件类型后丢失信息。
- `REQ-007`：Adapter 必须把 OpenCode stderr 作为诊断事件上报，不能混入 ACP stdout。
- `REQ-008`：Adapter 必须支持 `session/cancel`，且通知本身不得等待 JSON-RPC 响应。
- `REQ-009`：子进程退出或协议连接断开时，Adapter 必须使全部未完成调用返回错误。
- `REQ-010`：Stop 必须先尝试关闭 stdin 和优雅退出，超时后才能强制结束进程。
- `REQ-011`：Provider API Key 只能从调用进程环境或显式进程环境传入，不能写入仓库文件或事件。
- `REQ-012`：Agent 向客户端请求权限时，默认策略必须 fail-closed；未配置决策器时选择拒绝选项或取消请求。
- `REQ-013`：Adapter 必须允许调用方设置 ACP session config option，以便选择 `google/<model>` 等模型。

## 状态与不变量

- `INV-001`：未完成初始化时不得创建会话。
- `INV-002`：未创建会话时不得发送 Prompt 或取消通知。
- `INV-003`：每个 JSON-RPC 请求 ID 最多完成一次。
- `INV-004`：同一会话同一时间最多执行一个 Prompt。
- `INV-005`：未知 ACP 通知不得导致连接退出。
- `INV-006`：API Key 不得出现在日志、错误文本或事件 payload 中。

## 接口与事件

Adapter 对外使用 `internal/agent.Adapter`。新增交互能力包括：

- `Prompt(ctx, sessionID, PromptRequest) (PromptResult, error)`
- `Cancel(ctx, sessionID) error`
- `SetConfigOption(ctx, sessionID, configID, value) error`

新增事件：

- `agent.session_update`
- `agent.stderr`
- `agent.disconnected`
- `agent.permission_requested`

`agent.session_update` 的 payload 包含 ACP `update` 原始 JSON。

## 失败与恢复

- 启动失败、握手失败和协议版本不兼容：不可重试，由任务控制层决定是否重新创建 Adapter。
- Prompt 上下文取消：发送 `session/cancel`。
- ACP 进程退出：当前会话不可在本规格内恢复，产生断联事件。
- JSON 解析失败：记录脱敏诊断，关闭连接，避免继续使用可能错位的消息流。

## 安全与权限

- OpenCode 子进程只继承显式传入和父进程已有的环境变量。
- Adapter 不读取或记录 `GEMINI_API_KEY`、`OPENAI_API_KEY`、`ANTHROPIC_API_KEY` 的值。
- 客户端不声明文件系统和终端 ACP 能力；OpenCode 使用自身工具和权限系统。
- 权限请求默认拒绝，不使用“默认同意”。

## 验收标准

- `AC-001`：项目内 OpenCode 可执行文件能够报告预期版本。
- `AC-002`：模拟 ACP Agent 验证 initialize 必须先于 session/new。
- `AC-003`：模拟 ACP Agent 能接收 Prompt、发送 update 并返回 stopReason。
- `AC-004`：取消操作产生 session/cancel 通知。
- `AC-005`：异常退出使未完成 Prompt 返回断联错误并产生事件。
- `AC-006`：未知 session update 可以被完整读取和转发。
- `AC-007`：`./scripts/go vet ./...` 与 `./scripts/test` 通过。
- `AC-008`：真实 OpenCode 二进制完成 initialize 和 session/new 冒烟测试。

## 验证矩阵

| 要求 | 验收项 | 验证方式 |
|---|---|---|
| REQ-001..004 | AC-001, AC-002, AC-008 | 本地二进制 + 模拟 Agent |
| REQ-005, REQ-006 | AC-003, AC-006 | Go 单元测试 |
| REQ-007..010 | AC-004, AC-005 | 故障注入测试 |
| REQ-011, REQ-012 | AC-007 | 代码审查 + 单元测试 |
| REQ-013 | AC-003 | JSON-RPC 请求断言 |

## 开放问题

- OpenCode session 恢复将在后续规格中定义。
- 三种模型 Provider 的统一配置和凭据代理将在独立规格中定义。

## 验收证据

- OpenCode：`1.18.31`，项目内安装路径 `.tools/opencode/1.18.31/opencode`。
- 发布包 SHA-256：`caf7f31fa1aec2353ea859d4ef9ab824c6273d941b016e88d51193fa3028d34e`。
- 真实冒烟：OpenCode ACP 完成 `initialize` 与 `session/new`，测试通过。
- 质量检查：`./scripts/go vet ./...`、`./scripts/test`、`./scripts/go test -race ./...` 与 CLI 构建均通过。
