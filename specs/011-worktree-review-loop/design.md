# SPEC-011 技术设计

## 创建与执行

```text
source checkout（必须 clean）
        │ git rev-parse HEAD
        ▼
<git-common-dir>/foreman-worktrees/<task-id>
        │ git worktree add --detach
        ▼
Agent / verification 在隔离目录执行
```

`internal/worktree` 只负责 Git 隔离与只读 diff。应用层在 queued task 真正启动前创建 worktree，随后把实际 CWD、源 CWD、worktree root 和基线 revision 原子写回任务快照。Agent Adapter、监督循环与验证器继续只接收普通绝对 CWD，无需感知 Git 实现。

源路径位于仓库子目录时，任务在 worktree 中进入相同的相对目录；用于审查的 diff 始终以 worktree root 为范围。worktree 使用 detached HEAD，避免 Foreman 猜测分支命名或修改用户分支引用。

## Diff

`GET /api/v1/tasks/{id}/diff` 调用 `git status --porcelain=v1 -z` 生成文件清单，并用 `git diff <base> -- <safe-paths>` 读取 tracked diff。未跟踪的普通文本文件由 Foreman 生成只读 patch；二进制和过大文件只返回说明。

敏感路径不会传给 `git diff`，其 patch 固定为隐藏标记。最终 patch 使用有界 buffer，防止大仓库变更拖垮控制面。读取操作有 HTTP 级 10 秒超时。

## 审查反馈

Web 弹窗展示文件清单和完整 patch。任务处于 `waiting_input` 且拥有 `continue` 动作时，审查文本被包装为明确的代码审查指令，通过现有 REST action 进入任务 mailbox，并复用同一 Agent session。Agent 修改完成后可刷新 diff，形成：

```text
Agent 修改 → 人工查看 diff → continue(审查反馈) → Agent 修复 → 验证/再次审查
```

## 生命周期与清理

任务删除不删除 worktree。这样即使用户删除了数据库历史，未提交代码仍然保留。前端确认框会显示保留路径。自动提交、应用到源 checkout 和显式 worktree 清理留给后续功能，并且必须独立授权。

## 数据迁移

SQLite schema 升级为 v2。启动时检查 `tasks` 表列并对旧库执行幂等 `ALTER TABLE`，为 `source_cwd`、`worktree_root` 和 `base_revision` 增加空字符串默认值，不重写已有事件。
