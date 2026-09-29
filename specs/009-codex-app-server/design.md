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

Probe 调用 `codex --version`、`codex login status` 和只读 initialize 握手。它不读取认证文件，不创建 thread。失败只会令 Codex Adapter unhealthy，不影响其他 Adapter 或控制平面启动。
