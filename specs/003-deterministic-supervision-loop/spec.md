# SPEC-003：确定性监督闭环

- 状态：IMPLEMENTING
- 负责人：Cyber Foreman
- 创建日期：2026-09-18
- 最后更新：2026-09-18
- 关联规格：SPEC-001、SPEC-002

## 背景与问题

当前 `internal/supervisor` 只有未接入应用层的简单规则，`app.Service` 在 Agent 正常退出后会直接使用占位验证器完成任务。OpenCode 的 Prompt 流程也主要由 CLI 自行编排，因此 timeout、Git、测试和恢复动作尚未形成统一闭环。

## 目标

- 让 Adapter、定时器、Git 和测试结果形成统一的结构化 Observation。
- 使用无副作用的规则引擎，根据任务快照生成可审计 Decision。
- 由受控 Action Executor 执行纠偏、取消、重试、验证和人工升级。
- 以测试和工作区检查作为完成门禁，不信任 Agent 的自然语言完成声明。
- 在不接入任何监督大模型的情况下跑通完整控制闭环。

## 非目标

- 不接入 Qwen、OpenAI、Anthropic 或 Google 作为监督决策器。
- 不实现 SQLite 持久化、进程重启后的恢复或跨机器调度。
- 不实现通用 CI 系统、容器沙箱或任意用户脚本执行平台。
- 不自动批准 Agent 权限请求。
- 不实现 Grok Build、Codex CLI 等新的 Agent Adapter。

## 使用场景

### SCN-001：空转纠偏

Given Agent 在运行且超过 idle timeout 没有有效进展  
When 定时 Probe 产生 idle Observation  
Then 规则引擎生成有预算的纠偏 Decision，执行器按 Adapter 能力进行提示或“取消后追加 Prompt”

### SCN-002：硬超时

Given 任务总运行时间达到 hard timeout  
When 超时 Observation 被处理  
Then Foreman 取消当前操作、停止 Agent，并把任务转为 `attention_required`

### SCN-003：测试失败后修复

Given Agent 当前轮次结束  
When 预配置测试命令返回非零退出码  
Then 任务保持未完成，Foreman 将脱敏且限长的失败摘要反馈给 Agent；修复预算耗尽后转人工处理

### SCN-004：Git 越界

Given 任务开始时已记录工作区基线  
When Agent 修改禁止路径、改变 HEAD 或产生非法工作区变化  
Then 完成门禁失败，Foreman 不得把任务标记为 completed

### SCN-005：验证通过

Given Agent 已结束当前轮次  
When 所有必需测试通过且 Git 规则通过  
Then 任务从 verifying 转为 completed，并关联验证证据

### SCN-006：重复事件

Given 同一个断联、超时或测试失败 Observation 被重复投递  
When 规则引擎再次处理它  
Then 不得重复执行动作或重复消耗预算

## 功能要求

### 决策管线

- `REQ-001`：系统必须把 Agent、Timer、Git 和 Test 的输入规范化为带唯一 ID、任务 ID、时间和类型的 Observation。
- `REQ-002`：Rule Engine 必须仅依赖 Observation、Task Snapshot 和 Policy 生成 Decision，不得执行 I/O 或调用 Adapter。
- `REQ-003`：每个非空 Decision 必须包含 `rule_id`、action、reason、evidence、dedupe_key 和 budget_cost。
- `REQ-004`：Action Executor 必须在执行前验证任务状态、Adapter 能力、预算和 Decision 去重状态。
- `REQ-005`：系统必须发布 `supervisor.decision`、`supervisor.action_started` 与 `supervisor.action_finished` 事件。
- `REQ-006`：同一 `dedupe_key` 最多成功执行一次，重复输入不得再次消耗预算。

### 策略与预算

- `REQ-007`：每个任务必须有不可变的 Policy 快照；第一版默认值为 idle timeout 90 秒、hard timeout 30 分钟、最多 2 次 nudge、2 次 retry、2 次 test repair。
- `REQ-008`：预算耗尽、规则冲突或必要能力缺失时，任务必须转为 `attention_required`，不得无限重试。
- `REQ-009`：确定性安全拒绝、Git 越界、验证失败和预算耗尽的优先级必须高于普通纠偏建议。

### Timeout

- `REQ-010`：系统必须区分有效进展事件与配置、usage、heartbeat 等非进展事件。
- `REQ-011`：idle timeout 首次触发时必须生成 nudge；重复空转按 Policy 升级为 cancel/retry 或 attention_required。
- `REQ-012`：hard timeout 必须停止自动执行并转为 attention_required，不能被普通进展事件延长。
- `REQ-013`：时间相关测试必须使用可注入 Clock，不得依赖真实 sleep。

### Test 验证器

- `REQ-014`：测试命令必须来自任务创建时的显式 argv 配置，不得执行 Agent 输出中建议的命令。
- `REQ-015`：每条测试命令必须有独立超时、输出大小上限和退出码证据。
- `REQ-016`：测试失败反馈必须限长并脱敏，不得包含环境变量值或凭据。
- `REQ-017`：测试失败只能在 test repair 预算内触发修复 Prompt；预算耗尽后必须转人工处理。

### Git 与工作区

- `REQ-018`：任务启动前必须记录 Git HEAD 和工作区文件基线，不得把用户已有改动归因于 Agent。
- `REQ-019`：工作区基线必须排除 `.git`、`.tools`、`.cache`、`bin`、`data` 及 Policy 声明的忽略路径。
- `REQ-020`：默认策略必须禁止 Agent 改变 HEAD、修改仓库外文件和修改敏感路径。
- `REQ-021`：验证阶段必须执行等价于 `git diff --check` 的检查，并记录退出码和限长输出。
- `REQ-022`：无法可靠建立或比较基线时，系统必须 fail closed，进入 attention_required。

### 完成门禁与应用层

- `REQ-023`：Agent 的 `end_turn`、零退出码或自然语言完成声明只能触发 verifying，不得直接触发 completed。
- `REQ-024`：只有全部必需 Verifier 通过，任务才能转为 completed。
- `REQ-025`：OpenCode Prompt、事件、取消和追加信息流程必须进入 `app.Service` 的统一控制循环，CLI 只作为调用入口。
- `REQ-026`：规则动作必须复用 `agent.Adapter`，不得依赖 OpenCode 专有输出格式。

## 状态与不变量

- `INV-001`：Rule Engine 无 I/O、无全局状态，同一输入必须产生同一 Decision。
- `INV-002`：同一任务同一时间最多执行一个 Agent Prompt 和一个监督动作。
- `INV-003`：任何自动动作都必须消耗或明确声明不消耗预算。
- `INV-004`：任务进入 completed 时必须关联 test 和 Git 验证证据。
- `INV-005`：用户在任务开始前已有的文件内容不得被恢复、覆盖或清理。
- `INV-006`：日志、Observation、Decision 和验证输出不得包含 API Key。
- `INV-007`：未来 LLM Decision 不得覆盖确定性 deny 或完成门禁失败。

## 接口与事件

建议的领域接口：

```text
RuleEngine.Evaluate(snapshot, observation, policy) []Decision
ActionExecutor.Execute(ctx, decision) ActionResult
Verifier.Verify(ctx, task) VerificationResult
Clock.Now() time.Time
```

新增领域事件：

- `supervisor.decision`
- `supervisor.action_started`
- `supervisor.action_finished`
- `verification.started`
- `verification.finished`
- `task.attention_required`

事件只记录 Prompt 的用途和引用，不记录完整纠偏 Prompt 或凭据。

## 失败与恢复

- Observation malformed：记录规则错误并忽略该 Observation；不得猜测动作。
- Action 执行失败：记录失败证据，在预算内重试一次执行；仍失败则 attention_required。
- Agent 断联：在 retry 预算内创建新 Session；没有 ResumeSession 能力时明确记录上下文重建方式。
- Verifier 超时：视为验证失败，不视为测试通过。
- Git 不可用或工作区越界：fail closed，进入 attention_required。
- 本规格所有预算只保存在内存；Foreman 进程退出后任务不可恢复。

## 安全与权限

- 验证命令使用 argv 直接执行，不经过 shell 展开。
- Action Executor 不得执行来自 Agent 文本的命令。
- 默认禁止网络型 Verifier。
- Git Probe 只读；本规格不得自动 reset、checkout、clean 或删除文件。
- 敏感路径至少包括 `.api_key`、`.env*`、私钥文件和 Policy 追加项。

## 可观测性

每个监督动作必须能回答：

- 哪条规则触发；
- 使用了哪些 Observation；
- 执行了什么动作；
- 消耗了多少预算；
- 动作结果和下一状态是什么；
- 完成时通过了哪些验证器。

## 验收标准

- `AC-001`：纯规则单元测试覆盖 timeout、断联、测试失败、Git 越界、预算耗尽和规则优先级。
- `AC-002`：假 Clock 能在不 sleep 的情况下验证 idle 与 hard timeout。
- `AC-003`：重复 Observation 不产生重复动作或预算消耗。
- `AC-004`：测试失败触发有限次修复，随后进入 attention_required。
- `AC-005`：预先存在未提交改动时，基线比较不会把它误认为本任务新改动。
- `AC-006`：修改禁止路径或 HEAD 时完成门禁失败，且不会修改用户文件。
- `AC-007`：Agent 声称完成但测试失败时，任务不会进入 completed。
- `AC-008`：OpenCode 模拟 ACP 测试覆盖 Prompt → idle nudge → cancel/follow-up → verify → completed。
- `AC-009`：所有自动恢复预算耗尽后停止，不存在无限循环。
- `AC-010`：`./scripts/go vet ./...`、`./scripts/test` 和 `./scripts/go test -race ./...` 通过。

## 验证矩阵

| 要求 | 验收项 | 验证方式 |
|---|---|---|
| REQ-001..006 | AC-001, AC-003 | Rule Engine 与 Executor 单元测试 |
| REQ-007..013 | AC-001, AC-002, AC-009 | 假 Clock、预算和优先级测试 |
| REQ-014..017 | AC-004, AC-007 | 临时命令与假 Adapter 组件测试 |
| REQ-018..022 | AC-005, AC-006 | 临时 Git 仓库与文件快照测试 |
| REQ-023..026 | AC-007, AC-008 | app.Service + 模拟 ACP 端到端测试 |
| 全部 | AC-010 | 项目质量命令 |

## 开放问题

- 无阻塞性开放问题；持久化、LLM 决策和其他 Agent Adapter 由后续规格处理。

## 阶段性实现证据

- OpenCode Prompt、事件、Prompt result 与 timer 已迁入 `app.Service` 单任务 mailbox。
- 模拟 Adapter 验证 idle timeout 会产生 `idle-nudge`、调用 Cancel、在同一 Session 追加 Prompt，并进入完成门禁。
- 模拟 Adapter 验证 hard timeout 会停止自动执行并进入 `attention_required`。
- 真实 OpenCode 1.18.31 + Gemini 3.8 Flash 验证 operator interrupt 经由 Service 完成 cancel/follow-up，保留上下文并通过 workspace verifier。
- 真实 idle timeout 验证在 2 秒无进展后自动生成 `idle-nudge`，首轮返回 `cancelled`，追加轮返回 `AUTO-IDLE-OK`，最终任务进入 `completed`。
