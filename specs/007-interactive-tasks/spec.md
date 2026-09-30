# SPEC-007：交互式任务继续、删除与总结

- 状态：IMPLEMENTED
- 创建日期：2026-09-28
- 最后更新：2026-09-30
- 依赖：SPEC-005、SPEC-006

## 背景与问题

现有 REST Agent 任务只支持运行中 interrupt：一轮回复结束后 ACP session 被关闭，用户无法在原上下文上正常追问。任务列表也不能清理历史，详情页主要展示事件过程，缺少面向结果的整体视图。

## 目标

- REST Agent 一轮结束后保留同一 ACP session，允许用户发起不带 cancel 的下一轮 Prompt。
- 明确区分运行中纠偏 `interrupt` 与空闲后追问 `continue`。
- 允许删除已停止、已结束或正在等待输入的任务，并同步删除 SQLite 事件历史。
- 允许操作员把已经验证通过、正在等待输入的交互任务正常结束为 `completed`。
- 在 Web 详情中提供从完整事件日志重建的任务总结。

## 非目标

- 不跨 Foreman 进程恢复 ACP transport；重启后的旧 session 仍不可继续。
- 不使用额外 LLM 生成摘要，避免引入费用、幻觉和外网依赖。
- 不允许直接删除正在执行的进程或 Agent turn。
- 不在本阶段实现归档、软删除或回收站。

## 功能要求

- REQ-001：交互式 Agent 完成一轮并通过验证后必须进入 `waiting_input`，而不是关闭 session。
- REQ-002：`continue` 只在 session 存活且没有活动 turn 时可用，并复用原 session ID。
- REQ-003：`continue` 不得调用 Adapter Cancel；`interrupt` 仍必须先 cancel 当前 turn 再追加。
- REQ-004：任务响应必须返回 `available_actions`，前端不能仅凭状态猜测 session 是否存活。
- REQ-005：运行中任务删除必须返回冲突；终态或 `waiting_input` 任务删除必须清理任务快照和全部事件。
- REQ-006：Web 总结必须展示当前结论、原始任务、最新完整 Agent 回复、轮次、工具活动、监工决策和验证结果。
- REQ-007：CLI `foreman opencode` 保持单轮语义，完成后仍进入 `completed` 并退出。
- REQ-008：`finish` 只允许用于 session 存活且处于 `waiting_input` 的交互 Agent 任务；必须关闭 session 并转为 `completed`，不得从 running 或 attention 状态绕过完成门禁。

## 安全与不变量

- INV-001：等待输入时 idle/hard timeout 必须暂停，新一轮开始时重新计时。
- INV-002：重启时 `waiting_input` 与其他非终态任务一样转为 `attention_required`，不得伪造可恢复 session。
- INV-003：删除必须先确认任务没有活动 turn；数据库删除任务和事件必须在同一事务提交。
- INV-004：任务总结只能由已记录事实生成，不推断未发生的完成或验证结果。
- INV-005：删除后内存 replay 也不得继续返回该任务事件。
- INV-006：正常结束与强制停止必须保持不同语义：`finish → completed`，`cancel → stopped`。

## 验收标准

- AC-001：自动测试验证连续两轮 Prompt 使用同一 runtime，且 continue 的 cancel 次数为零。
- AC-002：等待状态支持 continue/cancel/delete，重启后不再暴露 continue。
- AC-003：SQLite 删除测试验证 task 和 events 同时消失。
- AC-004：前端可从列表删除任务，并在详情中使用“继续对话”和“任务总结”。
- AC-005：Go test/race/vet、TypeScript 和生产构建全部通过。
- AC-006：等待输入时 API 与 Web 暴露“结束任务”，执行后关闭 runtime 并进入 completed；其他状态拒绝 finish。
