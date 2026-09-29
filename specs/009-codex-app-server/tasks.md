# SPEC-009 实现任务

- [x] T001：确认官方 App Server 与认证协议，并生成本机版本 JSON Schema。
- [x] T002：为 JSONL 客户端增加 Codex versionless wire mode。
- [x] T003：实现 Codex 健康探测、thread 和 turn 生命周期。
- [x] T004：实现 Prompt、续聊、取消、事件转换和断联上报。
- [x] T005：实现安全的审批请求默认拒绝。
- [x] T006：接入 `serve --codex-bin` 与统一 Adapter 列表。
- [x] T007：补充伪 App Server 单元测试和 README。
- [x] T008：运行完整测试、静态检查与本机真实 Codex 验证。
- [x] T009：增加 Codex 自定义 API provider 配置与 session-local Responses/Chat Completions bridge。
- [x] T010：转换文本流、函数工具调用，并跨轮保存 Gemini thought signature。
- [x] T011：补充桥接器单测、README 和 Gemini 3.8 Flash 真实工具调用验证。
- [x] T012：增加原生 Responses 透传和 Anthropic Messages 双向转换。
- [x] T013：保存 DeepSeek reasoning content 与 Anthropic thinking signature，并完成三种格式的真实 Codex 工具调用验证。
