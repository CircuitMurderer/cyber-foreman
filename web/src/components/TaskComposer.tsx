import {useEffect, useMemo, useState, type FormEvent} from "react";
import {Button, Card} from "@heroui/react";
import {Bot, CheckCircle2, Command, FolderGit2, Plus, ShieldCheck, TimerReset} from "lucide-react";
import {
  createTask,
  errorMessage,
  type AdapterDescriptor,
  type CreateTaskRequest,
  type Task,
  type TaskKind
} from "../api";

interface TaskComposerProps {
  adapters: AdapterDescriptor[];
  onCreated: (task: Task) => void;
}

export function TaskComposer({adapters, onCreated}: TaskComposerProps) {
  const [adapter, setAdapter] = useState("");
  const [kind, setKind] = useState<TaskKind>("agent");
  const [workspace, setWorkspace] = useState("");
  const [prompt, setPrompt] = useState("");
  const [command, setCommand] = useState("");
  const [model, setModel] = useState("");
  const [idleTimeout, setIdleTimeout] = useState("90s");
  const [hardTimeout, setHardTimeout] = useState("30m");
  const [maxNudges, setMaxNudges] = useState(2);
  const [maxRetries, setMaxRetries] = useState(2);
  const [maxTestRepairs, setMaxTestRepairs] = useState(2);
  const [verifyWorkspace, setVerifyWorkspace] = useState(true);
  const [runTests, setRunTests] = useState(false);
  const [testCommand, setTestCommand] = useState("./scripts/test");
  const [testTimeout, setTestTimeout] = useState("10m");
  const [advanced, setAdvanced] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  const selectedAdapter = useMemo(
    () => adapters.find((item) => item.name === adapter),
    [adapter, adapters]
  );

  const availableAdapters = useMemo(
    () => adapters.filter((item) => item.installed && item.healthy),
    [adapters]
  );

  useEffect(() => {
    if (availableAdapters.length === 0) {
      setAdapter("");
      return;
    }
    if (availableAdapters.some((item) => item.name === adapter)) return;
    const preferred = availableAdapters.find((item) => item.name === "opencode") ?? availableAdapters[0];
    setAdapter(preferred.name);
    setKind(preferred.capabilities.prompt ? "agent" : "command");
  }, [adapter, availableAdapters]);

  useEffect(() => {
    if (!selectedAdapter) return;
    if (kind === "agent" && !selectedAdapter.capabilities.prompt) setKind("command");
    if (kind === "command" && !selectedAdapter.capabilities.command) setKind("agent");
  }, [kind, selectedAdapter]);

  useEffect(() => {
    setVerifyWorkspace(kind === "agent");
  }, [kind]);

  useEffect(() => {
    setModel("");
  }, [adapter]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    try {
      const input = kind === "agent" ? {prompt: prompt.trim()} : {command: splitCommand(command)};
      if (kind === "agent" && !prompt.trim()) throw new Error("请填写要交给 Agent 的任务");
      const verificationCommands = runTests ? [{argv: splitCommand(testCommand), timeout: testTimeout}] : undefined;
      const payload: CreateTaskRequest = {
        kind,
        adapter,
        ...(workspace.trim() ? {workspace: workspace.trim()} : {}),
        input,
        ...(kind === "agent"
          ? {
              ...(model.trim() ? {model: model.trim()} : {}),
              supervision: {
                idle_timeout: idleTimeout,
                hard_timeout: hardTimeout,
                max_nudges: maxNudges,
                max_retries: maxRetries,
                max_test_repairs: maxTestRepairs
              }
            }
          : {}),
        verification: {
          workspace: verifyWorkspace,
          ...(verificationCommands ? {commands: verificationCommands} : {})
        }
      };
      setSubmitting(true);
      const task = await createTask(payload);
      onCreated(task);
      if (kind === "agent") setPrompt("");
      else setCommand("");
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Card className="composer-card" variant="secondary">
      <Card.Header className="composer-header">
        <div>
          <div className="eyebrow">任务配置</div>
          <Card.Title>派发新任务</Card.Title>
          <Card.Description>监工会持续观察、纠偏并执行完成门禁。</Card.Description>
        </div>
        <div className="composer-mark"><Plus size={20} /></div>
      </Card.Header>
      <Card.Content>
        <form className="composer-form" onSubmit={submit}>
          <div className="form-grid two-columns">
            <Field label="执行器" icon={<Bot size={15} />}>
              <select
                className="control"
                value={adapter}
                onChange={(event) => setAdapter(event.target.value)}
                disabled={submitting}
              >
                {adapters.map((item) => (
                  <option key={item.name} value={item.name} disabled={!item.installed || !item.healthy}>
                    {adapterLabel(item)}
                  </option>
                ))}
              </select>
              {selectedAdapter && (
                <span className={`adapter-health ${selectedAdapter.healthy ? "healthy" : "unhealthy"}`}>
                  {selectedAdapter.healthy
                    ? selectedAdapter.capabilities.prompt
                      ? `ACP v${selectedAdapter.protocol_version ?? "?"}${selectedAdapter.version ? ` · ${selectedAdapter.version}` : ""}`
                      : "本地命令执行器"
                    : selectedAdapter.error || "Agent 不可用"}
                </span>
              )}
              {adapters.some((item) => !item.installed || !item.healthy) && (
                <div className="unavailable-adapters">
                  {adapters.filter((item) => !item.installed || !item.healthy).map((item) => (
                    <span key={item.name}><strong>{item.name}</strong>：{item.error || "ACP 健康检查失败"}</span>
                  ))}
                </div>
              )}
            </Field>
            <Field label="工作区" icon={<FolderGit2 size={15} />} hint="留空使用后端当前目录">
              <input
                className="control"
                value={workspace}
                onChange={(event) => setWorkspace(event.target.value)}
                placeholder="/absolute/path/to/project"
                disabled={submitting}
              />
            </Field>
          </div>

          {selectedAdapter?.capabilities.prompt && selectedAdapter.capabilities.command && (
            <div className="kind-switch" role="group" aria-label="任务类型">
              <button type="button" className={kind === "agent" ? "active" : ""} onClick={() => setKind("agent")}>
                <Bot size={15} /> Agent
              </button>
              <button type="button" className={kind === "command" ? "active" : ""} onClick={() => setKind("command")}>
                <Command size={15} /> Command
              </button>
            </div>
          )}

          {kind === "agent" ? (
            <>
              <Field label="任务说明" icon={<Command size={15} />}>
                <textarea
                  className="control textarea prompt-input"
                  value={prompt}
                  onChange={(event) => setPrompt(event.target.value)}
                  placeholder="描述目标、约束和验收标准……"
                  rows={5}
                  disabled={submitting}
                />
              </Field>
              <Field label="模型">
                <input className="control" value={model} onChange={(event) => setModel(event.target.value)} placeholder="留空使用 Agent 默认模型" disabled={submitting} />
              </Field>
            </>
          ) : (
            <Field label="命令" icon={<Command size={15} />} hint="直接 argv 执行，支持单双引号，不经过 shell">
              <input
                className="control mono"
                value={command}
                onChange={(event) => setCommand(event.target.value)}
                placeholder="go test ./..."
                disabled={submitting}
              />
            </Field>
          )}

          <div className="policy-row">
            <label className="check-control">
              <input type="checkbox" checked={verifyWorkspace} onChange={(event) => setVerifyWorkspace(event.target.checked)} />
              <span><ShieldCheck size={16} /> 验证工作区</span>
            </label>
            <label className="check-control">
              <input type="checkbox" checked={runTests} onChange={(event) => setRunTests(event.target.checked)} />
              <span><CheckCircle2 size={16} /> 运行验证命令</span>
            </label>
            <button className="advanced-toggle" type="button" onClick={() => setAdvanced((value) => !value)}>
              <TimerReset size={15} /> {advanced ? "收起策略" : "监督策略"}
            </button>
          </div>

          {runTests && (
            <div className="form-grid test-grid">
              <Field label="验证命令"><input className="control mono" value={testCommand} onChange={(event) => setTestCommand(event.target.value)} /></Field>
              <Field label="超时"><input className="control mono" value={testTimeout} onChange={(event) => setTestTimeout(event.target.value)} /></Field>
            </div>
          )}

          {advanced && kind === "agent" && (
            <div className="advanced-panel">
              <Field label="空转超时"><input className="control mono" value={idleTimeout} onChange={(event) => setIdleTimeout(event.target.value)} /></Field>
              <Field label="硬超时"><input className="control mono" value={hardTimeout} onChange={(event) => setHardTimeout(event.target.value)} /></Field>
              <Field label="最大提醒"><input className="control" type="number" min={0} value={maxNudges} onChange={(event) => setMaxNudges(Number(event.target.value))} /></Field>
              <Field label="最大恢复"><input className="control" type="number" min={0} value={maxRetries} onChange={(event) => setMaxRetries(Number(event.target.value))} /></Field>
              <Field label="最大测试修复"><input className="control" type="number" min={0} value={maxTestRepairs} onChange={(event) => setMaxTestRepairs(Number(event.target.value))} /></Field>
            </div>
          )}

          {error && <div className="inline-error" role="alert">{error}</div>}
          <Button className="dispatch-button" variant="primary" type="submit" fullWidth isDisabled={submitting || !adapter || !selectedAdapter?.healthy}>
            {submitting ? "正在派发…" : <><Plus size={17} /> 派发任务</>}
          </Button>
        </form>
      </Card.Content>
    </Card>
  );
}

function adapterLabel(adapter: AdapterDescriptor): string {
  const title = adapter.agent_info?.title || adapter.agent_info?.name || adapter.name;
  if (!adapter.installed) return `${title}（未安装）`;
  if (!adapter.healthy) return `${title}（不可用）`;
  return adapter.version ? `${title} · ${adapter.version}` : title;
}

function Field({label, icon, hint, children}: {label: string; icon?: React.ReactNode; hint?: string; children: React.ReactNode}) {
  return (
    <label className="field">
      <span className="field-label">{icon}{label}{hint && <small>{hint}</small>}</span>
      {children}
    </label>
  );
}

function splitCommand(value: string): string[] {
  const result: string[] = [];
  let current = "";
  let quote = "";
  let escaped = false;
  for (const character of value.trim()) {
    if (escaped) {
      current += character;
      escaped = false;
      continue;
    }
    if (character === "\\" && quote !== "'") {
      escaped = true;
      continue;
    }
    if (quote) {
      if (character === quote) quote = "";
      else current += character;
      continue;
    }
    if (character === "'" || character === '"') {
      quote = character;
    } else if (/\s/.test(character)) {
      if (current) {
        result.push(current);
        current = "";
      }
    } else {
      current += character;
    }
  }
  if (escaped) current += "\\";
  if (quote) throw new Error("命令中存在未闭合的引号");
  if (current) result.push(current);
  if (result.length === 0) throw new Error("请填写命令");
  return result;
}
