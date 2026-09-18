# SPEC-003 实现任务

- [x] T-001 [REQ-001..003] 定义 Observation、Snapshot、Policy、Decision 与证据类型
- [x] T-002 [REQ-007..009] 增加监督预算和 `attention_required` 状态迁移
- [x] T-003 [REQ-002, REQ-009..013] 将现有 Rules 重构为无副作用 Rule Engine，并使用假 Clock 测试
- [x] T-004 [REQ-004..006] 实现带能力检查、幂等和审计事件的 Action Executor
- [x] T-005 [REQ-010..013] 实现有效进展分类、idle timeout 和 hard timeout Probe
- [x] T-006 [REQ-014..017] 实现 argv Test Verifier、超时、输出上限和脱敏摘要
- [x] T-007 [REQ-018..022] 实现 Git/文件基线与只读 Workspace Verifier
- [x] T-008 [REQ-023..026] 将 OpenCode Prompt 生命周期迁入 `app.Service` 单任务 mailbox
- [x] T-009 [REQ-023, REQ-024] 实现 verifying 聚合与完成门禁
- [ ] T-010 [AC-003, AC-009] 增加重复事件、并发输入和预算耗尽故障测试
- [x] T-011 [AC-004..007] 使用临时命令和临时 Git 仓库验证 test/git 规则
- [ ] T-012 [AC-008] 使用模拟 ACP Agent 验证完整监督闭环
- [ ] T-013 [AC-010] 执行 fmt、vet、全量测试和竞态测试并记录验收证据
- [ ] T-014 更新 README 与验证矩阵，将规格推进到 ACCEPTED
