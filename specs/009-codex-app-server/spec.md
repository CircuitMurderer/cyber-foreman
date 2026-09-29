# SPEC-009：Codex CLI App Server 接入

- 状态：ACCEPTED
- 负责人：Cyber Foreman contributors
- 创建日期：2026-09-29
- 最后更新：2026-09-29

## 背景

Codex CLI 不提供 ACP，而是通过 `codex app-server` 暴露无 `jsonrpc` 字段的 JSON-RPC JSONL 协议。Foreman 需要把这一协议适配到既有 `agent.Adapter`，让三个首批 Agent 在 REST 和 Web 控制面下保持一致体验。

## 目标

- 使用 Codex 官方 App Server 结构化协议，不解析终端 UI。
- 支持复用 Codex CLI/桌面版的本机认证缓存，或接入内网 OpenAI-compatible API。
- 支持 thread 启动、首轮 Prompt、同 thread 续聊、流式事件、取消和断联上报。
- 保持 Rule Engine、REST API、SQLite 历史和前端无需了解 Codex 私有协议。

## 非目标

- 不在 Foreman 内实现 Codex 登录流程或保存 token。
- 不在本阶段恢复 Foreman 重启前的 Codex thread。
- 不把 Codex App Server 伪装成 ACP profile。
- 不自动批准 Codex 的提权、文件或用户输入请求。

## 功能要求

- REQ-001：Codex 专属实现必须位于 `internal/agent/codex` 并实现 `agent.Adapter`。
- REQ-002：启动探测必须验证 executable、`codex login status` 和 App Server `initialize`，且不得创建 thread。
- REQ-003：实际任务必须依次执行 `initialize`、`initialized`、`thread/start` 和 `turn/start`。
- REQ-004：同一 Foreman 任务的继续对话必须复用 thread ID。
- REQ-005：取消必须调用 `turn/interrupt`，并把 interrupted 映射为统一的 `cancelled` stop reason。
- REQ-006：Agent 文本 chunk 必须映射为 `agent_message_chunk`；思考、工具和错误必须映射为现有领域事件。
- REQ-007：审批请求必须生成审计事件并默认拒绝，不能因无人值守而自动扩大权限。
- REQ-008：默认使用 `workspace-write`、`approvalPolicy=never` 和 ephemeral thread。
- REQ-009：`serve` 必须注册 Codex，并允许通过 `--codex-bin` 指定可执行文件。
- REQ-010：`--codex-api-base` 启用 API 模式时，密钥必须仅从 `--codex-api-key-env` 指定的环境变量读取，且 Probe 不得要求 Codex 登录。
- REQ-011：API 模式必须通过 `--codex-api-format` 支持原生 OpenAI Responses、OpenAI-compatible Chat Completions 和 Anthropic-compatible Messages；后两者必须把文本流与函数调用转换回 Codex 可消费的 Responses SSE 事件。
- REQ-012：桥接服务必须只监听随机 loopback 端口，生命周期不得超过对应 Codex session。
- REQ-013：Gemini thought signature、OpenAI reasoning content 和 Anthropic thinking/signature 必须按 call ID 在内存中跨轮保存和回填，不得落盘或暴露为任务事件。
- REQ-014：Responses 专有且无法等价转换的托管工具必须忽略，不能伪造成普通函数工具。
- REQ-015：API 模式必须关闭 Codex analytics、插件功能和远程插件目录刷新，避免对 ChatGPT/GitHub 控制面产生非模型请求。

## 验收标准

- AC-001：版本无关 JSON-RPC 客户端收发均不带 `jsonrpc` 字段，同时不破坏 ACP 严格模式。
- AC-002：伪 App Server 完成启动、两轮 Prompt、流式文本与取消生命周期。
- AC-003：审批请求被拒绝并形成领域事件。
- AC-004：本机真实 Codex 登录态、首轮 Prompt 和续聊通过。
- AC-005：`./scripts/test` 和 `./scripts/go vet ./...` 通过。
- AC-006：伪 Responses、Chat Completions 和 Anthropic Messages 上游验证请求、鉴权、文本流、函数调用与推理元数据往返转换。
- AC-007：真实 Codex App Server 分别经 DeepSeek Responses、Chat Completions、Anthropic Messages 以及 Gemini 兼容端点调用本地命令并产出最终回复。

## 安全不变量

- INV-001：Foreman 不读取 `~/.codex/auth.json` 或系统凭据存储。
- INV-002：健康检查不调用模型。
- INV-003：无人值守审批默认拒绝。
- INV-004：HTTP 服务仍默认只监听 loopback。
- INV-005：Provider API key 不得进入 Codex argv、子进程环境、SQLite 或公开事件。

## 验证矩阵

| 要求 | 验收项 | 测试/命令 | 证据 |
|---|---|---|---|
| REQ-001~009 | AC-001~003 | `./scripts/go test ./internal/acp ./internal/agent/codex` | 通过；包含 versionless wire、两轮 Prompt、取消竞态与审批拒绝 |
| 全部 | AC-005 | `./scripts/test`、`./scripts/go vet ./...` | 2026-09-29 全部通过 |
| REQ-002~006 | AC-004 | 本机 Codex CLI + Foreman REST | `codex-cli 0.158.0-alpha.2.1` 共享 ChatGPT 登录态，首轮返回 `CODEX_APP_SERVER_OK`，同 thread 续聊返回 `CODEX_CONTINUE_OK` |
| REQ-010~014 | AC-006 | `./scripts/go test ./internal/agent/codex` | 通过；覆盖 Responses 透传、Chat/Anthropic 双向转换、鉴权转发、并行工具调用和推理元数据 |
| REQ-010~014 | AC-007 | Codex App Server + DeepSeek/Gemini + Foreman REST | 三种 DeepSeek wire format 与 Gemini Chat compatibility 均发起 `exec_command(pwd)`，Codex 执行成功并最终回复 `cyber-foreman` |
