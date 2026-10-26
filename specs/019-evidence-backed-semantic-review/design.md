# SPEC-019 设计

## 判定流程

```text
可信操作员指令
      │
      ▼
提取显式必须项
      │
      ├── Agent 可见回复（不可信自述）
      ├── inspect_task_state（状态、变更文件、验证元数据）
      ├── inspect_agent_activity（工具活动元数据）
      └── inspect_workspace_diff（显式授权时）
      │
      ▼
证据化终态动作
      ├── accept_turn: evidence
      ├── request_follow_up: unmet_requirements + evidence
      ├── report_uncertain: missing_evidence
      └── request_operator_attention: evidence
```

## 数据模型

`supervisor.SemanticReview` 增加三个只读审计字段：

- `evidence`：支持当前结论的观察事实；
- `unmet_requirements`：纠偏针对的显式要求；
- `missing_evidence`：无法判断时缺少的事实。

字段进入 `supervisor.semantic_review_finished` 事件，但不会成为新的确定性事实。只有现有 `verdict` 会触发既有动作路径。

## 工具边界

`inspect_task_state` 可以返回工作区验证产生的变更文件名，以及每条验证命令的序号、退出码、耗时、超时、错误和输出是否存在。工具不返回工作目录、命令参数或原始输出。完整代码差异继续受 `allow_workspace_diff` 控制。

最终复核的第一轮通过 OpenAI-compatible `tool_choice` 强制调用 `inspect_task_state`。运行中抽检仍使用自动工具选择，避免把“尚未验证”误判为失败。

## 兼容与故障语义

严格 JSON 路径和 tool calling 路径使用同一证据校验。旧模型若不返回所需字段，本次复核按非法响应 fail-open，确定性流程继续运行。运行中 `accept_turn` 仅允许 Agent 继续当前回合，因此可以在尚无完成证据时返回。
