# SPEC-006 技术设计

## 选型

Foreman 的数据表面既有 KV 特征（不同 Adapter 的事件 payload），也有明显的关系查询（按任务、状态、时间和 sequence 查询）。因此采用 SQLite 作为单一存储，并把事件载荷保存为 JSON BLOB：固定字段可索引，开放 payload 不绑定具体 schema。

相比 bbolt，这避免了手写二级索引、事务内序列分配、序列化迁移和按任务扫描；相比 Badger/Pebble，单文件 SQLite 不需要 value-log GC、compaction 调优或多文件备份协议，更符合本地单进程控制面的负载。

## 边界

```text
app.Service ── storage.TaskStore ─┐
                                 ├── SQLite (tasks, events, schema_migrations)
event.Bus ─── storage.EventJournal┘
     │
     └── live subscribers
```

`internal/storage` 只定义面向领域对象的端口；`internal/storage/sqlite` 拥有 SQL、迁移和 JSON 编解码。应用层不依赖 SQLite 细节，CLI 的 `run`/`opencode` 仍可使用纯内存 Bus。

## 一致性与 replay

SQLite `events.sequence` 使用 `INTEGER PRIMARY KEY AUTOINCREMENT`。Bus 在持有订阅锁时追加数据库，再将带持久 sequence 的同一事件写入内存窗口并广播。订阅时也在该锁内先查询持久历史、再安装 live channel，因此 replay 与 live 之间不存在空窗。

任务快照在合法状态迁移时先 upsert，再更新内存对象；关键生命周期事件使用可返回错误的持久发布路径。普通 Adapter chunk 仍通过原有 Publisher 接口进入同一持久 Bus。

## 重启语义

启动时加载所有任务快照。终态任务原样恢复；非终态任务无法安全恢复其 OS process 或 ACP session，因此转为 `attention_required`，原因固定为 Foreman restart，并追加 `task.state` 与 `task.attention_required` 事件。用户可据此检查工作区后创建新任务。

## 数据库配置

- `journal_mode=WAL`：读 replay 时不阻塞正常追加。
- `synchronous=FULL`：优先保证监督审计历史的落盘完整性。
- `busy_timeout=5000`：避免短暂锁竞争直接失败。
- `foreign_keys=ON`：为后续 schema 扩展保留一致性基线。
- 单连接池：当前是单进程、低写入并发控制面，简化 connection-local PRAGMA 和写锁行为。

## 回滚

删除或移动 `data/foreman.db` 即可用空库启动；数据库路径在 Git ignore 内。`run` 和 `opencode` 子命令不依赖 SQLite，可独立用于诊断。
