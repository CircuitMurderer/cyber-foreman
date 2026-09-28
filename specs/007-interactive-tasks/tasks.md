# SPEC-007 实施任务

- [x] 增加 `waiting_input` 状态及合法状态迁移。
- [x] 保留交互式 Agent runtime 并暂停空闲期计时器。
- [x] 实现 mailbox `continue` 动作和动态 `available_actions`。
- [x] 将 DELETE 改为任务与持久事件删除，运行中任务拒绝删除。
- [x] 在任务列表增加删除入口和确认提示。
- [x] 抽取共享对话聚合器并新增任务总结弹窗。
- [x] 增加同 session 多轮、零 cancel、SQLite 删除和 replay 清理测试。
- [ ] 后续增加显式 session 空闲 TTL 与归档策略。
