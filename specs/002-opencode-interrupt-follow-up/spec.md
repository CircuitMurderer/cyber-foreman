# SPEC-002：OpenCode 中断与追加信息

- 状态：ACCEPTED
- 创建日期：2026-09-17
- 协议基线：Agent Client Protocol v1

## 目标

Cyber Foreman 在观察到 OpenCode 已开始输出后，应能取消当前轮次，并在同一 ACP 会话中追加一条新 Prompt，使 Agent 带着原会话上下文继续工作。

## 行为

1. 启动首轮 `session/prompt` 并转发事件。
2. 收到首个 `agent_message_chunk` 后发送 `session/cancel`。
3. 等待首轮 `session/prompt` 返回，避免同一会话并发 Prompt。
4. 在同一 `sessionId` 上发送追加 Prompt。
5. 转发追加轮次事件并返回最终 `stopReason`。

## 要求

- `REQ-001`：中断必须使用 ACP `session/cancel`，不得结束整个 OpenCode 进程。
- `REQ-002`：追加 Prompt 必须复用原 session ID。
- `REQ-003`：追加 Prompt 必须在被中断 Prompt 返回后发送。
- `REQ-004`：必须产生 `agent.interrupt_requested` 与 `agent.follow_up_started` 生命周期事件。
- `REQ-005`：CLI 通过 `--interrupt-with` 提供这一组合能力。
- `REQ-006`：CLI 不得把 API Key 或追加 Prompt 内容写入生命周期事件。

## 验收标准

- `AC-001`：模拟事件可识别 `agent_message_chunk`，其他 update 不触发中断。
- `AC-002`：真实 OpenCode + Gemini 3.8 Flash 的首轮输出被取消。
- `AC-003`：同一会话接受追加信息并返回预期答案。
- `AC-004`：格式化、vet、测试和竞态测试通过。

## 验收证据

- 真实模型：`google/gemini-3.8-flash`。
- 首轮收到 `agent_message_chunk` 后产生 `agent.interrupt_requested`。
- 首轮停止原因：`cancelled`。
- 同一 session ID 随后产生 `agent.follow_up_started`。
- 追加 Prompt 正确引用首轮上下文，返回 `ORBIT-73-RECOVERED`。
- 第二轮停止原因：`end_turn`。
