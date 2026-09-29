# SPEC-009 技术设计

## 协议边界

```text
REST / Web / Rule Engine
          │
          ▼
  internal/agent.Adapter
          │
          ▼
  Codex App Server Adapter
          │  versionless JSON-RPC over JSONL
          ▼
   codex app-server ── shared local auth cache
```

`internal/acp.Client` 增加可选 wire mode：默认仍严格发送和校验 JSON-RPC 2.0；Codex 模式省略 `jsonrpc` 并接受缺失版本字段。传输、请求关联、反向请求和断联处理继续复用经过测试的实现。

## 生命周期

每个 Foreman 任务拥有独立 App Server 进程和 ephemeral thread：

1. `initialize` / `initialized` 完成连接初始化。
2. `thread/start` 设置绝对工作目录、`workspace-write` 与 `approvalPolicy=never`。
3. 每轮 `turn/start` 返回 turn ID；Adapter 等待匹配的 `turn/completed`。
4. 续聊继续使用原 thread ID。
5. 取消调用 `turn/interrupt`；停止时关闭 stdin，超时后回收进程。

完成通知可能先于 `turn/start` 响应被消费，因此 session 同时保存 waiter 和早到的 completion，避免竞态丢失。

## 事件转换

- `item/agentMessage/delta` → `agent.session_update / agent_message_chunk`
- reasoning/plan delta → `agent_thought_chunk`
- `item/started` → `tool_call`
- `item/completed` 与 tool output delta → `tool_call_update`
- `turn/completed` → Prompt result
- approval/user-input request → `agent.permission_requested`，随后拒绝或返回空答案
- stderr、error、process exit → 现有 stderr/error/disconnected/exited 事件

转换后的 update 保留 `raw` 字段，前端可以展示完整 App Server payload；文本仍使用 ACP 风格 content 结构，使对话聚合逻辑可直接复用。

## 登录与健康状态

登录态模式的 Probe 调用 `codex --version`、`codex login status` 和只读 initialize 握手；API 模式则以指定环境变量是否存在替代登录检查。两者都不读取认证文件、不创建 thread。失败只会令 Codex Adapter unhealthy，不影响其他 Adapter 或控制平面启动。

## 内网 API Provider

```text
Codex App Server
      │ Responses API（loopback）
      ▼
session-local bridge
      ├─ Responses（透明转发）
      ├─ Chat Completions（转换）
      └─ Anthropic Messages（转换）
      ▼
DeepSeek / Gemini / 内网兼容端点
```

设置 `--codex-api-base` 后，Probe 改为检查 API key 环境变量和 App Server initialize，不再要求 `codex login status`。每个 session 启动独立的 loopback bridge，并通过临时 `model_providers.cyber_foreman` 配置让 Codex 使用 Responses wire API；session 结束或启动失败时立即关闭 bridge。

`--codex-api-format` 选择 `responses`、`chat-completions` 或 `anthropic-messages`。Responses 模式保持请求和 SSE 原样，仅注入上游鉴权；Chat 模式把 developer 消息映射为 system，把 Responses function tools 映射为 Chat Completions tools；Anthropic 模式把 system、tool_use 与 tool_result 映射为 Messages content block。两种转换模式都将上游 SSE 重建为 `response.created`、文本 delta、function call item 和 `response.completed`。Responses 专有托管工具没有可靠等价物时直接跳过。

Gemini 的函数调用会在 `tool_calls[].extra_content.google.thought_signature` 返回加密签名，DeepSeek Chat 返回 `reasoning_content`，Anthropic Messages 返回 thinking block 和 signature。Codex 的 function-call item 不保留这些扩展字段，所以 bridge 以 call ID 在 session 内存中暂存，并在包含对应 function call/output 的下一轮请求中回填。元数据不写日志、事件或 SQLite。

Provider key 由 Foreman 读取指定环境变量并只作为上游 HTTP Authorization header 使用。Codex 子进程只收到 loopback URL，不收到 key；代理配置继续由 Foreman 进程继承的标准代理环境变量控制。
