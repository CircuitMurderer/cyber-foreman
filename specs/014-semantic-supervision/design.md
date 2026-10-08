# SPEC-014 技术设计

## 优先级

```text
Agent turn finished
        │
        ▼
deterministic Git / test verification
        ├─ failed ──> repair / attention（LLM 不参与）
        │
        └─ passed or not configured
                    │
                    ▼
          optional semantic reviewer
             ├─ pass / uncertain / error ──> 原完成流程
             └─ redirect + budget ─────────> 同 session 追加纠偏
```

## 边界

`supervisor.SemanticReviewer` 是应用层使用的抽象；`OpenAIReviewer` 只实现 OpenAI-compatible Chat Completions，并使用标准库 HTTP 客户端。`app.Service` 在任务启动时捕获 reviewer，聚合当前回合的 `agent_message_chunk`，确定性验证之后才调用 reviewer。

Reviewer 输出经过严格 JSON 解码和枚举验证。Agent 文本被放在 `untrusted_agent_response` 字段中；输入和输出均限制长度。调用失败通过持久 `supervisor.semantic_review_finished` 事件暴露，但不会改变确定性任务状态。

自动纠偏使用新的 `semantic_redirect` 决策及独立预算。预算随操作员的新回合重置，与 idle、retry、test repair 预算互不影响。
