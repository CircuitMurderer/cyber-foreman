# 赛博监工

一个面向编码 Agent 的本地控制平面骨架。当前版本负责启动和观察子进程、维护任务状态、发布结构化事件，并提供最小 HTTP/SSE API。后续可以通过 Adapter 接入 Grok Build ACP、OpenCode 和 Codex CLI。

## 当前包含

- 统一的 `agent.Adapter` 接口和能力声明
- 通用非交互子进程 Adapter
- 明确的任务状态机
- 内存任务仓库、带 sequence/replay 的实时事件总线
- 初始确定性监督规则
- 本地 HTTP API 和 SSE 事件流
- 项目内 Go 1.26.8 工具链，不修改全局 PATH
- 项目内 OpenCode 1.18.31 与 ACP v1 Adapter

## 快速开始

检查工具链：

```bash
./scripts/go version
```

运行测试：

```bash
./scripts/test
```

直接监督一个命令：

```bash
./scripts/go run ./cmd/foreman run -- sh -c 'echo working; sleep 1; echo done'
```

启动本地控制面：

```bash
./scripts/dev
```

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
GOOGLE_GENERATIVE_AI_API_KEY="..." ./scripts/go run ./cmd/foreman opencode \
  --model "google/gemini-3.8-flash" \
  --prompt "检查这个项目并概括当前架构"
```

OpenCode Prompt 已由 `app.Service` mailbox 调度。可以配置空转和硬超时：

```bash
GOOGLE_GENERATIVE_AI_API_KEY="..." ./scripts/go run ./cmd/foreman opencode \
  --idle-timeout 90s \
  --timeout 30m \
  --max-nudges 2 \
  --max-retries 2 \
  --prompt "实现需求并运行项目测试"
```

发生 idle timeout 时，Rule Engine 会生成带预算和去重键的 Decision。由于 OpenCode ACP 不支持真正的 mid-turn message，执行器会发送 `session/cancel`，等待当前轮返回，再在同一 session 中追加确定性的监工提示。连续干预会使用递增的 idle backoff；hard timeout 会停止自动执行并转为 `attention_required`。

在 OpenCode 开始输出后取消当前轮，并在同一会话中追加信息：

```bash
GOOGLE_GENERATIVE_AI_API_KEY="..." ./scripts/go run ./cmd/foreman opencode \
  --model "google/gemini-3.8-flash" \
  --prompt "先分析当前实现并给出完整方案" \
  --interrupt-with "补充信息：优先考虑完全离线部署，请据此重新回答"
```

`--interrupt-with` 会在首个 Agent 文本片段出现后发送 ACP `session/cancel`，等待当前轮返回，再复用原 session ID 发送追加 Prompt。

Adapter 与 Provider 解耦：Cyber Foreman 使用 ACP 控制 OpenCode，OpenCode 再使用其原生 Google Provider 调用 Gemini。未来接入 OpenAI 和 Anthropic 时不需要修改 ACP 协议层。
当前 `foreman opencode` 默认模型是 `google/gemini-3.8-flash`，仍可通过 `--model` 覆盖。

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

`foreman serve` 同时注册 `process` 和 `opencode` Adapter。创建任务使用独立的 v1 DTO，任务会先以 `queued` 状态返回，再异步启动 Adapter：

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

SSE 事件带有 `id`、`version`、`sequence` 和 `occurred_at`。当前内存历史最多保留最近 4096 个事件和约 16 MiB；cursor 太旧时会产生 `stream.gap`，客户端应重新读取任务快照。测试或工作区验证任一失败时，任务不会进入 `completed`，而会进入 `attention_required`。

## 目录

```text
cmd/foreman/             CLI 与 HTTP 服务入口
internal/agent/          Agent 适配协议
internal/agent/process/  通用子进程适配器
internal/agent/opencode/ OpenCode ACP 适配器
internal/acp/            ACP v1 JSON-RPC 客户端
internal/app/            任务编排应用层
internal/domain/         任务和事件类型
internal/event/          实时事件总线
internal/supervisor/     监督与纠偏规则
internal/task/           状态机
internal/api/            HTTP/SSE 控制面
```

## 下一步

1. 完成 SPEC-004 的真实 OpenCode REST interrupt/cancel 验收。
2. 完成 SPEC-003 剩余能力：断联重试、测试失败自动修复和模拟 ACP 全闭环测试。
3. 将任务、事件、监督决策和恢复检查点持久化到 SQLite。
4. 接入 OpenAI、Anthropic 和 Google 三类命名 Provider 配置。
5. 接入内网本地模型，作为低于确定性规则优先级的建议决策器。
6. 在对外监听前加入认证、工作目录白名单和命令权限策略。

> 当前 HTTP API 可以启动任意本地命令，因此默认只监听 `127.0.0.1`，不要直接暴露到局域网。
