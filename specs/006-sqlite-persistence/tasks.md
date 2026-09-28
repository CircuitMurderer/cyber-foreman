# SPEC-006 实施任务

- [x] 定义 TaskStore 与 EventJournal 持久化边界。
- [x] 增加纯 Go SQLite 驱动、schema 初始化和连接配置。
- [x] 持久化任务快照并在启动时恢复。
- [x] 持久化事件 envelope/JSON payload 和全局 sequence。
- [x] 让按任务 SSE 从持久 cursor replay 后切换 live。
- [x] 将重启时的非终态任务转为 `attention_required` 并记录审计事件。
- [x] 增加 round-trip、reopen、typed payload 和 restart recovery 测试。
- [ ] 增加保留周期、导出和数据库维护命令。
