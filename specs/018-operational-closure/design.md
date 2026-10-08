# SPEC-018 技术设计

## 权限审批闭环

`agent.Adapter` 保持最小公共接口不变，新增可选能力 `agent.PermissionResolver`。支持该能力的 Adapter 保存当前 session 的待处理请求，只接受 Agent 原始候选项中的 option ID。

```text
Agent request
    │ structured event
    ▼
waiting_permission ── REST/UI option ──► PermissionResolver
    │                                         │
    └──────────── agent.permission_resolved ◄─┘
                                              │
                                              ▼
                                           running
```

ACP 直接保留协议的 option ID。Codex thread 使用 `approvalPolicy=on-request` 和 `approvalsReviewer=user`，命令和文件审批映射为 `accept`、`acceptForSession` 与 `decline` 响应。当前无法安全转授的 Codex 动态权限、用户输入和 MCP elicitation 仍使用安全默认响应，并只发出不可操作的说明事件。

权限状态下停止 idle timer 与运行中语义抽检，避免把“等待人批准”误判为空转；hard timeout 保持有效，防止无人处理的权限请求无限挂起。权限决定本身不由语义监督器执行。

## 完成门禁

工作区验证器已有 `RequireChanges` 确定性规则。本阶段将 REST DTO 的 `require_changes` 改为三态：

- 省略：Agent + workspace verification 默认 `true`；
- 显式 `true`：必须观察到工作区变化；
- 显式 `false`：允许纯分析、审查或问答任务无改动完成。

前端对 Agent 任务默认启用“验证工作区”和“必须产生改动”，并保留显式关闭入口。验证命令仍由任务配置决定；只要配置了命令，所有命令与工作区门禁都通过后才能完成。

## 等待 session TTL

`supervisor.Policy.WaitingTimeout` 默认两小时。任务进入 `waiting_input` 或仍保留 runtime 的 `attention_required` 时重置定时器；操作员继续任务时立即停止等待计时器并重新启动 turn 的 idle/hard timer。

TTL 到期执行合法状态迁移至 `stopped`，随后由现有 runtime 清理逻辑停止 Adapter session。该状态表示资源回收，不伪造“任务已完成”。权限等待仍受当前 turn 的 hard timeout 约束。
