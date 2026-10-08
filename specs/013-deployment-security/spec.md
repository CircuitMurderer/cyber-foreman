# SPEC-013：部署访问边界

- 状态：ACCEPTED
- 负责人：Cyber Foreman contributors
- 创建日期：2026-10-08
- 最后更新：2026-10-08

## 背景

Foreman 能启动本地 Agent、直接命令和验证命令，也保存完整任务记录。只依赖 loopback 默认值不足以支持内网共享部署：服务端必须独立验证工作目录、直接命令和调用者身份，不能信任前端隐藏选项。

## 目标

- 在 Agent JSON 配置中增加可选、严格解析的部署安全策略。
- 将任务限制在规范化后的工作目录根路径内。
- 用结构化 argv 前缀白名单限制直接命令和验证命令。
- 为 REST/SSE 控制平面增加可选 Token 认证。
- 让浏览器通过 HttpOnly 会话使用受保护的 API，不把 Token 持久化到 JavaScript 存储。
- 非回环监听地址必须启用 API 认证。

## 非目标

- 不解析或拦截 Agent 自己在 ACP/App Server 内部发起的工具命令。
- 不实现多用户、RBAC、审计账户或外部身份提供商。
- 不在 Foreman 内终止 TLS；局域网部署由反向代理提供 HTTPS。

## 功能要求

- REQ-001：`security.workspace_roots` 和 `security.command_allowlist` 必须支持“省略为兼容旧行为、显式空数组为全部拒绝”。
- REQ-002：目录授权必须使用绝对、解析软链接后的规范路径，并正确处理路径分隔边界。
- REQ-003：命令白名单必须对 argv 做精确前缀匹配，不得经过 shell 字符串解析。
- REQ-004：任务写入数据库及启动 Adapter 前必须完成授权，拒绝返回 HTTP 403 `task_forbidden`。
- REQ-005：`security.api_token_env` 只能引用合法环境变量名；变量缺失时启动失败。
- REQ-006：受保护 API 同时接受 `Authorization: Bearer` 和浏览器会话 Cookie，比较过程不得保存或回传明文 Token。
- REQ-007：健康检查、静态前端及认证入口公开，其余 `/api/*` 均需认证。
- REQ-008：Web 登录页只把 Token 发送给同源认证入口，成功后依赖 HttpOnly、SameSite Cookie。
- REQ-009：未配置 API Token 时，只允许监听明确的 loopback 地址；通配、局域网和其他主机地址必须拒绝启动。

## 安全不变量

- INV-001：浏览器 Token 不写入 localStorage、sessionStorage、URL 或可读 Cookie。
- INV-002：工作目录检查不能被 `..`、路径前缀碰撞或 symlink 绕过。
- INV-003：空命令白名单不得被解释为无限制。
- INV-004：认证关闭时必须保持当前 loopback 开发流程兼容。
- INV-005：认证不能破坏同源 SSE 事件流。
- INV-006：服务不得在没有认证的情况下绑定非回环接口。

## 验收标准

- AC-001：配置测试覆盖合法策略、相对目录、空命令前缀和非法环境变量名。
- AC-002：策略测试覆盖目录逃逸、命令拒绝、验证命令拒绝和显式空限制。
- AC-003：API 测试覆盖未认证、Bearer、错误 Token、浏览器 Cookie 和公开健康检查。
- AC-004：前端类型检查与生产构建通过，认证开启时可登录并继续读取任务与 SSE。
- AC-005：全量测试、vet、race 与差异检查通过。
- AC-006：监听地址测试覆盖 IPv4/IPv6 loopback、通配地址、局域网地址、主机名和已认证网络监听。
