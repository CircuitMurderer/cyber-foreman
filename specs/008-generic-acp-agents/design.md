# SPEC-008 技术设计

## 组件

```text
agents.json / built-in presets
              │
              ▼
       ACP Agent Profile
              │
     resolve + version + initialize
              │
              ▼
     Generic ACP Adapter ───── Adapter status
              │                      │
              ▼                      ▼
      ACP JSON-RPC session       REST / Web UI
```

通用实现放在 `internal/agent/acpagent`。`internal/agent/opencode` 保留为兼容包装层，负责把旧 Config 转换成 OpenCode profile。

## Profile

Profile 只描述进程启动和 ACP 认证选择：

```json
{
  "name": "grok",
  "command": "grok",
  "args": ["--no-auto-update", "agent", "stdio"],
  "version_args": ["version"],
  "auth_methods": ["xai.api_key", "cached_token"],
  "auth_optional": true
}
```

配置不提供 env value 字段。运行时凭据由 Foreman 进程环境继承，避免配置文件成为密钥仓库。

## 可用性与健康度

`installed` 表示命令可以解析为可执行文件；`healthy` 表示最近一次 ACP initialize 成功且协议版本兼容。健康探测不会创建 session，因此不会触发模型费用。

状态由 Adapter 内部锁保护。Registry 通过可选的 `StatusProvider` 接口取得状态；不实现该接口的 Adapter 默认视为 installed/healthy。

## 启动与认证

实际任务启动流程：

1. 重新解析 executable。
2. 启动 profile command/args。
3. 发送 `initialize`。
4. 若 Agent 宣告 auth methods，则按 profile 顺序选择并发送 `authenticate`。全部失败且 `auth_optional=true` 时记录警告并继续，让 Agent 使用 BYOK provider 的环境凭据。
5. 发送 `session/new`。
6. 后续 Prompt、cancel、permission 和 session update 沿用现有 ACP 生命周期。

## 错误边界

- Profile 语法错误阻止 Foreman 启动，因为管理员配置不明确。
- 单个 Agent 未安装或握手失败不阻止 Foreman 启动。
- 不健康 Agent 在任务入队前被拒绝，避免生成必然失败的异步任务。
- Agent 在探测后被删除时，启动阶段再次解析并将任务明确置为 failed。

## 兼容性

- `opencode.NewAdapter(Config)` 继续存在。
- `foreman opencode --bin` 继续使用兼容包装层。
- `serve --opencode-bin` 继续存在，并新增 `--grok-bin`、`--agents-file`。
