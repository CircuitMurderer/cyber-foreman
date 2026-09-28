# SPEC-007 技术设计

## 生命周期

```text
running → verifying → waiting_input
   ▲                       │
   └──── continue ─────────┘
```

REST 创建的 Agent 任务标记为 interactive；CLI 任务保持非交互。交互任务一轮验证通过后进入 `waiting_input`，`consumePrompt` 不退出，因此 Adapter、ACP transport、session ID 和事件 channel 都继续存活。新 Prompt 到达 mailbox 后先执行 `waiting_input → running`，重置本轮 Supervisor snapshot 和定时器，再调用原 session 的 `Prompt`。

验证失败产生的 `attention_required` 在 runtime 仍健康时也可继续，用于让 Agent 根据失败结果修复；若服务重启或事件流断联，runtime 不存在，`available_actions` 不会包含 continue。

## 动作能力

API 不让前端从状态自行推断能力，而是动态返回：

- `interrupt`：Agent turn 正在 running，Adapter 支持 cancel。
- `continue`：interactive runtime 存活，任务为 waiting/可修复 attention。
- `cancel`：任务尚未终止。
- `delete`：任务已终止或处于 waiting_input。

## 删除

`DELETE /api/v1/tasks/{id}` 从原来的 stop 语义调整为真正删除；停止统一通过 `POST .../actions {type: "cancel"}`。SQLite Store 在一个事务中先删除 events、再删除 task snapshot；应用层随后移除 runtime、任务 map 和 Bus 的内存 replay。

正在运行、排队或验证中的任务必须先 cancel，防止用户界面消失但外部进程继续工作。等待输入的 runtime 没有活动 turn，删除时会先关闭 Adapter，再清理历史。

## 总结

总结不调用模型。Web 从持久 SSE 事件中确定性聚合：

- 第一条 operator conversation message 作为原始任务；
- 同一轮 `agent_message_chunk` 拼接为完整回复，最后一轮作为最新结果；
- verification.finished、supervisor.decision 和 tool events 形成可核验统计与清单；
- 当前任务状态与 error 作为最终结论。

这与“对话与回复”共享同一个 conversation builder，避免两处 chunk 聚合逻辑漂移。
