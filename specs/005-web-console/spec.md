# SPEC-005：Foreman Web 控制台

- 状态：IMPLEMENTED
- 创建日期：2026-09-21
- 最后更新：2026-09-21
- 依赖：SPEC-004

## 背景与问题

现有 Foreman 能通过 REST/SSE 创建、观察和干预任务，但主要交互仍依赖 curl。最终产品需要一个由同一 Go 进程提供的浏览器控制台，使用户无需理解 API schema 即可操作 OpenCode 与进程任务。

## 目标

- 使用 React、TypeScript、HeroUI v3 与 pnpm 提供可操作的本地控制台。
- 可创建 agent/command 任务并配置监督和验证策略。
- 可查看任务状态、实时事件、验证结果与 Supervisor 决策。
- 可对运行中的 Agent 执行 interrupt/follow-up，对未结束任务执行 cancel。
- 可按轮次查看用户、监工与 Agent 的对话，并读取聚合后的完整回复。
- 生产构建由 Foreman HTTP 服务同源托管；开发模式由 Vite 代理 REST/SSE。

## 非目标

- 本规格不增加登录、多用户、远程网络暴露或权限配置页面。
- 本规格不实现历史持久化；刷新后的数据仍受当前进程内存历史约束。
- 本规格不在浏览器保存 Provider API Key。

## 使用场景

### SCN-001：创建 OpenCode 任务

Given `opencode` Adapter 可用  
When 用户填写工作区、Prompt、模型、超时和验证设置并提交  
Then UI 创建任务、选中任务详情，并显示从 queued 到最终状态的实时变化。

### SCN-002：纠偏运行中的 Agent

Given Agent task 为 running  
When 用户输入补充信息并点击“打断并追加”  
Then UI 调用 interrupt action，显示成功或结构化错误，并在事件流中展示决策、cancel 和 follow-up。

### SCN-003：观察验证结果

Given Agent turn 已结束  
When任务进入 verifying  
Then UI 显示 verification.started/finished 和最终 completed 或 attention_required 状态。

### SCN-004：查看完整对话

Given Agent 已产生一个或多个流式文本 chunk
When 用户打开“对话与回复”弹窗
Then UI 按轮次展示用户/监工指令，并将同一轮 Agent chunk 合并为连续的完整回复。

## 功能要求

- REQ-001：前端必须从 `/api/v1/adapters` 动态读取 Adapter 与能力，不能硬编码可用 Agent。
- REQ-002：创建表单必须保证 prompt/command 恰好选择一种，并将 duration 作为后端支持的字符串提交。
- REQ-003：任务列表必须显示状态、Adapter、类型、工作区与更新时间，并允许选择任务。
- REQ-004：任务详情必须通过 task SSE 展示有序事件；重连由浏览器 Last-Event-ID 机制处理。
- REQ-005：运行中 agent task 必须提供 interrupt/follow-up；所有非终态任务必须提供 cancel。
- REQ-006：API 错误必须以用户可见方式展示，不得导致页面白屏。
- REQ-007：前端不得请求、持久化或显示 Provider API Key。
- REQ-008：Go 服务必须支持 SPA 静态文件和前端路由 fallback，同时保持 `/api/v1` 与 `/healthz` 优先。
- REQ-009：前端必须在窄屏下退化为单列布局，并保持主要操作可用。
- REQ-010：后端必须记录实际发送给 Agent 的初始、人工和自动追加指令；前端必须区分其来源并聚合 Agent 文本 chunk。

## 安全与不变量

- INV-001：UI 不能构造后端不支持的状态迁移。
- INV-002：Prompt、命令和工作区只发送给同源 Foreman，不写入 localStorage。
- INV-003：浏览器控制台不增加新的凭据入口。
- INV-004：UI 的“完成”展示只来源于后端 task status，不能根据 Agent 文本推断。

## 验收标准

- AC-001：`pnpm build` 和 TypeScript 检查通过。
- AC-002：Go 测试、vet 与 build 通过。
- AC-003：通过 UI 可创建任务、查看实时事件、interrupt 和 cancel。
- AC-004：生产构建访问 `/` 返回前端，未知非 API 路径 fallback 到 `index.html`。
- AC-005：API 不可用、空任务列表和 SSE 断线都有明确 UI 状态。
- AC-006：通过真实 OpenCode 任务可在弹窗中看到原始指令和无 chunk 断裂的完整回复，并可复制单轮回复。

## 验证矩阵

| 要求 | 验收项 | 验证 |
|---|---|---|
| REQ-001..REQ-007 | AC-001, AC-003, AC-005 | 前端构建与手工 REST/SSE 闭环 |
| REQ-008 | AC-002, AC-004 | `internal/api` 测试与 Go build |
| REQ-009 | AC-005 | 390px/桌面浏览器检查 |
| REQ-010 | AC-006 | prompt 事件单测与真实 OpenCode 弹窗验收 |
