# 赛博监工

一个面向编码 Agent 的本地控制平面。当前版本可以启动和观察子进程、维护任务状态、执行确定性监督与完成门禁，并通过 Web 控制台或 HTTP/SSE API 操作 OpenCode、Grok Build 和 Codex CLI。

## 当前包含

- 统一的 `agent.Adapter` 接口和能力声明
- 通用非交互子进程 Adapter
- 明确的任务状态机
- SQLite 任务仓库、持久 sequence/replay 与实时事件总线
- 初始确定性监督规则
- 可选 OpenAI-compatible 语义复核与预算化自动纠偏
- 本地 HTTP API 和 SSE 事件流
- React、TypeScript、HeroUI v3 Web 控制台
- 对话记录弹窗与 Agent 流式 chunk 完整回复聚合
- 同一 ACP session 的多轮继续对话、任务总结与历史删除
- 可选 Git worktree 隔离、任务 diff 展示与审查反馈闭环
- Go 1.26.8、Node.js 22.18 与 pnpm 用户级工具链
- 通用 ACP v1 Adapter、启动健康探测与 OpenCode/Grok Build preset
- Codex App Server Adapter，支持共享登录态或 Responses、Chat Completions、Anthropic Messages API
- 项目内 OpenCode 1.18.31 测试工具

## 快速开始

检查工具链：

```bash
go version
node --version
pnpm --version
```

运行测试：

```bash
./scripts/test
```

直接监督一个命令：

```bash
./scripts/go run ./cmd/foreman run -- sh -c 'echo working; sleep 1; echo done'
```

安装前端依赖、构建前后端：

```bash
cd web && pnpm install && cd ..
./scripts/build
```

启动本地控制面和 Web 控制台：

```bash
./scripts/dev
```

然后访问 <http://127.0.0.1:8090/>。开发前端时可以另开终端运行 `cd web && pnpm dev`；Vite 会把 `/api` 和 `/healthz` 代理到 Go 服务。

`serve` 默认把任务和完整事件时间线保存在 `data/foreman.db`。可通过 `--db /path/to/foreman.db` 修改位置；数据库目录不会提交到 Git。任务状态和事件索引使用关系字段，Agent 原始事件载荷使用 JSON，因此扩展新事件不需要为每种 payload 改表。服务重启时，无法恢复进程句柄或 ACP session 的未结束任务会被明确转为 `attention_required`。

## 模型接口配置

三个 Agent 都可以接 OpenAI-compatible 和 Anthropic-compatible API；Google 的优先级较低，目前 OpenCode 使用原生 Google Provider，Grok Build 和 Codex 使用 Google 官方 OpenAI compatibility。默认示例以 DeepSeek 验证，换成内网 Qwen 等服务时只需修改 base URL 和 model，不需要改 Adapter 或 REST API。

密钥只通过启动 Foreman/Agent 时的环境变量提供：

```bash
export FOREMAN_AGENT_API_KEY_OPENAI="..."
export FOREMAN_AGENT_API_KEY_ANTHROPIC="..."
export FOREMAN_AGENT_API_KEY_GOOGLE="..."
```

本地开发可以把这些 `export` 放进已被 `.gitignore` 排除的 `.api_key`，运行前执行 `source .api_key`。仓库中的 [OpenCode provider catalog](config/opencode-providers.example.json) 和 [Grok Build provider template](config/grok-providers.example.toml) 只有环境变量名，没有密钥。

| Agent | OpenAI | Anthropic | Google |
|---|---|---|---|
| OpenCode | `foreman-openai/deepseek-flash` | `foreman-anthropic/deepseek-flash` | `foreman-google/gemini-3.8-flash`（原生） |
| Grok Build | `foreman-openai` | `foreman-anthropic` | `foreman-google`（OpenAI compatibility） |
| Codex CLI | `responses` 或 `chat-completions` | `anthropic-messages` | `chat-completions`（OpenAI compatibility） |

OpenCode 包装脚本默认加载上述 catalog；部署者显式设置 `OPENCODE_CONFIG` 时不会被覆盖。Grok Build 使用前复制一次模板：

```bash
mkdir -p .cache/grok
cp config/grok-providers.example.toml .cache/grok/config.toml
```

`scripts/grok` 默认使用 `.cache/grok`，也尊重显式 `GROK_HOME`。格式和 Agent 的控制协议彼此独立：OpenCode/Grok 仍由 ACP 控制，Codex 仍由 App Server 控制。

## OpenCode ACP

OpenCode 安装在 `.tools/opencode/1.18.31`，通过项目内包装脚本运行。包装脚本把 OpenCode 的配置、数据和缓存目录放在项目 `.cache/opencode`，并关闭自动更新检查，不修改全局环境：

```bash
./scripts/opencode --version
```

使用 provider catalog 中的模型：

```bash
source .api_key
./scripts/opencode run -m foreman-openai/deepseek-flash "只回复 OK"
./scripts/opencode run -m foreman-anthropic/deepseek-flash "只回复 OK"
./scripts/opencode run -m foreman-google/gemini-3.8-flash "只回复 OK"
```

包装脚本会把 `FOREMAN_AGENT_API_KEY_GOOGLE` 映射给 OpenCode 原生 Google Provider；为兼容旧用法，也继续接受 `GEMINI_API_KEY` 和 `GOOGLE_GENERATIVE_AI_API_KEY`，不会把值落盘。

通过 Cyber Foreman 启动 ACP 会话并发送 Prompt：

```bash
source .api_key
./scripts/go run ./cmd/foreman opencode \
  --model "foreman-openai/deepseek-flash" \
  --prompt "检查这个项目并概括当前架构"
```

OpenCode Prompt 已由 `app.Service` mailbox 调度。可以配置空转和硬超时：

```bash
./scripts/go run ./cmd/foreman opencode \
  --idle-timeout 90s \
  --timeout 30m \
  --max-nudges 2 \
  --max-retries 2 \
  --prompt "实现需求并运行项目测试"
```

发生 idle timeout 时，Rule Engine 会生成带预算和去重键的 Decision。由于 OpenCode ACP 不支持真正的 mid-turn message，执行器会发送 `session/cancel`，等待当前轮返回，再在同一 session 中追加确定性的监工提示。连续干预会使用递增的 idle backoff；hard timeout 会停止自动执行并转为 `attention_required`。

在 OpenCode 开始输出后取消当前轮，并在同一会话中追加信息：

```bash
./scripts/go run ./cmd/foreman opencode \
  --model "foreman-openai/deepseek-flash" \
  --prompt "先分析当前实现并给出完整方案" \
  --interrupt-with "补充信息：优先考虑完全离线部署，请据此重新回答"
```

`--interrupt-with` 会在首个 Agent 文本片段出现后发送 ACP `session/cancel`，等待当前轮返回，再复用原 session ID 发送追加 Prompt。

Adapter 与 Provider 解耦：Cyber Foreman 使用 ACP 控制 OpenCode，OpenCode 再用所选 Provider 调用模型。`foreman opencode` 的旧默认模型仍为 `google/gemini-3.8-flash`，实际使用内网模型时应通过 `--model` 显式选择。

## 通用 ACP Agent

`serve` 默认读取标准库 JSON 配置 [config/agents.json](config/agents.json)，只有 `agents` 数组中的项目会出现在 Web 控制台的 Agent 选择器。启动时会检查可执行文件、版本、登录状态和协议握手；未安装、未登录或协议不兼容的已配置 Agent 会显示为不可用，但不会阻止其他 Agent 启动。

本机路径不要写进提交的默认配置。复制示例到被 Git 忽略的本地文件；`scripts/dev` 会自动优先加载它：

```bash
cp config/agents.example.json config/agents.local.json
# 编辑 command、default_workspace、provider 和 default_model
./scripts/dev
```

也可以显式加载部署配置：

```bash
./bin/foreman serve --agents-file /etc/cyber-foreman/agents.json
```

配置使用 Go 标准库 `encoding/json` 严格解析，不引入 YAML/TOML 依赖。每个条目支持：

- `driver`：`acp` 或 `codex-app-server`
- `command`、`args`、`version_args`：可执行文件和结构化协议启动参数
- `default_workspace`：前端默认工作目录，任务未显式传入时后端也会使用
- `default_model`：前端默认模型，任务未覆盖时由后端发送给 Agent
- `provider.format`：`openai`、`anthropic` 或 `google`
- `provider.base_url`、`provider.api_key_env`：端点和密钥环境变量名；配置文件不接受明文 key
- `provider.wire_api`：Codex 的 OpenAI Provider 可选 `chat-completions` 或 `responses`
- `auth_methods`、`auth_optional`：ACP 登录方法及 BYOK 回退策略

### 部署安全边界

同一个配置文件可选地声明 `security`。没有 `security` 时保持本地开发的兼容行为；生产或内网共享部署建议显式配置：

```json
{
  "security": {
    "workspace_roots": ["/srv/source", "/data/projects"],
    "command_allowlist": [
      ["./scripts/test"],
      ["go", "test"],
      ["pnpm", "check"]
    ],
    "api_token_env": "FOREMAN_API_TOKEN"
  }
}
```

- `workspace_roots` 限制任务可使用的工作目录。Foreman 会解析绝对路径和软链接后再判断，避免通过 `..` 或 symlink 逃逸。省略表示不限制；显式 `[]` 表示拒绝所有任务目录。
- `command_allowlist` 使用 argv 前缀匹配，约束 `process` 任务和 Foreman 自己执行的验证命令。例如 `["go","test"]` 允许 `go test ./...`，但不允许 `go env`。省略表示兼容旧行为；显式 `[]` 表示拒绝所有直接及验证命令。
- Agent 内部通过自身工具执行的命令仍由 OpenCode、Grok 或 Codex 的 sandbox/permission 机制控制；Foreman 的命令白名单只覆盖它直接启动的命令。
- `api_token_env` 只保存环境变量名。变量缺失或为空时 Foreman 拒绝启动，Token 不会进入 JSON、日志或 API 响应。

启用认证后，Web 控制台会显示登录页，并把 Token 换成当前浏览器的 `HttpOnly`、`SameSite=Strict` 会话 Cookie。CLI 可直接使用 Bearer Token：

```bash
export FOREMAN_API_TOKEN='replace-with-a-long-random-token'
./scripts/dev

curl -H "Authorization: Bearer $FOREMAN_API_TOKEN" \
  http://127.0.0.1:8090/api/v1/tasks
```

`GET /healthz`、Web 静态资源和认证入口保持公开，其余 `/api/*` 均要求有效 Cookie 或 Bearer Token。默认监听地址仍是 `127.0.0.1`；若改为通配、局域网或其他非回环地址，Foreman 会强制要求 Token，否则拒绝启动。跨机器访问还应由可信反向代理提供 HTTPS。

### LLM 辅助监督

Foreman 可以在 Agent 每轮结束且确定性 Git/命令验证通过后，再调用一个 OpenAI-compatible 模型做保守的语义复核。确定性规则始终优先：验证失败会直接进入原有修复或人工处理流程，LLM 不能把失败改成通过；LLM 超时、断网、返回非法 JSON 或判断为 `uncertain` 时均 fail-open，不影响原有完成流程。

默认 `config/agents.json` 使用 DeepSeek 做外网验证：

```json
{
  "supervisor": {
    "semantic_review": {
      "format": "openai",
      "base_url": "https://api.deepseek.com/v1",
      "api_key_env": "FOREMAN_AGENT_API_KEY_OPENAI",
      "model": "deepseek-chat",
      "timeout": "20s"
    }
  }
}
```

如果存在 `config/agents.local.json`，`scripts/dev` 会优先读取它；需要把同一个 `supervisor.semantic_review` 配置块加入本地文件才能启用复核。

内网部署只需把 `base_url`、`model` 和 `api_key_env` 换成本地 Qwen 的 OpenAI-compatible 参数；无鉴权端点可以省略 `api_key_env`。如果配置了变量名但变量缺失，Foreman 会输出警告并关闭辅助复核，其余功能继续运行。每个任务的“最大语义纠偏”默认是 1，设为 0 可禁用该任务的自动纠偏；预算用尽后的 LLM 建议只保留在事件记录中，不会形成无限循环。

语义复核只自动发送有界的操作员指令、当前 Agent 可见回复以及确定性验证结论；不会主动读取源码、diff、工具输出、密钥或隐藏思维内容。操作员指令本身若包含代码或敏感信息仍会随请求发送。使用外部模型意味着任务文本和 Agent 回复会离开本机；敏感项目应改用内网模型或关闭该配置。

Codex 会根据 Provider 字段实际建立 session-local API bridge。ACP Agent 的 Provider 字段同时作为选择器元数据和标准 `FOREMAN_PROVIDER_*` 环境传给包装脚本；OpenCode/Grok 当前仍由各自的 provider catalog 决定具体模型别名，`default_model` 应填写 catalog 中存在的名称。

### Grok Build Provider

项目本地 Grok 二进制放在 `.tools/grok/grok`，运行状态与用户配置隔离在 `.cache/grok`。复制三格式无密钥模板并在启动 Foreman 前注入环境变量：

```bash
mkdir -p .cache/grok
cp config/grok-providers.example.toml .cache/grok/config.toml
source .api_key
export HTTPS_PROXY=http://127.0.0.1:7897
export HTTP_PROXY=http://127.0.0.1:7897
./scripts/dev
```

模板默认使用 DeepSeek OpenAI-compatible 端点，同时提供 Anthropic Messages 和 Google OpenAI compatibility alias。API key 只从三个 `FOREMAN_AGENT_API_KEY_*` 环境变量读取，不会写入 TOML。使用 xAI 登录或 `XAI_API_KEY` 时仍可改用默认 Grok preset，或在自定义 profile 中声明 `xai.api_key`、`cached_token`。

可先独立确认 Grok 与 Gemini 的链路：

```bash
source .api_key
HTTPS_PROXY=http://127.0.0.1:7897 \
HTTP_PROXY=http://127.0.0.1:7897 \
./scripts/grok --no-auto-update --model foreman-openai \
  --permission-mode dontAsk --single "只回复 OK"
```

查看探测结果：

```bash
curl http://127.0.0.1:8090/api/v1/adapters
```

响应还会包含 `selectable`、`driver`、`provider_format`、`default_model` 和 `default_workspace`。内建 `process` Adapter 仍供命令任务使用，但 `selectable=false`，不会混入 Agent 下拉列表。健康探测只执行版本/登录检查和协议 `initialize`，不会创建 session、发送 Prompt 或产生模型费用。

### Codex CLI

Codex 不使用 ACP；Foreman 通过官方 `codex app-server` 的 JSONL 协议接入，并把 Codex 的流式消息、思考摘要、工具活动和审批请求转换为统一事件。登录态模式启动时会执行 `codex login status`，但不会读取或保存 token；API 模式只检查配置的密钥环境变量。Codex CLI 与桌面版共用本机登录缓存，因此已登录桌面版的机器通常无需再次认证；也可先手动确认：

```bash
codex --version
codex login status
```

每个 Foreman 任务启动一个临时 Codex thread，工作区采用 `workspace-write`，审批策略为 `never`；同一任务的“继续”会复用该 thread，打断则调用 `turn/interrupt`。若使用登录态，Codex profile 省略 `provider` 即可；若使用内网 API，则在同一个 profile 中配置 Provider。

内网也可以让 Codex 使用 Responses、OpenAI-compatible Chat Completions 或 Anthropic-compatible Messages，而不依赖 ChatGPT 登录。Foreman 为每个 Codex session 启动一个仅监听 loopback 的临时桥接器：Responses 原样转发，另外两种格式双向转换。API key 只从指定环境变量读取，并从 Codex 子进程环境中剥离，不会作为 Codex 参数、任务事件或数据库字段保存。API 模式还会关闭 Codex analytics、插件功能和远程插件目录刷新，避免启动时访问 ChatGPT/GitHub 控制面；模型请求仍按配置访问指定 API base。

优先使用内网最常见的 OpenAI-compatible Chat Completions：

```bash
source .api_key
export HTTP_PROXY=http://127.0.0.1:7897
export HTTPS_PROXY=http://127.0.0.1:7897
export ALL_PROXY=http://127.0.0.1:7897
```

Codex profile：

```json
{
  "name": "codex",
  "driver": "codex-app-server",
  "command": "codex",
  "args": ["app-server"],
  "default_model": "deepseek-flash",
  "provider": {
    "format": "openai",
    "wire_api": "chat-completions",
    "base_url": "https://api.deepseek.com",
    "api_key_env": "FOREMAN_AGENT_API_KEY_OPENAI"
  }
}
```

使用 Anthropic Messages 时设为 `"format":"anthropic"`；若 OpenAI 上游原生支持 Responses API，则设为 `"wire_api":"responses"`。Google 设为 `"format":"google"`，现阶段通过 `https://generativelanguage.googleapis.com/v1beta/openai` 的 Chat Completions compatibility 接入。

创建任务时选择 `adapter: "codex"` 即可；省略任务级 `model` 时使用 profile 的 `default_model`，传入任务级模型则只覆盖该任务后续 turn。兼容层支持文本流、函数工具调用，以及 Gemini thought signature、DeepSeek reasoning content、Anthropic thinking signature 的跨轮回填；当前不转换 Responses API 专有的托管工具，例如 OpenAI web search。旧的 `--codex-*`、`--opencode-bin` 和 `--grok-bin` 参数只在显式传入 `--agents-file ""` 的兼容模式生效。

## 确定性验证

Rule-based Supervisor 当前已经提供：

- idle/hard timeout 的纯规则判断与可注入时钟
- 带预算和去重键的结构化 Decision
- Action Executor 的能力检查、幂等执行和审计事件
- argv 测试验证器、单命令超时、输出上限和凭据脱敏
- Git HEAD、文件内容基线、敏感路径以及 staged/unstaged `git diff --check`
- workspace 安全门禁优先于测试命令，越界后不会执行可能已被篡改的测试
- 测试失败后在预算内向同一 Agent 追加脱敏修复指令并自动重新验证
- Agent 断联后按 `max_retries` 重建 session，恢复模型配置，并重放有界的受信任指令上下文
- 真实 ACP 子进程故障测试覆盖 cancel/follow-up、transport 断联、session 重建、测试修复与最终验证
- 修复预算耗尽或工作区验证失败后的 `attention_required` 状态
- OpenCode 单任务 mailbox、自动 idle 纠偏和 hard timeout
- Agent turn 结束后的 `verifying → completed/attention_required` 完成门禁

## REST 控制面 v1

`foreman serve` 同时注册 `process`、`opencode`、`grok` 和 `codex` Adapter。创建任务使用独立的 v1 DTO，任务会先以 `queued` 状态返回，再异步启动 Adapter：

```bash
curl -X POST http://127.0.0.1:8090/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{
    "kind":"agent",
    "adapter":"opencode",
    "workspace":"/absolute/path/to/project",
    "input":{"prompt":"实现需求并运行测试"},
    "model":"google/gemini-3.8-flash",
    "supervision":{
      "idle_timeout":"90s",
      "hard_timeout":"30m",
      "max_nudges":2,
      "max_retries":2,
      "max_test_repairs":2
    },
    "verification":{
      "commands":[{"argv":["./scripts/test"],"timeout":"10m"}],
      "workspace":true
    }
  }'
```

对正在运行的 OpenCode turn 进行取消并追加信息：

```bash
curl -X POST http://127.0.0.1:8090/api/v1/tasks/TASK_ID/actions \
  -H 'Content-Type: application/json' \
  -d '{"type":"interrupt","message":"先检查现有接口，不要重写整个模块"}'
```

Agent 一轮完成并通过验证后，交互式 REST 任务进入 `waiting_input`，OpenCode ACP session 会继续保留。此时可以不取消任何 turn，直接继续当前对话：

```bash
curl -X POST http://127.0.0.1:8090/api/v1/tasks/TASK_ID/actions \
  -H 'Content-Type: application/json' \
  -d '{"type":"continue","message":"继续实现，并把刚才提到的边界情况也补上测试"}'
```

不再继续追问时，可以正常结束已经通过验证、正在等待输入的交互任务：

```bash
curl -X POST http://127.0.0.1:8090/api/v1/tasks/TASK_ID/actions \
  -H 'Content-Type: application/json' \
  -d '{"type":"finish"}'
```

任务响应中的 `available_actions` 会明确给出当前可执行的 `interrupt`、`continue`、`finish`、`cancel` 和 `delete`。其中 `interrupt` 用于正在执行的 turn，`continue` 只用于已经等待输入且仍保有 session 的任务，`finish` 只在 `waiting_input` 出现并会把任务正常标记为 `completed`；它不能绕过失败的验证。

创建普通命令任务：

```bash
curl -X POST http://127.0.0.1:8090/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{"adapter":"process","input":{"command":["sh","-c","echo hello; sleep 1; echo finished"]}}'
```

让 Agent 在独立 Git worktree 中执行，避免直接修改当前 checkout：

```bash
curl -X POST http://127.0.0.1:8090/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{"adapter":"opencode","workspace":"/path/to/repo","workspace_mode":"worktree","input":{"prompt":"实现需求并运行测试"}}'
```

源仓库必须处于 clean 状态。Foreman 从当前 `HEAD` 创建 detached worktree，并在任务响应中返回 `source_workspace`、`worktree_root` 和 `base_revision`。查看任务变更：

```bash
curl http://127.0.0.1:8090/api/v1/tasks/TASK_ID/diff
```

Web 控制台的“任务变更”弹窗会展示完整 patch；任务等待输入时，可直接把审查反馈送回同一 Agent session。敏感文件内容会被隐藏，patch 最大返回 1 MiB。删除任务不会删除隔离 worktree，确认框会显示保留路径，避免未提交成果丢失。

查看 Adapter、任务和可重连事件流：

```bash
curl http://127.0.0.1:8090/api/v1/adapters
curl http://127.0.0.1:8090/api/v1/tasks
curl -N http://127.0.0.1:8090/api/v1/tasks/TASK_ID/events
curl -N -H 'Last-Event-ID: evt-42' http://127.0.0.1:8090/api/v1/tasks/TASK_ID/events
```

删除已结束或等待输入的任务及其 SQLite 事件/对话历史：

```bash
curl -X DELETE http://127.0.0.1:8090/api/v1/tasks/TASK_ID
```

SSE 事件带有 `id`、`version`、`sequence` 和 `occurred_at`。`serve` 模式下 cursor 与事件历史由 SQLite 持久化，页面刷新或 Foreman 重启后仍可读取完整时间线；非持久化 CLI 模式保留最近 4096 个事件和约 16 MiB。Agent 断联会经过 `running/waiting_input → recovering → 原状态`，旧 session 的迟到事件会被忽略；恢复上下文只包含操作员与监工指令，Agent 历史输出不重放，当前工作区是进度事实来源。工作区验证失败会立即进入 `attention_required`；普通测试失败会先按 `max_test_repairs` 自动修复并重新验证，预算耗尽后再转人工。

Web 任务详情同时提供“任务总结”和“对话与回复”：前者聚合原始任务、最新完整回复、轮次、工具活动、监工干预和验证结论；后者保留逐轮完整对话。两者都从持久事件重建，不会把模型的流式 chunk 当作互相独立的最终答案。

## 目录

```text
cmd/foreman/             CLI 与 HTTP 服务入口
internal/agent/          Agent 适配协议
internal/agent/process/  通用子进程适配器
internal/agent/acpagent/ 通用 ACP Adapter、profile 和健康探测
internal/agent/opencode/ OpenCode 兼容包装层
internal/agent/codex/    Codex App Server Adapter
internal/acp/            ACP/Codex 共用 JSON-RPC JSONL 客户端
internal/app/            任务编排应用层
internal/domain/         任务和事件类型
internal/event/          实时事件总线
internal/storage/        持久化边界与 SQLite 实现
internal/supervisor/     监督与纠偏规则
internal/task/           状态机
internal/worktree/       Git worktree 隔离与安全 diff
internal/api/            HTTP/SSE 控制面
web/                     React/HeroUI 控制台
```

## 下一步

1. 增加 API 认证、工作目录白名单和命令权限策略。
2. 接入内网本地模型，作为低于确定性规则优先级的建议决策器。

> 当前 HTTP API 可以启动任意本地命令，因此默认只监听 `127.0.0.1`，不要直接暴露到局域网。
