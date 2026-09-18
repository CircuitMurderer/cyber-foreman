# SPEC-004 实现任务

- [x] T-001 [REQ-003, AC-001] 实现 Adapter registry、能力列表和重复名称检查。
- [x] T-002 [REQ-002, REQ-009, AC-002, AC-003] 定义独立 REST v1 DTO、duration 解析和错误 envelope。
- [x] T-003 [REQ-004, AC-002] 增加异步 `SubmitTask`，创建时返回 queued task 与 Location。
- [x] T-004 [REQ-005, AC-004] 将运行中 interrupt/follow-up 接入任务 mailbox。
- [x] T-005 [REQ-006..REQ-008, AC-005] 增加事件 sequence、内存历史、cursor replay、gap 和 SSE keepalive。
- [x] T-006 [REQ-003] `foreman serve` 同时注册 process 和 OpenCode Adapter。
- [x] T-007 [REQ-004, REQ-005] 完成真实 OpenCode REST 创建与 interrupt 验收：follow-up 返回 `REST-INTERRUPT-OK`，workspace gate 通过并 completed。
- [ ] T-008 [REQ-010] 增加命名 Provider profile，替代长期继承守护进程全部环境。
- [x] T-009 [AC-006] 执行 fmt、vet、测试、race 测试并记录证据。
- [x] T-011 [REQ-011, AC-007] 增加公开事件 payload 脱敏、64 KiB 上限与测试。
- [ ] T-010 后续规格：SQLite 持久化、幂等创建键、认证和 workspace/command allowlist。
