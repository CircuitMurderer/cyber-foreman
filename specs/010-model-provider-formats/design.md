# SPEC-010 技术设计

## 分层

```text
REST / Web / Rule Engine
          │
          ▼
 internal/agent.Adapter
     │ ACP            │ App Server
     ▼                ▼
OpenCode/Grok       Codex CLI
     │                │ Responses(loopback)
     │                ▼
     │          session-local bridge
     └──────────────┬─┴──────────────┐
                    ▼                ▼
        OpenAI / Anthropic       Google
```

Foreman 的领域层只观察 Agent 生命周期。OpenCode 和 Grok 使用各自原生 provider 配置；Codex 固定向本地 Responses endpoint 发请求，由 bridge 根据启动参数透传或转换。这样模型 wire format 不泄漏到 REST DTO、监督规则或 SQLite schema。

## 配置优先级

1. 部署者显式设置的 `OPENCODE_CONFIG` 或 `GROK_HOME`。
2. 项目包装脚本提供的本地配置目录和无密钥示例 catalog。
3. Agent 自身默认配置。

OpenCode catalog 使用 provider/model ID 选择模型。Grok TOML 使用 model alias。Codex 使用服务级 `--codex-api-base`、`--codex-api-key-env`、`--codex-model` 和 `--codex-api-format`；省略 API base 时继续使用本机 Codex 登录态。

Grok 的 session title 请求会强制选择内部工具，DeepSeek thinking mode 会拒绝这种 `tool_choice`。模板因此用 `deepseek-flash` 执行主任务，并用映射到 non-thinking 请求模式的 `deepseek-chat` alias 生成 session summary；两者使用同一个 OpenAI-compatible endpoint 和 key。

## Codex 转换

- `responses`：保留 body 和 SSE，仅重写 endpoint 并注入 Bearer key。
- `chat-completions`：转换 message、function tool、tool result 和 tool choice；把 Chat SSE 重建为 Responses SSE。
- `anthropic-messages`：把 developer/system 提取为 system，把 function call/output 映射为 tool_use/tool_result content block；把 Messages SSE 重建为 Responses SSE。

bridge 为 Gemini thought signature、DeepSeek reasoning content、Anthropic thinking/signature 建立 session-local call metadata。并行工具调用按 index 排序，Anthropic 同一思考块只回填一次。

## Google 阶段边界

OpenCode 已有稳定的 Google SDK provider，因此采用原生格式。Grok 当前公开 backend 为 Chat Completions、Responses 和 Messages；Codex App Server 的自定义 provider 使用 Responses。因此二者先连接 Google 官方 OpenAI-compatible endpoint。未来增加原生 Google bridge 时，不改变 Adapter 或 REST API。
