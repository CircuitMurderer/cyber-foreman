# SPEC-010：Agent 多模型接口格式

- 状态：ACCEPTED
- 负责人：Cyber Foreman contributors
- 创建日期：2026-09-29
- 最后更新：2026-09-29

## 背景

内网模型服务以 OpenAI-compatible API 为主，仍存在 Anthropic-compatible 遗留端点，Google 原生协议很少使用。Foreman 首批支持 OpenCode、Grok Build 和 Codex CLI，三者的模型配置能力不同，但控制面应允许部署者在不保存密钥的前提下为每个 Agent 选择上述端点。

## 目标

- OpenAI-compatible 为默认和最高优先级路径。
- 三个 Agent 均能使用 OpenAI-compatible 和 Anthropic-compatible 端点。
- 保留 Google 接入；OpenCode 使用原生 Google，Grok/Codex 先使用 Google OpenAI compatibility。
- Provider 配置只引用环境变量，不把 API key 写进 Git、任务、事件或数据库。
- 不改变 `internal/agent.Adapter`、REST 任务 DTO 和 Rule Engine 的协议无关边界。

## 非目标

- 本阶段不实现密钥管理服务或在 Web 前端录入密钥。
- 本阶段不为 Grok/Codex 实现 Google 原生 GenerateContent wire format。
- 本阶段不在单个任务请求中动态切换 Codex provider；Codex provider 是服务启动配置。
- 不承诺转换 Responses API 专有托管工具。

## 功能要求

- REQ-001：密钥环境变量统一命名为 `FOREMAN_AGENT_API_KEY_OPENAI`、`FOREMAN_AGENT_API_KEY_ANTHROPIC`、`FOREMAN_AGENT_API_KEY_GOOGLE`。
- REQ-002：OpenCode 必须通过无密钥 provider catalog 支持 OpenAI、Anthropic 和 Google，并允许 `OPENCODE_CONFIG` 覆盖默认 catalog。
- REQ-003：Grok Build 必须通过无密钥 TOML 模板支持 Chat Completions、Messages 和 Google OpenAI compatibility，并允许调用者覆盖 `GROK_HOME`。
- REQ-004：Codex 必须通过 session-local loopback bridge 支持 Responses、Chat Completions 和 Anthropic Messages。
- REQ-005：所有 provider base URL、模型名与 key 环境变量都必须可配置，不得把 DeepSeek 写死在 Adapter 领域层。
- REQ-006：ACP 控制协议、Codex App Server 协议与模型 wire format 必须相互独立。
- REQ-007：转换桥必须保存上游完成工具调用所需的 opaque 推理元数据，并仅在 session 内存中按 call ID 使用。
- REQ-008：项目包装脚本不得覆盖部署者显式设置的 `OPENCODE_CONFIG` 或 `GROK_HOME`。
- REQ-009：Codex API 模式必须关闭 analytics、插件功能和远程插件目录刷新，确保内网部署只依赖配置的模型 endpoint。

## Agent/格式矩阵

| Agent | OpenAI | Anthropic | Google |
|---|---|---|---|
| OpenCode | `@ai-sdk/openai-compatible` | `@ai-sdk/anthropic` | `@ai-sdk/google` 原生 |
| Grok Build | `chat_completions`/`responses` | `messages` | OpenAI compatibility |
| Codex CLI | Responses 透传或 Chat bridge | Messages bridge | OpenAI compatibility bridge |

## 安全不变量

- INV-001：模板和 SDD 不得包含真实 key。
- INV-002：Codex bridge key 不得进入 Codex argv、子进程环境、SQLite 或公开事件。
- INV-003：Codex bridge 只监听随机 loopback 端口，并随 session 关闭。
- INV-004：健康探测不得产生模型费用。
- INV-005：Google 原生支持的延期不得阻塞 OpenAI/Anthropic 内网主路径。

## 验收标准

- AC-001：OpenCode 使用仓库 catalog 分别得到 OpenAI、Anthropic 和原生 Google 的有效回复。
- AC-002：Grok Build 使用仓库 TOML 分别得到 OpenAI 和 Anthropic 的有效回复。
- AC-003：Codex 经 Responses、Chat Completions 和 Anthropic Messages 均能完成一次真实工具调用及最终回复。
- AC-004：桥接单测覆盖鉴权、文本流、工具调用、并行调用和推理元数据回填。
- AC-005：`./scripts/test`、`./scripts/go vet ./...` 和 race test 通过。

## 验证证据

| 路径 | 结果 |
|---|---|
| OpenCode + DeepSeek OpenAI | `OPENCODE_WRAPPER_OK` |
| OpenCode + DeepSeek Anthropic | `OPENCODE_ANTHROPIC_WRAPPER_OK` |
| OpenCode + Gemini native | `OPENCODE_GOOGLE_NATIVE_OK` |
| Grok + DeepSeek OpenAI | `GROK_OPENAI_OK` / `GROK_TEMPLATE_OK` |
| Grok + DeepSeek Anthropic | `GROK_ANTHROPIC_OK` |
| Codex + DeepSeek 三种格式 | 均执行 `pwd` 并返回 `cyber-foreman` |
