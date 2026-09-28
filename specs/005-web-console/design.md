# SPEC-005 技术设计

## 技术栈

- React 19 + TypeScript + Vite
- HeroUI v3 (`@heroui/react`, `@heroui/styles`)
- Tailwind CSS v4，仅用于 HeroUI 样式基础和少量 utility
- pnpm 12，锁定 `packageManager`
- lucide-react 图标

前端位于 `web/`，不进入 Go module。API 类型由前端显式维护，后续可用 OpenAPI 替换手工类型。

## 数据流

```text
Create Form ──POST──> /api/v1/tasks
Task List   <─poll─── /api/v1/tasks
Task Detail <─SSE──── /api/v1/tasks/{id}/events
Actions      ─POST──> /api/v1/tasks/{id}/actions
```

任务列表使用低频轮询确保即使 SSE history gap 也能恢复最终快照；选中任务使用 EventSource 获得实时事件。浏览器原生 EventSource 自动携带 Last-Event-ID 重连。

## 静态托管

开发时 Vite 将 `/api` 与 `/healthz` 代理到 `127.0.0.1:8090`。生产时 `foreman serve --web-dir web/dist` 从磁盘提供构建产物；文件不存在时返回 `index.html`，但 API 路径仍由 Go mux 的更具体 route 处理。

第一版不 embed 构建产物，避免提交生成文件并保持前后端独立构建。以后发布单二进制时再增加 `go:embed` 构建步骤。

## 状态与错误

UI 只使用后端状态值。任务创建和动作请求有独立 pending/error 状态；失败显示错误条，不清空用户输入。EventSource 断线显示“正在重连”，任务轮询继续运行。

事件 data 默认摘要展示，用户可展开查看结构化 JSON。

## 对话聚合

应用层在每次实际调用 Adapter Prompt 前发布 `conversation.message`，其中包含 `operator` 或 `supervisor` 来源。前端按事件 sequence 合并 `agent_message_chunk`，遇到下一条 Prompt 或 follow-up 边界时开启新一轮回复。

浏览器最多保留 4096 条任务事件，与当前后端默认历史上限一致。对话弹窗提供按角色排列的记录和按轮次聚合的完整回复；当前记录仍随服务进程重启而丢失，后续由持久化层解决。
