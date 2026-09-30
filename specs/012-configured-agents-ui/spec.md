# SPEC-012：配置驱动的 Agent 与统一任务操作区

- 状态：ACCEPTED
- 负责人：Cyber Foreman contributors
- 创建日期：2026-09-30
- 最后更新：2026-09-30

## 背景

内网部署机器上的 Agent 安装路径、模型端点和默认项目目录各不相同。把 OpenCode、Grok 和 Codex 固定注册在启动代码中，会让前端出现未部署的选项，也迫使运维人员维护大量命令行参数。同时，任务继续、结束和停止动作分散在输入框两侧，容易造成操作含义不清。

## 目标

- 使用 Go 标准库 JSON 配置所有可选 Agent，不引入配置解析依赖。
- 只有配置文件声明的 Agent 出现在前端选择器。
- 配置支持控制协议、命令、Provider 格式、默认模型和默认工作区。
- API key 只引用环境变量名，禁止写入配置文件。
- 任务干预使用一个完整输入区域和右下角统一圆形动作坞。

## 非目标

- 不在本阶段提供浏览器内配置编辑器。
- 不热重载 Agent 配置；修改后重启 Foreman 生效。
- 不让 Foreman 猜测 ACP Agent 私有的 Provider 配置格式。
- 不在本阶段实现 HTTP API 认证、目录白名单或命令授权。

## 功能要求

- REQ-001：`serve` 默认读取 `config/agents.json`，`--agents-file` 可覆盖路径。
- REQ-002：配置必须由 `encoding/json` 严格解析，未知字段、重复名称和尾随 JSON 必须拒绝。
- REQ-003：`driver` 必须支持 `acp` 和 `codex-app-server`。
- REQ-004：`provider.format` 必须支持 `openai`、`anthropic` 和 `google`；配置不得接受明文 API key 字段。
- REQ-005：`default_model` 和 `default_workspace` 必须同时供前端预填和后端缺省值使用。
- REQ-006：内建 process Adapter 必须保留，但不得出现在 Agent 选择器。
- REQ-007：Codex Provider 配置必须实际驱动现有 session-local bridge；Google 使用 OpenAI Chat compatibility。
- REQ-008：ACP Provider 元数据必须作为 `FOREMAN_PROVIDER_*` 环境传给包装脚本，模型名称仍由 Agent catalog 解析。
- REQ-009：本地覆盖文件 `config/agents.local.json` 必须被 Git 忽略，并由 `scripts/dev` 自动优先使用。
- REQ-010：任务操作区必须使用单一文本框，继续/打断、结束和停止按钮统一位于右下角。
- REQ-011：操作按钮必须为带中心图标的圆形按钮，并在 hover/focus 时显示文字说明。
- REQ-012：原任务配置卡片中无行为的装饰加号必须移除。
- REQ-013：执行器协议、Provider 格式和版本仅通过选择器悬浮提示展示，不得挤占表单布局。
- REQ-014：监督策略开关必须与工作区策略位于同一行，展开面板自身显示标题和用途。
- REQ-015：事件、监工决策、工具活动和验证结果指标卡必须可点击查看对应的持久事件详情。
- REQ-016：删除、结束和停止等破坏性动作必须使用统一 UI 对话框确认，不得调用浏览器原生确认框。

## 安全不变量

- INV-001：Agent 配置不允许保存 API key、token 或密码值。
- INV-002：只有环境变量名可以进入 Provider 配置和公开 Adapter 元数据。
- INV-003：未配置的 Agent 不得因本机恰好安装而出现在前端。
- INV-004：配置默认工作区不能绕过后续目录白名单；白名单仍是独立安全阶段。
- INV-005：UI 重排不得改变任务动作的后端状态约束。

## 验收标准

- AC-001：配置加载/构建测试覆盖 ACP、Codex、三种 Provider 及非法字段。
- AC-002：Registry API 返回 selectable、driver、Provider 格式和默认值。
- AC-003：省略任务模型和目录时，应用层使用配置默认值。
- AC-004：前端只渲染 selectable Agent，并通过类型检查与生产构建。
- AC-005：全量 Go 测试、vet 和 race 测试通过。
- AC-006：四类指标均可打开详情弹窗，所有破坏性操作均显示 HeroUI 确认对话框。
