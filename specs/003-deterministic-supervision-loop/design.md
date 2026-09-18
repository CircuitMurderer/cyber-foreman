# SPEC-003 技术设计

## 总体结构

```text
internal/agent.Adapter events ─┐
Clock ticks ───────────────────┼─> Observation Builder
Git / Test probe results ──────┘            │
                                             ▼
                                  immutable Task Snapshot
                                             │
                                             ▼
                                   supervisor.RuleEngine
                                             │ Decision
                                             ▼
                                   supervisor.Executor
                                      │             │
                                      ▼             ▼
                                  Adapter       Verifiers
                                      │             │
                                      └──────┬──────┘
                                             ▼
                                         Event Bus
```

`app.Service` 持有任务运行时并串行化单任务的 Observation 处理。`internal/supervisor` 不直接拥有进程、Git 仓库或测试命令。

## 领域模型

### Observation

Observation 是已规范化事实，至少包含：

- ID 与 dedupe source；
- TaskID、SessionID；
- 类型和发生时间；
- 类型化 payload；
- 原始领域事件 ID 或验证结果引用。

第一版 Observation 类型：

- `agent.progress`
- `agent.turn_finished`
- `agent.disconnected`
- `timer.idle`
- `timer.hard_timeout`
- `verification.test_finished`
- `verification.git_finished`
- `action.finished`

### Task Snapshot

Snapshot 是规则计算所需的不可变视图：任务状态、当前 Session、最近有效进展时间、当前轮次、剩余预算、已执行 dedupe key、验证状态和 Policy。

### Decision

Decision 动作为：

- `none`
- `nudge`
- `cancel_and_follow_up`
- `retry_session`
- `start_verification`
- `complete`
- `attention_required`
- `stop`

Decision 不携带凭据。纠偏文本由 Executor 根据稳定模板与 evidence 摘要生成。

## 单任务串行化

每个运行中任务拥有一个 mailbox goroutine。Agent event、timer tick 和 verifier result 都进入同一 mailbox，因此 Snapshot 更新、规则计算和动作登记按顺序发生，不依赖复杂锁实现幂等。

跨任务可以并发；同一工作目录默认不允许并发写任务。后续多 worktree 规格再放宽限制。

## 有效进展

以下 ACP update 默认刷新 `last_progress_at`：

- Agent 文本 chunk；
- tool call 创建、状态改变或结果；
- plan 内容变化；
-明确的终端输出进展。

以下事件不刷新：

- usage update；
- config option update；
- available commands update；
-重复且 payload 未变化的 heartbeat/status。

事件分类由 Adapter/Observation Builder 完成，Rule Engine 不解析 OpenCode 专有 JSON。

## Timeout

使用 `Clock` 接口注入时间。生产实现使用标准库 Timer；测试实现手动推进。

- idle deadline 基于最近有效进展计算；
- hard deadline 固定为任务开始时间加 Policy 时限；
- timer Observation 的 dedupe key 包含 task、deadline 类型和触发序号；
-任何事件风暴都不能延长 hard deadline。

OpenCode 不支持真正的 mid-turn message 时，nudge 执行为 SPEC-002 已验证的 cancel → 等待当前 Prompt 返回 → follow-up Prompt。

## 验证器

### Test Verifier

命令配置为 `[][]string`，直接传给 `exec.CommandContext`。每项记录开始时间、结束时间、退出码、超时状态及 stdout/stderr 的限长尾部。默认单项超时 10 分钟，默认合并输出上限 64 KiB。

环境变量只按 allowlist 继承；事件不记录完整环境。失败摘要通过结构化字段进入纠偏模板。

### Workspace / Git Verifier

任务开始时：

1. 解析仓库根目录与 HEAD；
2. 保存 Git status；
3. 对 Policy 范围内文件记录路径、类型、mode、大小和 SHA-256；
4. 记录已存在的未提交状态作为基线。

验证时重新采样并比较，因此 Agent 对任务开始前已经 dirty 的文件继续修改也能被识别，而不会覆盖或清理用户原改动。

若文件数、单文件大小或总扫描大小超过 Policy 上限，基线建立失败并进入 attention_required。Git 命令全部只读，本规格不调用 reset、checkout、clean 或 commit。

## 完成门禁

Agent turn 结束后：

1. `running → verifying`；
2. 并行执行 Test 与 Git Verifier；
3. 聚合全部必需结果；
4. 全部通过才生成 complete Decision；
5. 可修复失败生成 bounded corrective action；
6. 安全失败或预算耗尽生成 attention_required。

Verifier 完成顺序不影响最终 Decision；聚合器按 verifier ID 排序，保证输入一致时决策一致。

## 规则优先级

从高到低：

1. 安全 deny / 工作区越界；
2. hard timeout；
3. 预算耗尽或能力缺失；
4. 验证失败；
5. 断联恢复；
6. idle nudge；
7. complete；
8. none。

同一 Observation 最多选择一个有副作用动作；其他匹配规则作为 suppressed decision evidence 记录。

## 能力适配

- `CancelTurn + Prompt`：执行 cancel-and-follow-up。
- `MidTurnMessage`：未来可直接发送 nudge，但仍受相同预算控制。
- 无交互能力：只能 Stop、Retry 或 attention_required。
- 无 ResumeSession：断联重试必须新建 Session，并用固定模板注入原任务目标与验证证据。

## 事件与脱敏

Decision 和 ActionResult 事件只保存：规则、动作、状态、预算、evidence ID、退出码、耗时和限长摘要。统一 redactor 在发布前移除疑似 token、Authorization header、已知敏感环境变量值和敏感文件内容。

## 回滚与开关

任务 Policy 包含 `supervision.enabled`。关闭时保留现有手动流程，但完成仍不得绕过显式配置的 verifier。新 Supervisor 不改变 Adapter 协议，移除 Service 装配即可回滚。

## 后续扩展

本地 Qwen 将实现为额外的 Advisor：接收脱敏 Snapshot 与 Observation，返回建议 Decision。合并器必须先执行本规格的确定性优先级，Advisor 不能覆盖 deny、验证失败或预算耗尽。

