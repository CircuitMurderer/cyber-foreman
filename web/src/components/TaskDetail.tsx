import {useMemo, useState} from "react";
import {Button, Card, Chip} from "@heroui/react";
import {
  Activity,
  AlertTriangle,
  Bot,
  CheckCircle2,
  CircleStop,
  Clock3,
  GitBranch,
  MessageSquarePlus,
  Radio,
  ScrollText,
  ShieldAlert,
  TerminalSquare
} from "lucide-react";
import {
  cancelTask,
  errorMessage,
  getTask,
  interruptTask,
  terminalStatuses,
  type ForemanEvent,
  type Task
} from "../api";
import {absoluteTime, shortID, statusLabel, statusTone} from "../status";
import {useTaskEvents, type StreamState} from "../useTaskEvents";

interface TaskDetailProps {
  task?: Task;
  onChanged: (task: Task) => void;
}

export function TaskDetail({task, onChanged}: TaskDetailProps) {
  const {events, streamState} = useTaskEvents(task?.id);
  const [message, setMessage] = useState("");
  const [acting, setActing] = useState(false);
  const [actionError, setActionError] = useState("");

  const metrics = useMemo(() => summarize(events), [events]);

  if (!task) {
    return (
      <section className="detail-empty">
        <div className="detail-empty-glyph"><Activity size={34} /></div>
        <div className="eyebrow">任务详情</div>
        <h2>选择一个任务</h2>
        <p>这里会显示 Agent 的实时活动、监工决策和完成门禁。</p>
      </section>
    );
  }

  async function refresh() {
    if (!task) return;
    onChanged(await getTask(task.id));
  }

  async function interrupt() {
    if (!task || !message.trim()) return;
    setActing(true);
    setActionError("");
    try {
      await interruptTask(task.id, message.trim());
      setMessage("");
      await refresh();
    } catch (error) {
      setActionError(errorMessage(error));
    } finally {
      setActing(false);
    }
  }

  async function cancel() {
    if (!task || !window.confirm("确定停止这个任务？当前 Agent turn 会被取消。")) return;
    setActing(true);
    setActionError("");
    try {
      await cancelTask(task.id);
      await refresh();
    } catch (error) {
      setActionError(errorMessage(error));
    } finally {
      setActing(false);
    }
  }

  const canInterrupt = task.kind === "agent" && task.status === "running";
  const canCancel = !terminalStatuses.has(task.status);

  return (
    <section className="task-detail">
      <div className="detail-titlebar">
        <div>
          <div className="detail-id"><span>{task.kind === "agent" ? "AGENT" : "COMMAND"}</span> / {shortID(task.id)}</div>
          <h2>{task.adapter}</h2>
          <p title={task.workspace}>{task.workspace || "默认工作目录"}</p>
        </div>
        <div className="detail-status">
          <Chip color={statusTone[task.status]} variant="soft">{statusLabel[task.status]}</Chip>
          <StreamIndicator state={streamState} />
        </div>
      </div>

      {task.error && (
        <div className="task-alert" role="alert">
          <ShieldAlert size={18} />
          <div><strong>任务需要注意</strong><span>{task.error}</span></div>
        </div>
      )}

      <div className="metric-grid">
        <Metric icon={<ScrollText size={17} />} value={events.length} label="事件" />
        <Metric icon={<Bot size={17} />} value={metrics.decisions} label="监工决策" />
        <Metric icon={<TerminalSquare size={17} />} value={metrics.toolEvents} label="工具活动" />
        <Metric icon={<CheckCircle2 size={17} />} value={metrics.verifications} label="验证结果" />
      </div>

      {(canInterrupt || canCancel) && (
        <Card className="action-card" variant="secondary">
          <Card.Header>
            <div>
              <Card.Title>人工干预</Card.Title>
              <Card.Description>动作会进入任务 mailbox，并保留在监督事件中。</Card.Description>
            </div>
          </Card.Header>
          <Card.Content className="action-content">
            {canInterrupt && (
              <div className="interrupt-row">
                <textarea
                  className="control textarea"
                  rows={2}
                  value={message}
                  onChange={(event) => setMessage(event.target.value)}
                  placeholder="补充上下文，或要求 Agent 调整方向……"
                  disabled={acting}
                />
                <Button variant="secondary" onPress={interrupt} isDisabled={acting || !message.trim()}>
                  <MessageSquarePlus size={16} /> 打断并追加
                </Button>
              </div>
            )}
            {canCancel && (
              <Button variant="danger-soft" size="sm" onPress={cancel} isDisabled={acting}>
                <CircleStop size={16} /> 停止任务
              </Button>
            )}
            {actionError && <div className="inline-error">{actionError}</div>}
          </Card.Content>
        </Card>
      )}

      <div className="timeline-heading">
        <div><Radio size={16} /><strong>实时活动</strong></div>
        <span>{absoluteTime(task.created_at)} 创建</span>
      </div>
      <div className="timeline">
        {events.length === 0 ? (
          <div className="timeline-empty"><Clock3 size={22} /><span>等待第一个事件…</span></div>
        ) : [...events].reverse().map((event) => <EventItem key={event.id || `${event.type}-${event.sequence}`} event={event} />)}
      </div>
    </section>
  );
}

function Metric({icon, value, label}: {icon: React.ReactNode; value: number; label: string}) {
  return <div className="metric"><span>{icon}</span><strong>{value}</strong><small>{label}</small></div>;
}

function StreamIndicator({state}: {state: StreamState}) {
  const label = state === "live" ? "实时" : state === "reconnecting" ? "重连中" : state === "connecting" ? "连接中" : "离线";
  return <span className={`stream-state ${state}`}><i />{label}</span>;
}

function EventItem({event}: {event: ForemanEvent}) {
  const summary = eventSummary(event);
  const icon = eventIcon(event.type);
  const tone = eventTone(event.type, event.data);
  return (
    <article className={`event-item ${tone}`}>
      <div className="event-rail"><span>{icon}</span></div>
      <div className="event-body">
        <div className="event-head">
          <strong>{eventLabel(event.type)}</strong>
          <time>{event.occurred_at ? absoluteTime(event.occurred_at) : "刚刚"}</time>
        </div>
        {summary && <p>{summary}</p>}
        {event.data !== undefined && (
          <details>
            <summary>查看数据</summary>
            <pre>{JSON.stringify(event.data, null, 2)}</pre>
          </details>
        )}
      </div>
    </article>
  );
}

function summarize(events: ForemanEvent[]) {
  return {
    decisions: events.filter((event) => event.type === "supervisor.decision").length,
    toolEvents: events.filter((event) => event.type === "agent.session_update" && sessionUpdate(event) === "tool_call_update").length,
    verifications: events.filter((event) => event.type === "verification.finished").length
  };
}

function sessionUpdate(event: ForemanEvent): string {
  const data = asRecord(event.data);
  return String(asRecord(data?.update)?.sessionUpdate ?? "");
}

function eventSummary(event: ForemanEvent): string {
  const data = asRecord(event.data);
  if (!data) return "";
  if (event.type === "task.state") return `${String(data.from ?? "")} → ${String(data.to ?? "")}${data.reason ? ` · ${String(data.reason)}` : ""}`;
  if (event.type === "supervisor.decision") return `${String(data.action ?? "decision")} · ${String(data.reason ?? "")}`;
  if (event.type === "supervisor.action_finished") return data.success ? "动作执行成功" : `动作失败：${String(data.error ?? "unknown")}`;
  if (event.type === "verification.started") return `开始 ${String(data.verifier ?? "")} 验证`;
  if (event.type === "verification.finished") return `${String(data.verifier ?? "")} · ${data.passed ? "通过" : "未通过"}`;
  if (event.type === "agent.stderr") return String(data.line ?? "");
  if (event.type === "agent.output") return String(data.line ?? "");
  if (event.type === "agent.follow_up_started") return `上一轮：${String(data.stop_reason ?? "cancelled")}`;
  if (event.type === "stream.gap") return String(data.message ?? "事件历史存在缺口，请刷新任务快照");
  if (event.type === "agent.session_update") {
    const update = asRecord(data.update);
    const kind = String(update?.sessionUpdate ?? "session update");
    if (kind === "agent_message_chunk") return String(asRecord(update?.content)?.text ?? "Agent 正在输出");
    if (kind === "tool_call" || kind === "tool_call_update") return `${String(update?.title ?? "工具调用")} · ${String(update?.status ?? kind)}`;
    if (kind === "usage_update") return "模型用量已更新";
    return kind.replaceAll("_", " ");
  }
  if (typeof data.reason === "string") return data.reason;
  return "";
}

function eventLabel(type: string): string {
  const labels: Record<string, string> = {
    "task.created": "任务已创建",
    "task.state": "状态迁移",
    "task.attention_required": "需要人工处理",
    "agent.started": "Agent 已连接",
    "agent.output": "进程输出",
    "agent.error": "Agent 错误",
    "agent.exited": "Agent 已退出",
    "agent.session_update": "Agent 活动",
    "agent.stderr": "Agent stderr",
    "agent.disconnected": "Agent 断联",
    "agent.permission_requested": "权限请求",
    "agent.interrupt_requested": "已请求中断",
    "agent.follow_up_started": "已追加指令",
    "supervisor.decision": "监工决策",
    "supervisor.action_started": "开始执行动作",
    "supervisor.action_finished": "动作执行完成",
    "verification.started": "开始验证",
    "verification.finished": "验证完成",
    "stream.gap": "事件缺口"
  };
  return labels[type] ?? type;
}

function eventIcon(type: string) {
  if (type.startsWith("supervisor.")) return <Bot size={15} />;
  if (type.startsWith("verification.")) return <CheckCircle2 size={15} />;
  if (type.includes("error") || type.includes("attention") || type.includes("disconnected")) return <AlertTriangle size={15} />;
  if (type === "task.state") return <GitBranch size={15} />;
  if (type.startsWith("agent.")) return <TerminalSquare size={15} />;
  return <Activity size={15} />;
}

function eventTone(type: string, data: unknown): string {
  const record = asRecord(data);
  if (type.includes("error") || type.includes("attention") || type.includes("disconnected") || record?.passed === false) return "danger";
  if (type === "verification.finished" || type === "task.state" && record?.to === "completed") return "success";
  if (type.startsWith("supervisor.")) return "accent";
  return "default";
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}
