# SPEC-017 技术设计

## 调度

每个 Agent turn 维护独立的定时器、可见回复长度、已抽检次数和 generation。只有以下条件同时满足时才启动异步抽检：

- `mid_turn.enabled=true`；
- 到达配置的 `interval`；
- 累计回复达到 `min_output_runes`；
- 当前 turn 抽检次数未达到 `max_reviews`；
- 当前没有抽检或待发送 follow-up。

turn 结束、人工中断、idle 恢复、session 重建或任务取消时会取消抽检上下文。异步结果携带 generation，过期结果直接丢弃。

## 纠偏路径

```text
Agent 可见输出 → 定时抽检 → SemanticReview{redirect}
                                  │
                                  ▼
                  Decision{cancel_and_follow_up,
                           semantic_redirects: 1}
                                  │
                                  ▼
                       Executor → Adapter.Cancel
                                  │
                                  ▼
                当前 turn 返回 → 同 session 追加修正 Prompt
```

模型只提出纠偏建议，不能直接调用 Adapter。Executor 继续负责能力检查、预算、去重和审计事件。预算耗尽按 fail-open 处理，让当前 turn 继续。

## 阶段隔离

语义请求包含 `review_phase`。`mid_turn` 的 system prompt 明确说明工作尚未完成，不得因为暂未运行测试或未写总结而判错。该阶段即使全局开启人工升级，也不会向模型声明 `request_operator_attention`；结束后的 `final` 阶段保持现有能力。

运行中抽检不携带确定性验证成功结论。最终工作区门禁、验证命令及结束后复核仍按原顺序执行。
