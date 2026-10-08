# SPEC-015 技术设计

## 控制流

```text
Agent turn + deterministic verification passed
                  │
                  ▼
        OpenAI-compatible reviewer
          │                 │
          │ read-only tool  │ terminal action tool
          ▼                 ▼
  SemanticToolbox      SemanticReview
  task/activity/diff   pass/uncertain/redirect
          │                 │
          └──── bounded ────┘
                            │ redirect
                            ▼
                 Decision → Executor → budget
                            │
                            ▼
                 same Agent session follow-up
```

## 权限边界

`supervisor.SemanticToolbox` 只定义读取接口，应用层负责实现。Reviewer 固定声明工具 schema，不接受模型动态指定命令。工具结果都是 JSON 文本并再次限长；最近活动只包含事件类型、时间、序号和少量白名单状态字段。

`request_follow_up`、`accept_turn` 和 `report_uncertain` 在 reviewer 内被解析为 `SemanticReview`，不是直接执行函数。只有 `redirect` 会在应用层转换为带 `SemanticRedirects: 1` 成本的 `Decision`，复用现有 Executor。因此模型无法绕开状态机、去重和预算。

Diff 属于显式数据出境能力。配置未打开、任务未使用隔离 worktree，或读取失败时均不暴露；即使打开，也复用 worktree 的敏感文件隐藏逻辑，并在发送模型前再次截断。

## 兼容与失败策略

`tool_calling=false` 保留 SPEC-014 的严格 JSON 响应，兼容不支持 tools 的 OpenAI-compatible 服务。工具调用最多三轮；HTTP、解析、工具、歧义动作或轮次错误继续使用原有 fail-open 语义，不影响已经通过的确定性结果。
