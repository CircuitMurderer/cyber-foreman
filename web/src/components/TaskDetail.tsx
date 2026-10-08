import {useMemo, useState} from "react";
import {Button, Card, Chip, Modal, useOverlayState} from "@heroui/react";
import {
  Activity,
  AlertTriangle,
  Bot,
  Check,
  CheckCircle2,
  Clock3,
  GitBranch,
  MessageSquarePlus,
  Send,
  Square,
  Radio,
  ScrollText,
  ShieldAlert,
  TerminalSquare
} from "lucide-react";
import {
  cancelTask,
  continueTask,
  errorMessage,
  finishTask,
  getTask,
  interruptTask,
  terminalStatuses,
  type ForemanEvent,
  type Task
} from "../api";
import {absoluteTime, shortID, statusLabel, statusTone} from "../status";
import {useTaskEvents, type StreamState} from "../useTaskEvents";
import {ConversationModal} from "./ConversationModal";
import {TaskSummaryModal} from "./TaskSummaryModal";
import {TaskDiffModal} from "./TaskDiffModal";
import {ConfirmationDialog} from "./ConfirmationDialog";

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

  async function continueConversation() {
    if (!task || !message.trim()) return;
    setActing(true);
    setActionError("");
    try {
      await continueTask(task.id, message.trim());
      setMessage("");
      await refresh();
    } catch (error) {
      setActionError(errorMessage(error));
    } finally {
      setActing(false);
    }
  }

  async function cancel() {
    if (!task) return;
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

  async function finish() {
    if (!task) return;
    setActing(true);
    setActionError("");
    try {
      await finishTask(task.id);
      await refresh();
    } catch (error) {
      setActionError(errorMessage(error));
    } finally {
      setActing(false);
    }
  }

  const actions = new Set(task.available_actions ?? []);
  const canInterrupt = actions.has("interrupt") || task.kind === "agent" && task.status === "running";
  const canContinue = actions.has("continue");
  const canFinish = actions.has("finish");
  const canCancel = actions.has("cancel") || !terminalStatuses.has(task.status);

  return (
    <section className="task-detail">
      <div className="detail-titlebar">
        <div>
          <div className="detail-id"><span>{task.kind === "agent" ? "AGENT" : "COMMAND"}</span> / {shortID(task.id)}</div>
          <h2>{task.adapter}</h2>
          <p title={task.workspace}>{task.workspace || "默认工作目录"}</p>
        </div>
        <div className="detail-side">
          <div className="detail-status">
            <Chip color={statusTone[task.status]} variant="soft">{statusLabel[task.status]}</Chip>
            <StreamIndicator state={streamState} />
          </div>
          <div className="detail-actions">
            {task.worktree_root && <TaskDiffModal task={task} onChanged={onChanged} />}
            <TaskSummaryModal task={task} events={events} />
            {task.kind === "agent" && <ConversationModal task={task} events={events} />}
          </div>
        </div>
      </div>

      {task.error && (
        <div className="task-alert" role="alert">
          <ShieldAlert size={18} />
          <div><strong>任务需要注意</strong><span>{task.error}</span></div>
        </div>
      )}

      <div className="metric-grid">
        <Metric icon={<ScrollText size={17} />} value={events.length} label="事件" kind="events" events={events} />
        <Metric icon={<Bot size={17} />} value={metrics.decisions} label="监工决策" kind="decisions" events={events} />
        <Metric icon={<TerminalSquare size={17} />} value={metrics.toolEvents} label="工具活动" kind="tools" events={events} />
        <Metric icon={<CheckCircle2 size={17} />} value={metrics.verifications} label="验证结果" kind="verifications" events={events} />
      </div>

      {(canInterrupt || canContinue || canFinish || canCancel) && (
        <Card className="action-card" variant="secondary">
          <Card.Header>
            <div>
              <Card.Title>{canContinue ? "继续当前任务" : "人工干预"}</Card.Title>
              <Card.Description>{canContinue ? "复用同一个 Agent session 继续对话，不会取消上一轮。" : "动作会进入任务 mailbox，并保留在监督事件中。"}</Card.Description>
            </div>
          </Card.Header>
          <Card.Content className="action-content">
            <div className="action-dialog">
              {(canInterrupt || canContinue) && (
                <textarea
                  className="control textarea action-textarea"
                  rows={3}
                  value={message}
                  onChange={(event) => setMessage(event.target.value)}
                  placeholder={canContinue ? "继续追问，或给 Agent 新的后续任务……" : "补充上下文，或要求 Agent 调整方向……"}
                  disabled={acting}
                />
              )}
              <div className="action-dock" aria-label="任务操作">
                {canContinue && (
                  <ActionOrb label="继续对话" tone="primary" disabled={acting || !message.trim()} onClick={continueConversation}>
                    <Send size={19} />
                  </ActionOrb>
                )}
                {!canContinue && canInterrupt && (
                  <ActionOrb label="打断并追加" tone="primary" disabled={acting || !message.trim()} onClick={interrupt}>
                    <MessageSquarePlus size={19} />
                  </ActionOrb>
                )}
                {canFinish && (
                  <ConfirmationDialog
                    title="结束当前任务？"
                    description="任务会标记为已完成，并关闭当前 Agent session。"
                    confirmLabel="结束任务"
                    tone="primary"
                    onConfirm={finish}
                    trigger={<ActionOrb label="结束任务" tone="success" disabled={acting}><Check size={19} /></ActionOrb>}
                  />
                )}
                {canCancel && (
                  <ConfirmationDialog
                    title="停止当前任务？"
                    description="正在运行的 Agent turn 会被取消，任务随后进入停止状态。"
                    confirmLabel="停止任务"
                    onConfirm={cancel}
                    trigger={<ActionOrb label="停止任务" tone="danger" disabled={acting}><Square size={17} /></ActionOrb>}
                  />
                )}
              </div>
            </div>
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

function ActionOrb({
  label, tone, disabled, onClick, onPress, children
}: {
  label: string;
  tone: "primary" | "success" | "danger";
  disabled: boolean;
  onClick?: () => void | Promise<void>;
  onPress?: () => void;
  children: React.ReactNode;
}) {
  return (
    <Button
      className={`action-orb ${tone}`}
      variant="secondary"
      isIconOnly
      aria-label={label}
      data-tooltip={label}
      isDisabled={disabled}
      onPress={onPress ?? (onClick ? () => void onClick() : undefined)}
    >
      {children}
    </Button>
  );
}

type MetricKind = "events" | "decisions" | "tools" | "verifications";

function Metric({icon, value, label, kind, events}: {
  icon: React.ReactNode;
  value: number;
  label: string;
  kind: MetricKind;
  events: ForemanEvent[];
}) {
  const details = metricEvents(events, kind);
  const state = useOverlayState();
  return (
    <Modal state={state}>
      <Button className="metric" variant="secondary" aria-label={`查看${label}详情`} onPress={state.open}>
        <span>{icon}</span><strong>{value}</strong><small>{label}</small>
      </Button>
      <Modal.Backdrop variant="blur">
        <Modal.Container size="lg" scroll="inside" placement="center">
          <Modal.Dialog className="metric-dialog">
            <Modal.CloseTrigger />
            <Modal.Header className="conversation-header">
              <div><Modal.Heading>{label}详情</Modal.Heading><p>共 {details.length} 条记录，最新记录优先。</p></div>
            </Modal.Header>
            <Modal.Body className="metric-detail-list">
              {details.length === 0 ? (
                <div className="metric-detail-empty">当前任务还没有{label}记录。</div>
              ) : [...details].reverse().map((event) => (
                <article className="metric-detail-item" key={event.id || `${event.type}-${event.sequence}`}>
                  <header><strong>{eventLabel(event.type)}</strong><time>{event.occurred_at ? absoluteTime(event.occurred_at) : "刚刚"}</time></header>
                  {eventSummary(event) && <p>{eventSummary(event)}</p>}
                  {event.data !== undefined && <pre>{JSON.stringify(event.data, null, 2)}</pre>}
                </article>
              ))}
            </Modal.Body>
          </Modal.Dialog>
        </Modal.Container>
      </Modal.Backdrop>
    </Modal>
  );
}

function metricEvents(events: ForemanEvent[], kind: MetricKind): ForemanEvent[] {
  if (kind === "events") return events;
  if (kind === "decisions") return events.filter((event) => event.type === "supervisor.decision");
  if (kind === "verifications") return events.filter((event) => event.type === "verification.finished");
  return events.filter((event) => event.type === "agent.session_update" && sessionUpdate(event) === "tool_call_update");
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
  if (event.type === "supervisor.semantic_review_started") return `${String(data.provider ?? "openai")} · ${String(data.model ?? "")}`;
  if (event.type === "supervisor.semantic_review_finished") {
    if (data.success === false) return `辅助复核失败，已按确定性结果继续：${String(data.error ?? "unknown")}`;
    return `${String(data.verdict ?? "uncertain")} · ${String(data.reason ?? "")}`;
  }
  if (event.type === "verification.started") return `开始 ${String(data.verifier ?? "")} 验证`;
  if (event.type === "verification.finished") return `${String(data.verifier ?? "")} · ${data.passed ? "通过" : "未通过"}`;
  if (event.type === "agent.stderr") return String(data.line ?? "");
  if (event.type === "agent.output") return String(data.line ?? "");
  if (event.type === "conversation.message") return String(data.text ?? "");
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
    "conversation.message": "已发送指令",
    "supervisor.decision": "监工决策",
    "supervisor.action_started": "开始执行动作",
    "supervisor.action_finished": "动作执行完成",
    "supervisor.semantic_review_started": "开始语义复核",
    "supervisor.semantic_review_finished": "语义复核完成",
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
