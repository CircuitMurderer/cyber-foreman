# SPEC-018：运行闭环补全

- 状态：IMPLEMENTED
- 创建日期：2026-10-08
- 最后更新：2026-10-08

## 背景

Foreman 已能启动、监督、恢复和验证 Agent，但长期无人值守运行仍有三个缺口：Agent 的权限请求只能被静默拒绝；代码型任务可能在没有产生任何改动时通过；已经等待人工输入的 session 会无限占用进程资源。

## 目标

- 将结构化权限请求提升为可审计、可由操作员处理的任务状态。
- 默认阻止“没有产生工作区改动”的 Agent 编码任务通过完成门禁。
- 为等待输入或等待人工处理的交互 session 增加可配置生命周期上限。

## 非目标

- 不自动批准任何命令或文件操作。
- 不解析终端文本来猜测权限请求。
- 不让 LLM 绕过确定性权限与验收规则。
- 不在本阶段恢复 Foreman 进程重启前仍在运行的 Agent session。

## 功能要求

- REQ-001：Adapter 必须将可处理的权限请求转换为包含请求 ID 和候选项的结构化事件。
- REQ-002：可处理的权限请求必须将任务切换为 `waiting_permission`，并停止 idle 和语义抽检。
- REQ-003：操作员只能选择 Agent 原始请求声明的候选项；未知请求和未知候选项必须拒绝。
- REQ-004：权限处理必须通过 REST API 和 Web 控制台完成，并记录 `agent.permission_resolved` 审计事件。
- REQ-005：ACP、OpenCode 和 Codex 的命令/文件审批必须复用统一的 `agent.PermissionResolver` 能力。
- REQ-006：REST 创建的 Agent 任务启用工作区验证时，`require_changes` 默认开启；纯分析任务可显式关闭。
- REQ-007：`require_changes=true` 但未启用工作区验证的请求必须在启动任务前拒绝。
- REQ-008：交互任务进入 `waiting_input` 或可继续的 `attention_required` 后必须启动等待 TTL；到期后安全停止 session，并将任务标记为 `stopped`。
- REQ-009：等待 TTL 默认两小时，可按任务配置为正 duration；开始下一轮 Prompt 时必须取消旧计时器。

## 验收标准

- AC-001：应用测试证明权限请求会暂停任务，操作员选择后同一 turn 继续并进入正常完成流程。
- AC-002：REST 测试证明 `resolve_permission` 动作可用且非法参数不会被 Adapter 接受。
- AC-003：ACP 与 Codex Adapter 测试证明请求会等待显式选择，而不是静默批准或拒绝。
- AC-004：工作区测试证明无改动任务被拦截、有改动任务可通过，REST 默认值可显式关闭。
- AC-005：测试证明等待中的交互 session 在 TTL 后停止。
- AC-006：全量测试、vet、race、前端类型检查与生产构建通过。
