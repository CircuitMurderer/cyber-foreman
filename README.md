# 赛博监工

一个面向编码 Agent 的本地控制平面骨架。当前版本负责启动和观察子进程、维护任务状态、发布结构化事件，并提供最小 HTTP/SSE API。后续可以通过 Adapter 接入 Grok Build ACP、OpenCode 和 Codex CLI。

## 当前包含

- 统一的 `agent.Adapter` 接口和能力声明
- 通用非交互子进程 Adapter
- 明确的任务状态机
- 内存任务仓库和实时事件总线
- 初始确定性监督规则
- 本地 HTTP API 和 SSE 事件流
- 项目内 Go 1.26.8 工具链，不修改全局 PATH

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

另一个终端创建任务：

```bash
curl -X POST http://127.0.0.1:8090/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{"command":["sh","-c","echo hello; sleep 1; echo finished"]}'
```

查看任务和事件：

```bash
curl http://127.0.0.1:8090/api/v1/tasks
curl -N http://127.0.0.1:8090/api/v1/events
```

## 目录

```text
cmd/foreman/             CLI 与 HTTP 服务入口
internal/agent/          Agent 适配协议
internal/agent/process/  通用子进程适配器
internal/app/            任务编排应用层
internal/domain/         任务和事件类型
internal/event/          实时事件总线
internal/supervisor/     监督与纠偏规则
internal/task/           状态机
internal/api/            HTTP/SSE 控制面
```

## 下一步

1. 增加 Grok Build ACP Adapter，以 JSON-RPC 代替终端解析。
2. 将任务、事件和恢复检查点持久化到 SQLite。
3. 接入内网本地模型，输出结构化监督决策。
4. 增加可配置验证器和有限次数的自动恢复。
5. 在对外监听前加入认证、工作目录白名单和命令权限策略。

> 当前 HTTP API 可以启动任意本地命令，因此默认只监听 `127.0.0.1`，不要直接暴露到局域网。
