# SPEC-011：隔离 Worktree 与代码审查反馈闭环

- 状态：ACCEPTED
- 负责人：Cyber Foreman contributors
- 创建日期：2026-09-30
- 最后更新：2026-09-30

## 背景

Agent 直接修改用户当前 checkout 会与人工工作相互干扰，也让“监工审查后再要求修改”的边界不清晰。Foreman 需要为单个任务提供可选的 Git worktree 隔离、稳定的基线 diff，以及把人工审查意见送回同一 Agent session 的闭环。

## 目标

- 任务可选择在独立、detached Git worktree 中运行。
- 原 checkout 保持不变，任务记录源目录、隔离目录和基线 revision。
- REST API 和 Web 控制台可以读取任务完整 diff。
- 人工审查意见复用既有 `continue` 动作送回原 session。
- diff 对敏感文件内容做确定性隐藏，并限制最大响应大小。

## 非目标

- 不自动提交、合并、cherry-pick 或删除 worktree。
- 不在本阶段生成 GitHub/GitLab Pull Request。
- 不对 diff 进行大模型审查或自动批准。
- 不允许从脏源 checkout 创建隔离任务。

## 功能要求

- REQ-001：`POST /api/v1/tasks` 必须接受 `workspace_mode=worktree`，默认仍为共享工作区。
- REQ-002：隔离模式必须从源仓库当前 `HEAD` 创建 detached worktree，并保留源目录的相对子目录。
- REQ-003：源仓库存在 tracked 或 untracked 变更时必须拒绝创建，避免基线歧义。
- REQ-004：任务必须持久化 `source_workspace`、`worktree_root` 和 `base_revision`。
- REQ-005：`GET /api/v1/tasks/{id}/diff` 必须返回文件状态、统一文本 patch、截断标记和敏感文件隐藏标记。
- REQ-006：`.api_key`、`.env*`、SSH 私钥、`.pem` 和 `.key` 文件内容不得进入 diff 响应。
- REQ-007：Web 控制台必须允许查看 diff，并在任务等待输入时把审查意见发送给同一 session。
- REQ-008：删除任务只删除 Foreman 元数据和事件；worktree 必须保留，防止未提交成果丢失。

## 安全不变量

- INV-001：Foreman 不自动 reset、checkout、clean、merge 或删除用户 Git 数据。
- INV-002：工作目录必须由 Git 自身解析并创建，任务 ID 不得包含路径分隔符。
- INV-003：diff 最大为 1 MiB，单个未跟踪文件最大展示 128 KiB。
- INV-004：敏感文件可以出现在文件清单中，但其内容必须被替换为固定隐藏提示。
- INV-005：审查反馈只能走现有的任务动作和 Agent Adapter，不能绕开生命周期状态机。

## 验收标准

- AC-001：隔离任务修改文件后，源 checkout 内容不变。
- AC-002：tracked、untracked 和敏感文件 diff 测试通过。
- AC-003：旧 SQLite schema 自动增加三个 worktree 元数据列并可正常读取。
- AC-004：API 校验 workspace mode，并为共享任务返回 `diff_unavailable`。
- AC-005：前端类型检查与生产构建通过。
- AC-006：`./scripts/test`、`./scripts/go vet ./...` 和 `./scripts/go test -race ./...` 通过。

## 验证矩阵

| 要求 | 验收项 | 测试/命令 |
|---|---|---|
| REQ-001~006 | AC-001~004 | `./scripts/go test ./internal/worktree ./internal/storage/sqlite ./internal/app ./internal/api` |
| REQ-007 | AC-005 | `cd web && pnpm check && pnpm build` |
| 全部 | AC-006 | `./scripts/test`、`./scripts/go vet ./...`、`./scripts/go test -race ./...` |
