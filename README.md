# 赛博监工

一个面向编码 Agent 的本地控制平面。当前版本可以启动和观察子进程、维护任务状态、执行确定性监督与完成门禁，并通过 Web 控制台或 HTTP/SSE API 操作 OpenCode、Grok Build 和 Codex CLI。

## 当前包含

- 统一的 `agent.Adapter` 接口和能力声明
- 通用非交互子进程 Adapter
- 明确的任务状态机
- SQLite 任务仓库、持久 sequence/replay 与实时事件总线
- 初始确定性监督规则
- 本地 HTTP API 和 SSE 事件流
- React、TypeScript、HeroUI v3 Web 控制台
- 对话记录弹窗与 Agent 流式 chunk 完整回复聚合
- 同一 ACP session 的多轮继续对话、任务总结与历史删除
- Go 1.26.8、Node.js 22.18 与 pnpm 用户级工具链
- 通用 ACP v1 Adapter、启动健康探测与 OpenCode/Grok Build preset
- Codex App Server Adapter，共享 Codex CLI/桌面版登录态
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
go run ./cmd/foreman run -- sh -c 'echo working; sleep 1; echo done'
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

## OpenCode ACP

OpenCode 安装在 `.tools/opencode/1.18.31`，通过项目内包装脚本运行。包装脚本把 OpenCode 的配置、数据和缓存目录放在项目 `.cache/opencode`，并关闭自动更新检查，不修改全局环境：

```bash
./scripts/opencode --version
```

使用 Gemini 时，只在当前 shell 注入密钥，不要把密钥写进仓库：

```bash
GOOGLE_GENERATIVE_AI_API_KEY="..." ./scripts/opencode models google
```

包装脚本也兼容 `GEMINI_API_KEY`，运行时会把它映射为 OpenCode 原生 Google Provider 使用的 `GOOGLE_GENERATIVE_AI_API_KEY`，不会落盘。

通过 Cyber Foreman 启动 ACP 会话并发送 Prompt：

```bash
GOOGLE_GENERATIVE_AI_API_KEY="..." go run ./cmd/foreman opencode \
  --model "google/gemini-3.8-flash" \
  --prompt "检查这个项目并概括当前架构"
```

OpenCode Prompt 已由 `app.Service` mailbox 调度。可以配置空转和硬超时：

```bash
GOOGLE_GENERATIVE_AI_API_KEY="..." go run ./cmd/foreman opencode \
  --idle-timeout 90s \
  --timeout 30m \
  --max-nudges 2 \
  --max-retries 2 \
  --prompt "实现需求并运行项目测试"
```

发生 idle timeout 时，Rule Engine 会生成带预算和去重键的 Decision。由于 OpenCode ACP 不支持真正的 mid-turn message，执行器会发送 `session/cancel`，等待当前轮返回，再在同一 session 中追加确定性的监工提示。连续干预会使用递增的 idle backoff；hard timeout 会停止自动执行并转为 `attention_required`。

在 OpenCode 开始输出后取消当前轮，并在同一会话中追加信息：

```bash
GOOGLE_GENERATIVE_AI_API_KEY="..." go run ./cmd/foreman opencode \
  --model "google/gemini-3.8-flash" \
  --prompt "先分析当前实现并给出完整方案" \
  --interrupt-with "补充信息：优先考虑完全离线部署，请据此重新回答"
```

`--interrupt-with` 会在首个 Agent 文本片段出现后发送 ACP `session/cancel`，等待当前轮返回，再复用原 session ID 发送追加 Prompt。

Adapter 与 Provider 解耦：Cyber Foreman 使用 ACP 控制 OpenCode，OpenCode 再使用其原生 Google Provider 调用 Gemini。未来接入 OpenAI 和 Anthropic 时不需要修改 ACP 协议层。
当前 `foreman opencode` 默认模型是 `google/gemini-3.8-flash`，仍可通过 `--model` 覆盖。

## 通用 ACP Agent

`serve` 会注册 OpenCode、Grok Build 与 Codex CLI，并在启动时检查可执行文件、版本、登录状态和协议握手。未安装、未登录或协议不兼容的 Agent 不会阻止 Foreman 启动，但会在 `/api/v1/adapters` 和 Web 控制台中显示为不可用，也不能接收新任务：

```bash
./bin/foreman serve \
  --opencode-bin /usr/local/bin/opencode \
  --grok-bin /usr/local/bin/grok \
  --codex-bin /usr/local/bin/codex
```

OpenCode 使用 `opencode acp`；Grok Build 使用 `grok --no-auto-update agent stdio`。命令可以是绝对/相对路径，也可以是 PATH 中的名称。
仓库中存在可执行的 `scripts/opencode` 或 `scripts/grok` 时，`serve` 会优先使用对应的项目本地包装脚本；否则回退到 PATH。

### Grok Build + Gemini

项目本地 Grok 二进制放在 `.tools/grok/grok`，运行状态与用户配置隔离在 `.cache/grok`。复制无密钥模板并在启动 Foreman 前注入环境变量：

```bash
mkdir -p .cache/grok
cp config/grok-gemini.example.toml .cache/grok/config.toml
source .api_key
export HTTPS_PROXY=http://127.0.0.1:7897
export HTTP_PROXY=http://127.0.0.1:7897
./scripts/go run ./cmd/foreman serve \
  --agents-file config/agents.gemini.example.json
```

模板使用 Google 官方 OpenAI-compatible 端点和 `gemini-3.8-flash`，API key 只从 `GOOGLE_GENERATIVE_AI_API_KEY` 读取，不会写入 TOML。`agents.gemini.example.json` 不声明 ACP `auth_methods`，因此由 Grok 的自定义 model/provider 环境完成认证；使用 xAI 登录或 `XAI_API_KEY` 时可改用默认 Grok preset，或在自定义 profile 中声明 `xai.api_key`、`cached_token`。

可先独立确认 Grok 与 Gemini 的链路：

```bash
source .api_key
HTTPS_PROXY=http://127.0.0.1:7897 \
HTTP_PROXY=http://127.0.0.1:7897 \
./scripts/grok --no-auto-update --model gemini-flash \
  --permission-mode dontAsk --single "只回复 OK"
```

内网存在其他 ACP v1 Agent，或者安装路径需要统一管理时，可以复制 `config/agents.example.json` 并使用：

```bash
./bin/foreman serve --agents-file /etc/cyber-foreman/agents.json
```

profile 支持 `name`、`command`、`args`、`version_args`、`auth_methods` 和 `auth_optional`。`auth_optional=true` 会先尝试列出的 ACP 登录方法，全部失败时再让 Agent 使用自定义 provider 的环境凭据；默认 Grok preset 已开启该行为。配置文件不接受明文密钥；Agent 继承启动 Foreman 时的环境变量和本机登录状态。指定 `--agents-file` 后使用文件内的 Agent 列表，不再加载命令行的 OpenCode/Grok preset。

查看探测结果：

```bash
curl http://127.0.0.1:8090/api/v1/adapters
```

响应会包含 `installed`、`healthy`、解析后的 `command`、`version`、`protocol_version`、`agent_info` 和失败原因。健康探测只执行版本/登录检查和协议 `initialize`，不会创建 session、发送 Prompt 或产生模型费用。

### Codex CLI

Codex 不使用 ACP；Foreman 通过官方 `codex app-server` 的 JSONL 协议接入，并把 Codex 的流式消息、思考摘要、工具活动和审批请求转换为统一事件。启动时会执行 `codex login status`，但不会读取或保存 token。Codex CLI 与桌面版共用本机登录缓存，因此已登录桌面版的机器通常无需再次认证；也可先手动确认：

```bash
codex --version
codex login status
./bin/foreman serve --codex-bin codex
```

每个 Foreman 任务启动一个临时 Codex thread，工作区采用 `workspace-write`，审批策略为 `never`；同一任务的“继续”会复用该 thread，打断则调用 `turn/interrupt`。若内网机器已预装并登录 Codex CLI，只需让 `codex` 位于 PATH，或显式传入 `--codex-bin`。

## 确定性验证

Rule-based Supervisor 当前已经提供：

- idle/hard timeout 的纯规则判断与可注入时钟
- 带预算和去重键的结构化 Decision
- Action Executor 的能力检查、幂等执行和审计事件
- argv 测试验证器、单命令超时、输出上限和凭据脱敏
- Git HEAD、文件内容基线、敏感路径以及 staged/unstaged `git diff --check`
- 验证失败后的 `attention_required` 状态
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
      "max_retries":2
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

任务响应中的 `available_actions` 会明确给出当前可执行的 `interrupt`、`continue`、`cancel` 和 `delete`。其中 `interrupt` 用于正在执行的 turn，`continue` 只用于已经等待输入且仍保有同一 session 的任务。

创建普通命令任务：

```bash
curl -X POST http://127.0.0.1:8090/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{"adapter":"process","input":{"command":["sh","-c","echo hello; sleep 1; echo finished"]}}'
```

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

SSE 事件带有 `id`、`version`、`sequence` 和 `occurred_at`。`serve` 模式下 cursor 与事件历史由 SQLite 持久化，页面刷新或 Foreman 重启后仍可读取完整时间线；非持久化 CLI 模式保留最近 4096 个事件和约 16 MiB。测试或工作区验证任一失败时，任务不会进入 `completed`，而会进入 `attention_required`。

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
internal/api/            HTTP/SSE 控制面
web/                     React/HeroUI 控制台
```

## 下一步

1. 完成 SPEC-003 剩余能力：断联重试、测试失败自动修复和模拟 ACP 全闭环测试。
2. 增加 worktree 隔离、任务 diff 和代码审查反馈闭环。
3. 接入内网本地模型，作为低于确定性规则优先级的建议决策器。
4. 在对外监听前加入认证、工作目录白名单和命令权限策略。

> 当前 HTTP API 可以启动任意本地命令，因此默认只监听 `127.0.0.1`，不要直接暴露到局域网。
