# SPEC-015：LLM 受控工具纠偏

- 状态：IMPLEMENTED
- 创建日期：2026-10-08
- 最后更新：2026-10-08

## 背景

SPEC-014 只能让辅助模型根据一次性上下文输出 `pass`、`redirect` 或 `uncertain`。当证据不足时，模型无法主动查看 Foreman 已知的任务状态和事件，因此容易过度猜测。与此同时，直接把 Shell、文件系统或 Adapter 暴露给监督模型会绕过确定性规则和权限边界。

## 目标

- 使用 OpenAI-compatible 原生 tool calling，让辅助模型按需读取受控的 Foreman 状态。
- 允许模型通过结构化工具提议继续纠偏，但实际动作仍由 Foreman 的 Decision、Executor 和预算执行。
- 对读取工具做白名单、参数校验、结果限长和事件审计。
- 先用 DeepSeek 验证；内网 Qwen 支持相同协议时仅需修改配置。

## 非目标

- 不给辅助模型任意命令、文件读写、网络请求或 Adapter 控制权。
- 不允许辅助模型覆盖 Git、测试、权限、超时或预算结果。
- 不在 Agent 正在输出时做异步中断；本阶段仍在 Agent 回合结束且确定性验证通过后复核。
- 不默认向外部模型发送工作区 diff。

## 功能要求

- REQ-001：`tool_calling` 显式控制是否使用原生工具协议，关闭时保留严格 JSON 兼容路径。
- REQ-002：只暴露 `inspect_task_state`、`inspect_recent_activity` 和三个终态工具。
- REQ-003：`request_follow_up` 只生成语义纠偏建议，必须经过既有 Decision、Executor、去重和预算。
- REQ-004：工具参数严格解码，未知字段、未知工具、歧义终态调用和超轮次均拒绝并 fail-open。
- REQ-005：最近事件工具不得返回对话正文、Agent 输出或原始工具载荷。
- REQ-006：每次读取工具调用写入持久事件，但不记录完整工具结果。
- REQ-007：`inspect_workspace_diff` 仅在 `allow_workspace_diff=true` 且任务存在隔离 worktree 时暴露，结果经过既有敏感路径处理和额外长度限制。
- REQ-008：模型最多进行三轮工具交互，避免自主循环。

## 验收标准

- AC-001：单元测试覆盖读取工具后请求纠偏、歧义终态拒绝和 diff 默认关闭。
- AC-002：工具调用事件可以通过现有时间线展示。
- AC-003：DeepSeek 实际接口能完成原生工具调用并产出可执行的终态建议。
- AC-004：全量测试、vet、race 和前端构建通过。
