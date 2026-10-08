# SPEC-013 技术设计

## 配置与启动

`internal/agentconfig.LoadFile` 严格解析 Agent 与顶层 `security`。切片的 nil/非 nil 状态保留配置意图：字段省略时不安装对应限制，显式空数组则安装拒绝全部的策略。API Token 配置只保存环境变量名，`serve` 启动时读取真实值。未取得 Token 时，监听地址只允许明确的 IPv4/IPv6 loopback 或 `localhost`，避免误把无认证控制面暴露到内网。

## 任务授权

```text
REST create task
      │
      ▼
app.Service.queueTask
      │ resolve adapter defaults / workspace
      ▼
internal/access.Policy
      ├─ canonical workspace + root containment
      ├─ process argv prefix allowlist
      └─ verification argv prefix allowlist
      │
      ▼
persist task → start adapter
```

`app.RequestPolicy` 是应用层端口，`internal/access` 提供部署实现。这样 Agent Adapter 不感知 HTTP 或部署配置，CLI 构造的 Service 默认也保持兼容。Worktree 任务检查源工作区；随后生成的隔离 worktree 不需要加入用户白名单。

## API 认证

服务端仅保存 Token 的 SHA-256 摘要。CLI Bearer Token 先摘要再常量时间比较。浏览器向公开的 session 入口提交一次 Token，服务端设置包含摘要的 `HttpOnly; SameSite=Strict` Cookie；同源 `fetch` 与 `EventSource` 会自动携带 Cookie。

公开面仅包括 `/healthz`、`/api/v1/auth`、`/api/v1/auth/session` 和静态前端。认证失败统一返回 401 JSON 与 `WWW-Authenticate: Bearer`。

## 前端

应用启动先读取认证状态。未要求认证时直接进入原控制台；要求认证且没有有效 Cookie 时显示独立登录卡。Token 只存在于密码输入框的短生命周期 React state，成功后立即清空。
