# SPEC-008：通用 ACP Agent 接入

- 状态：ACCEPTED
- 负责人：Cyber Foreman contributors
- 创建日期：2026-09-28
- 最后更新：2026-09-28

## 背景与问题

当前 ACP Adapter 把 OpenCode 的名称、启动子命令和能力写死在实现中。内网机器已经安装 OpenCode、Grok Build 等 Agent，Foreman 应复用这些可执行文件，而不是携带或重新安装 Agent。

## 目标

- 使用同一 ACP v1 实现接入不同本地 Agent。
- 支持从 PATH、绝对路径或相对路径解析 Agent 可执行文件。
- 提供 OpenCode 和 Grok Build 的默认启动 profile。
- 启动 Foreman 时探测 Agent 的安装状态、版本和 ACP 握手健康度。
- 通过 REST API 和 Web 控制台暴露可用状态，阻止向不可用 Agent 派发任务。
- 支持需要 ACP `authenticate` 的 Agent。

## 非目标

- 本规格不实现 ACP session 在 Foreman 重启后的恢复。
- 本规格不实现用户交互式登录或密钥管理。
- 本规格不实现通用终端文本解析降级。
- 本规格不实现 Agent 自动安装或更新。

## 使用场景

### SCN-001：使用 PATH 中的 OpenCode

Given `opencode` 已在 Foreman 的 PATH 中  
When Foreman 加载 OpenCode profile  
Then 系统必须解析可执行文件并使用 `opencode acp` 完成 ACP v1 握手。

### SCN-002：使用内网预装 Grok Build

Given `grok` 已安装并已登录或注入认证环境变量  
When Foreman 启动 Grok profile  
Then 系统必须使用 `grok --no-auto-update agent stdio`，选择配置允许的认证方法并创建 session。

### SCN-003：Agent 未安装

Given profile 指向不存在的命令  
When Foreman 启动  
Then Foreman 必须继续提供控制面，并把该 Adapter 标记为 `installed=false`、`healthy=false`，且拒绝向它派发任务。

### SCN-004：自定义 ACP Agent

Given 管理员提供不包含密钥的 JSON profile 文件  
When Foreman 使用 `--agents-file` 启动  
Then 系统必须按文件中的 command、args、version_args 和 auth_methods 注册 Agent。

## 功能要求

- REQ-001：通用 ACP Adapter 必须以 profile 名称作为 Adapter 名称，并执行显式 command/args，不附加 Agent 专属参数。
- REQ-002：命令不包含路径分隔符时必须通过 PATH 查找；包含路径分隔符时必须解析为绝对路径并检查可执行性。
- REQ-003：Adapter 状态必须包含 installed、healthy、resolved command、version、ACP protocol version、agent info 和最近错误。
- REQ-004：健康探测必须完成 ACP `initialize`，但不得创建任务 session 或调用模型。
- REQ-005：profile 声明 auth_methods 且 ACP `initialize` 返回认证方法时，Adapter 必须选择首个受支持的方法；无匹配项时必须明确失败。`auth_optional=true` 时必须记录认证警告并继续，让 Agent 尝试自定义 provider 的环境凭据；未声明 auth_methods 的 profile 直接由 Agent 自身/provider 环境完成认证。
- REQ-006：OpenCode 默认 profile 必须执行 `<binary> acp`。
- REQ-007：Grok 默认 profile 必须执行 `<binary> --no-auto-update agent stdio`，允许 `xai.api_key` 与 `cached_token` 认证，并在两者不可用时允许 BYOK provider 环境接管认证。
- REQ-008：不可用 Adapter 必须继续出现在 Adapter 列表中，但创建任务必须返回冲突错误。
- REQ-009：Web 控制台必须禁用不可用 Adapter，并显示版本或不可用原因。
- REQ-010：profile 文件不得接受明文密钥字段；Agent 子进程继承 Foreman 运行环境中的凭据。
- REQ-011：现有 `foreman opencode` 与 `--opencode-bin` 行为必须保持兼容。

## 状态与不变量

- INV-001：健康探测不得调用 `session/new` 或 `session/prompt`。
- INV-002：未安装或 ACP 握手失败的 Agent 不得接收任务。
- INV-003：Agent 的 stderr、握手错误和版本输出不得把进程环境写入事件。
- INV-004：一个 profile 对应一个唯一 Adapter 名称。

## 接口与事件

- CLI：`foreman serve --grok-bin PATH --agents-file PATH`
- REST：`GET /api/v1/adapters` 增加 `installed`、`healthy`、`command`、`version`、`protocol_version`、`agent_info`、`error`。
- 配置：JSON 对象 `{ "agents": [...] }`。
- 兼容性：新增 REST 字段为向后兼容；旧任务中的 `adapter=opencode` 保持有效。

## 失败与恢复

- 命令不存在：保留 Adapter descriptor，拒绝新任务。
- 版本命令失败：记录错误，但仍继续 ACP initialize 探测。
- ACP initialize 失败或协议版本不兼容：标记 unhealthy。
- 任务启动时 Agent 状态变化：重新解析命令；失败后任务进入 failed，Adapter 状态同步更新。

## 安全与权限

- profile 只保存命令、参数和认证方法 ID，不保存 API key。
- Agent 继承启动 Foreman 时的环境变量。
- 健康探测使用超时并强制回收子进程。

## 验收标准

- AC-001：伪 OpenCode profile 使用 `acp` 参数完成完整 prompt/cancel 生命周期。
- AC-002：伪 Grok profile 收到正确 argv，并在 `session/new` 前收到 `authenticate`。
- AC-003：PATH 命令可解析，缺失命令显示 unavailable 且不影响服务启动。
- AC-004：自定义 JSON profile 可加载，重复名称或无效字段启动失败。
- AC-005：REST 和前端正确展示并禁用不可用 Agent。
- AC-006：`./scripts/test` 全部通过。

## 验证矩阵

| 要求 | 验收项 | 测试/命令 | 证据 |
|---|---|---|---|
| REQ-001~007 | AC-001~004 | `./scripts/go test ./internal/agent/...` | 通过；包含 argv、PATH、认证重试、BYOK 回退、缺失命令与配置校验 |
| REQ-008 | AC-003 | `./scripts/go test ./internal/app ./internal/api` | 通过；不可用 Adapter 返回 HTTP 409 |
| REQ-009 | AC-005 | `pnpm --dir web check` | 通过 |
| 全部 | AC-006 | `./scripts/test` | 2026-09-28 全部通过 |

附加证据：OpenCode 1.18.31 真实 ACP initialize/session-new/stop 握手通过。Grok Build 1.0.41 使用项目本地 `GROK_HOME` 与 Gemini 3.8 Flash 完成真实 CLI 请求；随后通过 Foreman REST 完成 ACP initialize/session-new、首轮 Prompt、流式 chunk 汇总和同 session `continue`，分别返回 `GROK_ACP_OK` 与 `GROK_CONTINUE_OK`。
