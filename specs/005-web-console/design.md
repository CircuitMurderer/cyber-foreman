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

事件只保留当前页面最多 300 条，避免长会话无限占用浏览器内存。事件 data 默认摘要展示，用户可展开查看结构化 JSON。
