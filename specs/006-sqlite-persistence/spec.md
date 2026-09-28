# SPEC-006：SQLite 本地持久化

- 状态：IMPLEMENTED
- 创建日期：2026-09-28
- 最后更新：2026-09-28
- 依赖：SPEC-004、SPEC-005

## 背景与问题

任务、事件与对话目前只存在于 Foreman 进程内。页面刷新能依赖短期 replay，但服务重启会丢失全部历史，也无法判断哪些任务在重启时被中断。

## 目标

- `serve` 模式默认将任务快照与事件时间线写入本地 SQLite。
- 页面刷新和 Foreman 重启后，REST 列表、任务详情、对话记录和 SSE cursor 仍可恢复。
- 保持事件 payload 的 schema 灵活性，不要求每种 Adapter 事件都增加数据库列。
- 明确处理无法跨进程恢复的运行中任务。
- 使用纯 Go SQLite 驱动，不引入 CGO 或系统级数据库依赖。

## 非目标

- 不在本阶段实现多 Foreman 实例共享一个数据库。
- 不恢复进程句柄、ACP transport 或正在执行的模型 turn。
- 不提供全文搜索、保留周期、压缩或历史清理 UI。
- 不把 Provider key 写入数据库。

## 功能要求

- REQ-001：`serve` 默认使用 `data/foreman.db`，并允许用 `--db` 覆盖路径。
- REQ-002：任务必须保存类型、Adapter、工作区、状态、退出码、错误和时间戳。
- REQ-003：事件必须获得数据库级单调 sequence，并保存 envelope 与 JSON payload。
- REQ-004：SSE 必须按 task 和 cursor 从持久事件日志 replay，再无缝切到 live 订阅。
- REQ-005：服务重启后，非终态任务必须转为 `attention_required`，并追加可审计的状态与注意事件。
- REQ-006：schema 初始化和版本记录必须自动完成。
- REQ-007：默认启用 WAL、foreign keys、busy timeout 和完整同步写入。

## 安全与不变量

- INV-001：数据库只保存显式的领域数据与事件，不保存进程环境。
- INV-002：单条超大事件必须在进入持久日志前被截断为摘要。
- INV-003：持久化失败不能把尚未成功记录的任务作为已创建返回。
- INV-004：重启恢复不能把旧任务标记为 completed，也不能自动重启外部 Agent。
- INV-005：HTTP 默认监听仍保持 loopback；SQLite 不改变远程暴露策略。

## 验收标准

- AC-001：任务和事件关闭数据库再打开后保持完整，事件 sequence 继续递增。
- AC-002：按 task ID 查询不会混入其他任务事件，已知 payload 恢复为对应领域类型。
- AC-003：重启恢复测试验证 running/queued/verifying 等非终态变为 `attention_required`。
- AC-004：Go 测试、race、vet 与前后端构建通过。
- AC-005：实际启动 `serve`、创建任务、重启服务后，REST 和 SSE 仍能读到该任务及时间线。
