# 赛博监工离线部署指南

本文面向无法访问公网的 Linux 内网机器。交付包已经包含 Linux x86_64、AArch64 可执行文件和构建完成的 Web 静态资源；目标机不需要安装 Go、Node.js、npm、pnpm 或 SQLite。

## 1. 选择架构

在目标机执行：

```bash
uname -m
```

| `uname -m` | 使用文件 |
|---|---|
| `x86_64` | `dist/linux-amd64/foreman` |
| `aarch64`、`arm64` | `dist/linux-arm64/foreman` |

二进制使用 `CGO_ENABLED=0` 静态构建，并移除了调试符号。SQLite 驱动已经编译进程序。`dist/web/` 是浏览器直接加载的 HTML/CSS/JavaScript，不需要 Node 运行时；目标机即使只有 Node 22.14 且没有 npm，也不影响运行。

建议先验证校验和：

```bash
cd dist
sha256sum -c SHA256SUMS
```

## 2. 目录准备

以下示例将程序部署到 `/opt/cyber-foreman`，代码工作区放在 `/srv/repos`：

```bash
sudo mkdir -p /opt/cyber-foreman/{bin,web,config,data}
sudo mkdir -p /srv/repos
sudo cp dist/linux-amd64/foreman /opt/cyber-foreman/bin/foreman  # AArch64 请替换目录
sudo cp -R dist/web/. /opt/cyber-foreman/web/
sudo cp source/config/agents.internal.example.json /opt/cyber-foreman/config/agents.json
sudo chmod 0755 /opt/cyber-foreman/bin/foreman
```

编辑 `/opt/cyber-foreman/config/agents.json`，至少修改：

- OpenCode、Grok Build 的真实绝对路径；
- `default_workspace` 和 `security.workspace_roots`；
- 内网 Qwen 的 `base_url`、模型名和上下文限制；
- 根据实际仓库调整 `command_allowlist`。

Agent 列表完全由这个 JSON 决定。只部署 OpenCode 时删除 `grok` 条目；只部署 Grok Build 时删除 `opencode` 条目。不存在于配置中的 Agent 不会出现在前端。

## 3. 配置内网 Qwen

### OpenCode

Foreman 的 Agent Profile 会声明 Provider 元数据，但 OpenCode 自身仍必须知道模型端点。把 `source/config/opencode-internal.example.json` 复制到 OpenCode 实际读取的位置，或通过环境变量明确指定：

```bash
export OPENCODE_CONFIG=/opt/cyber-foreman/config/opencode.json
```

示例复用了 OpenCode 内置的 `openai` Provider，因此模型 ID 是 `openai/qwen3.8`，不需要在内网机器下载额外 npm Provider 包。请按公司实际模型名称、上下文长度和输出上限修改示例。

部署前独立验证：

```bash
opencode run -m openai/qwen3.8 "只回复 OK"
opencode acp
```

第二条命令进入 ACP stdio 后可直接按 `Ctrl+C` 退出；能正常启动且不输出普通交互 UI 即可。

### Grok Build

把 `source/config/grok-internal.example.toml` 复制到 Grok Build 使用的配置目录，并按安装版本设置 `GROK_HOME`：

```bash
export GROK_HOME=/opt/cyber-foreman/grok-home
mkdir -p "$GROK_HOME"
cp source/config/grok-internal.example.toml "$GROK_HOME/config.toml"
grok version
grok --no-auto-update agent stdio
```

不同 Grok Build 版本的配置目录可能不同；以公司内已安装版本的文档为准。Foreman 使用结构化 ACP stdio，不抓取终端文本。

### API Key

如果内网模型需要鉴权：

```bash
export FOREMAN_AGENT_API_KEY_OPENAI='replace-with-internal-key'
```

如果端点不鉴权，可以在三个配置文件中移除 `api_key_env`、`apiKey` 或 `env_key`。密钥只通过环境变量传入，不要写入 JSON、TOML、启动参数或 systemd unit 正文。

`supervisor.semantic_review` 也使用同一个 OpenAI-compatible Qwen。默认关闭工具调用和运行中抽检，以兼容能力不完整的内网模型。确认 Qwen 支持 OpenAI function calling 后，可以逐步打开 `tool_calling`；确认效果后再打开 `mid_turn.enabled`。确定性 Git/测试门禁始终优先于模型判断。

## 4. 启动

生成一个足够长的控制台 Token：

```bash
export FOREMAN_API_TOKEN='replace-with-a-long-random-token'
export FOREMAN_AGENT_API_KEY_OPENAI='replace-with-internal-key'
export OPENCODE_CONFIG=/opt/cyber-foreman/config/opencode.json
export GROK_HOME=/opt/cyber-foreman/grok-home

cd /opt/cyber-foreman
./bin/foreman serve \
  --addr 0.0.0.0:8090 \
  --agents-file ./config/agents.json \
  --web-dir ./web \
  --db ./data/foreman.db
```

浏览器访问 `http://服务器地址:8090/`，输入 `FOREMAN_API_TOKEN` 登录。绑定到非回环地址时，Foreman 强制要求配置 API Token。生产环境建议再通过公司反向代理提供 HTTPS，并限制来源网段。

启动日志中的某个 Agent `unavailable` 不会阻止其他 Agent 使用。先根据日志确认是可执行文件路径、ACP 握手、模型配置还是鉴权问题。

## 5. systemd 示例

密钥放在权限为 `0600` 的 `/etc/cyber-foreman.env`：

```ini
FOREMAN_API_TOKEN=replace-with-a-long-random-token
FOREMAN_AGENT_API_KEY_OPENAI=replace-with-internal-key
OPENCODE_CONFIG=/opt/cyber-foreman/config/opencode.json
GROK_HOME=/opt/cyber-foreman/grok-home
```

`/etc/systemd/system/cyber-foreman.service`：

```ini
[Unit]
Description=Cyber Foreman
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/cyber-foreman
EnvironmentFile=/etc/cyber-foreman.env
ExecStart=/opt/cyber-foreman/bin/foreman serve --addr 0.0.0.0:8090 --agents-file /opt/cyber-foreman/config/agents.json --web-dir /opt/cyber-foreman/web --db /opt/cyber-foreman/data/foreman.db
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

启用：

```bash
sudo chmod 0600 /etc/cyber-foreman.env
sudo systemctl daemon-reload
sudo systemctl enable --now cyber-foreman
sudo systemctl status cyber-foreman
```

服务账户必须能读写允许的代码工作区和 `/opt/cyber-foreman/data`，也必须能执行 OpenCode/Grok Build。若使用隔离 worktree，还需要 Git 对仓库具有写权限。

## 6. 离线运行与故障检查

- `curl http://127.0.0.1:8090/healthz` 应返回 `{"status":"ok","api_version":"v1"}`。
- Web 页面空白时检查 `--web-dir` 是否指向含有 `index.html` 的目录。
- Agent 不出现在下拉框时检查其是否存在于 `agents.json`。
- Agent 显示不可用时，使用同一服务账户独立执行对应的版本命令和 ACP 启动命令。
- 模型请求失败时，从服务账户环境检查 endpoint、key 和模型 ID；不要把 key 打印到日志。
- SQLite 数据位于 `--db` 指定文件。停服后备份该文件即可保留任务与审计时间线。
- 内网 DNS、代理和自签名 CA 由运行 Foreman 的服务账户环境决定。HTTPS 内部端点需要把公司 CA 安装到系统信任库。

## 7. 升级与回滚

1. 备份 `data/foreman.db` 和当前配置。
2. 停止服务。
3. 替换对应架构的 `foreman` 以及整个 `web/` 目录。
4. 启动并检查 `/healthz`、Agent 健康状态和一个小型测试任务。
5. 若需回滚，同时恢复上一版二进制和 Web 目录；数据库不要在服务运行时直接复制。

前后端来自同一次交付，升级时应成套替换，避免 REST DTO 与页面版本不一致。
