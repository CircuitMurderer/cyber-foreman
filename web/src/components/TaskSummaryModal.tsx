import {useMemo, useState} from "react";
import {Button, Chip, Modal} from "@heroui/react";
import {Check, ClipboardCheck, Copy, FileText, ShieldCheck, Wrench} from "lucide-react";
import type {ForemanEvent, Task} from "../api";
import {asRecord, buildConversation} from "../conversation";
import {statusLabel, statusTone} from "../status";

interface TaskSummaryModalProps {
  task: Task;
  events: ForemanEvent[];
}

export function TaskSummaryModal({task, events}: TaskSummaryModalProps) {
  const summary = useMemo(() => buildSummary(events), [events]);
  const [copied, setCopied] = useState(false);

  async function copyLatestResponse() {
    if (!summary.latestResponse) return;
    await navigator.clipboard.writeText(summary.latestResponse);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1600);
  }

  return (
    <Modal>
      <Button variant="secondary" size="sm">
        <FileText size={15} /> 任务总结
      </Button>
      <Modal.Backdrop variant="blur">
        <Modal.Container size="lg" scroll="inside" placement="center">
          <Modal.Dialog className="summary-dialog">
            <Modal.CloseTrigger />
            <Modal.Header className="conversation-header">
              <div>
                <Modal.Heading>任务总结</Modal.Heading>
                <p>基于完整持久事件记录自动整理，不依赖中间 chunk 展示。</p>
              </div>
            </Modal.Header>
            <Modal.Body className="summary-body">
              <div className="summary-status">
                <div>
                  <span>当前结论</span>
                  <Chip color={statusTone[task.status]} variant="soft">{statusLabel[task.status]}</Chip>
                </div>
                <dl>
                  <div><dt>对话轮次</dt><dd>{summary.turns}</dd></div>
                  <div><dt>工具活动</dt><dd>{summary.toolCalls}</dd></div>
                  <div><dt>监工干预</dt><dd>{summary.decisions}</dd></div>
                  <div><dt>验证</dt><dd>{summary.verifications.length}</dd></div>
                </dl>
              </div>

              {task.error && <div className="summary-error"><ShieldCheck size={17} /><span>{task.error}</span></div>}

              <SummarySection title="原始任务" icon={<ClipboardCheck size={16} />} empty="没有记录到原始 Prompt。">
                {summary.originalRequest}
              </SummarySection>

              <section className="summary-section latest-response">
                <header>
                  <div><FileText size={16} /><strong>最新完整回复</strong></div>
                  <Button
                    variant="ghost"
                    size="sm"
                    isIconOnly
                    aria-label="复制最新完整回复"
                    onPress={() => void copyLatestResponse()}
                    isDisabled={!summary.latestResponse}
                  >
                    {copied ? <Check size={15} /> : <Copy size={15} />}
                  </Button>
                </header>
                <div className={summary.latestResponse ? "summary-text" : "summary-empty"}>
                  {summary.latestResponse || "Agent 还没有产生完整文本回复。"}
                </div>
              </section>

              <section className="summary-section">
                <header><div><ShieldCheck size={16} /><strong>验证结论</strong></div></header>
                {summary.verifications.length === 0 ? (
                  <div className="summary-empty">尚未运行额外验证。</div>
                ) : (
                  <div className="summary-checks">
                    {summary.verifications.map((item, index) => (
                      <div className={item.passed ? "passed" : "failed"} key={`${item.name}-${index}`}>
                        <span>{item.name}</span><strong>{item.passed ? "通过" : "未通过"}</strong>
                      </div>
                    ))}
                  </div>
                )}
              </section>

              {summary.tools.length > 0 && (
                <section className="summary-section">
                  <header><div><Wrench size={16} /><strong>最近工具活动</strong></div></header>
                  <ul className="summary-tools">
                    {summary.tools.map((tool, index) => <li key={`${tool}-${index}`}>{tool}</li>)}
                  </ul>
                </section>
              )}
            </Modal.Body>
          </Modal.Dialog>
        </Modal.Container>
      </Modal.Backdrop>
    </Modal>
  );
}

function SummarySection({title, icon, empty, children}: {title: string; icon: React.ReactNode; empty: string; children?: string}) {
  return (
    <section className="summary-section">
      <header><div>{icon}<strong>{title}</strong></div></header>
      <div className={children ? "summary-text" : "summary-empty"}>{children || empty}</div>
    </section>
  );
}

function buildSummary(events: ForemanEvent[]) {
  const messages = buildConversation(events);
  const operatorMessages = messages.filter((message) => message.role === "operator");
  const responses = messages.filter((message) => message.role === "assistant" && message.text);
  const verifications = events
    .filter((event) => event.type === "verification.finished")
    .map((event) => {
      const data = asRecord(event.data);
      return {name: String(data?.verifier || "验证"), passed: data?.passed === true};
    });
  const toolEvents = events.filter((event) => {
    const update = asRecord(asRecord(event.data)?.update);
    return event.type === "agent.session_update" && (update?.sessionUpdate === "tool_call" || update?.sessionUpdate === "tool_call_update");
  });
  const tools = toolEvents
    .map((event) => {
      const update = asRecord(asRecord(event.data)?.update);
      return String(update?.title || update?.name || "工具调用");
    })
    .filter((value, index, all) => all.indexOf(value) === index)
    .slice(-8);
  return {
    originalRequest: operatorMessages[0]?.text || "",
    latestResponse: responses.at(-1)?.text || "",
    turns: responses.length,
    toolCalls: toolEvents.length,
    decisions: events.filter((event) => event.type === "supervisor.decision").length,
    verifications,
    tools
  };
}
