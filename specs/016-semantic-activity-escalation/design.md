# SPEC-016 技术设计

## Agent 活动摘要

`semanticToolbox` 从 Event Bus 的有界内存窗口反向筛选 `tool_call`、`tool_call_update` 和权限请求事件，只解码白名单元数据。ACP 的 `rawInput`、`rawOutput` 以及 Codex App Server 的原始 item payload 不会被复制到结果。输出按时间恢复为正序，并继续受 reviewer 总输入长度限制。

## 人工升级

```text
request_operator_attention(reason)
              │
              ▼
SemanticReview{attention}
              │ capability gate
              ▼
Decision{attention_required, semantic_escalations: 1}
              │
              ▼
Executor → budget/dedupe → TaskAttention
```

能力在 Reviewer 配置和应用层各校验一次。配置未开启时工具 schema 中不存在该函数；即使自定义 Reviewer 直接返回 `attention`，应用层也会拒绝并按 fail-open 完成原流程。

人工升级发生在 Agent 回合结束且确定性验证通过之后，不会中断正在执行的 Agent。对于 Web 创建的交互任务，运行时和 session 仍保留，操作员可以阅读原因后使用“继续对话”恢复；非交互调用则以 `attention_required` 结束。
