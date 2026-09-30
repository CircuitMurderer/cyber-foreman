# SPEC-012 技术设计

## 配置路径

```text
config/agents.json（默认、可提交）
          │ --agents-file 覆盖
          ▼
 internal/agentconfig
   ├─ strict JSON decode
   ├─ schema validation
   └─ build ACP / Codex Adapter
          │
          ▼
 agent.WithMetadata
          │
          ├─ Registry / REST descriptor
          └─ app.Service defaults
```

`internal/agentconfig` 只使用 `encoding/json`、`net/url` 等标准库。Profile 根据 `driver` 构造已有 Adapter，不把 Agent 私有逻辑带入应用层。`agent.Metadata` 提供 `selectable`、`driver`、Provider 格式、默认模型和默认工作区；wrapper 继续完整委托原 Adapter 的生命周期、状态和 Probe。

## Provider

Codex profile 被转换为现有 `codex.ProviderConfig`：OpenAI 默认使用 Chat Completions，可显式选择 Responses；Anthropic 使用 Messages；Google 使用其 OpenAI compatibility 端点。ACP Agent 的模型供应仍由其自身 catalog 负责，Foreman 将声明信息通过 `FOREMAN_PROVIDER_FORMAT`、`FOREMAN_PROVIDER_BASE_URL`、`FOREMAN_PROVIDER_API_KEY_ENV` 和 `FOREMAN_PROVIDER_DEFAULT_MODEL` 传给包装脚本。

配置仅保存 `api_key_env`，实际值继续从启动 Foreman 的环境继承。严格解码会拒绝 `api_key` 等未知字段。

## 默认值

前端切换 Agent 时预填 `default_model` 与 `default_workspace`。后端不能信任前端一定传值，因此 `app.Service` 在请求缺省时再次从 Adapter metadata 取默认值。恢复 session 时 runtime 保存最终解析后的模型。

## 可见性

Registry 仍包含 process Adapter，保证命令任务和内部能力不退化；其 `selectable=false`。Web 选择器只过滤 `selectable=true` 的 Descriptor，因此本机安装但未写入 Agent 配置的程序不会出现。

## 操作区

干预卡片改为纵向布局：完整 textarea 在上，动作坞在右下。圆形按钮只显示图标，hover 和键盘 focus 均显示文字 tooltip；`aria-label` 保证无障碍语义。后端动作、确认对话和可用性判断保持不变。
