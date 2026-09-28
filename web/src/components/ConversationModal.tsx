import {useMemo, useState} from "react";
import {Button, Modal} from "@heroui/react";
import {Bot, Check, Copy, MessageSquareText, ShieldCheck, UserRound} from "lucide-react";
import type {ForemanEvent, Task} from "../api";
import {absoluteTime} from "../status";

interface ConversationModalProps {
  task: Task;
  events: ForemanEvent[];
}

type ConversationRole = "operator" | "supervisor" | "assistant";

interface ConversationMessage {
  id: string;
  role: ConversationRole;
  text: string;
  occurredAt: string;
  responseIndex?: number;
}

export function ConversationModal({task, events}: ConversationModalProps) {
  const [view, setView] = useState<"conversation" | "responses">("conversation");
  const [copied, setCopied] = useState("");
  const messages = useMemo(() => buildConversation(events), [events]);
  const responses = messages.filter((message) => message.role === "assistant" && message.text);

  async function copyResponse(message: ConversationMessage) {
    await navigator.clipboard.writeText(message.text);
    setCopied(message.id);
    window.setTimeout(() => setCopied((current) => current === message.id ? "" : current), 1600);
  }

  return (
    <Modal>
      <Button variant="secondary" size="sm">
        <MessageSquareText size={15} /> 对话与回复
      </Button>
      <Modal.Backdrop variant="blur">
        <Modal.Container size="lg" scroll="inside" placement="center">
          <Modal.Dialog className="conversation-dialog">
            <Modal.CloseTrigger />
            <Modal.Header className="conversation-header">
              <div>
                <Modal.Heading>对话记录</Modal.Heading>
                <p>{task.adapter} · {messages.length} 条消息 · {responses.length} 轮回复</p>
              </div>
            </Modal.Header>
            <Modal.Body className="conversation-body">
              <div className="conversation-tabs" role="tablist" aria-label="对话视图">
                <button
                  type="button"
                  role="tab"
                  aria-selected={view === "conversation"}
                  className={view === "conversation" ? "active" : ""}
                  onClick={() => setView("conversation")}
                >
                  对话记录
                </button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={view === "responses"}
                  className={view === "responses" ? "active" : ""}
                  onClick={() => setView("responses")}
                >
                  完整回复 <span>{responses.length}</span>
                </button>
              </div>

              {view === "conversation" ? (
                <div className="conversation-list">
                  {messages.length === 0 ? (
                    <EmptyConversation text="还没有可展示的对话内容。" />
                  ) : messages.map((message) => (
                    <article className={`conversation-message ${message.role}`} key={message.id}>
                      <div className="message-avatar">{roleIcon(message.role)}</div>
                      <div className="message-content">
                        <div className="message-meta">
                          <strong>{roleLabel(message.role)}</strong>
                          <time>{message.occurredAt ? absoluteTime(message.occurredAt) : "刚刚"}</time>
                        </div>
                        <div className="message-text">{message.text}</div>
                      </div>
                    </article>
                  ))}
                </div>
              ) : (
                <div className="response-list">
                  {responses.length === 0 ? (
                    <EmptyConversation text="Agent 还没有产生文本回复。" />
                  ) : responses.map((message) => (
                    <article className="response-card" key={message.id}>
                      <header>
                        <div>
                          <strong>Agent 回复 {message.responseIndex}</strong>
                          <time>{message.occurredAt ? absoluteTime(message.occurredAt) : "刚刚"}</time>
                        </div>
                        <Button
                          variant="ghost"
                          size="sm"
                          isIconOnly
                          aria-label={`复制 Agent 回复 ${message.responseIndex}`}
                          onPress={() => void copyResponse(message)}
                        >
                          {copied === message.id ? <Check size={15} /> : <Copy size={15} />}
                        </Button>
                      </header>
                      <div className="response-text">{message.text}</div>
                    </article>
                  ))}
                </div>
              )}
            </Modal.Body>
          </Modal.Dialog>
        </Modal.Container>
      </Modal.Backdrop>
    </Modal>
  );
}

function EmptyConversation({text}: {text: string}) {
  return <div className="conversation-empty"><MessageSquareText size={24} /><span>{text}</span></div>;
}

function buildConversation(events: ForemanEvent[]): ConversationMessage[] {
  const messages: ConversationMessage[] = [];
  let activeAssistant: ConversationMessage | undefined;
  let responseIndex = 0;

  for (const event of [...events].sort((left, right) => left.sequence - right.sequence)) {
    if (event.type === "conversation.message") {
      const data = asRecord(event.data);
      const text = typeof data?.text === "string" ? data.text : "";
      if (!text) continue;
      const source = data?.source === "supervisor" ? "supervisor" : "operator";
      messages.push({id: event.id, role: source, text, occurredAt: event.occurred_at});
      activeAssistant = undefined;
      continue;
    }

    if (event.type === "agent.follow_up_started") {
      activeAssistant = undefined;
      continue;
    }

    const chunk = agentTextChunk(event);
    if (!chunk) continue;
    if (!activeAssistant) {
      responseIndex++;
      activeAssistant = {
        id: `response-${event.id || event.sequence}`,
        role: "assistant",
        text: "",
        occurredAt: event.occurred_at,
        responseIndex
      };
      messages.push(activeAssistant);
    }
    activeAssistant.text += chunk;
  }

  return messages;
}

function agentTextChunk(event: ForemanEvent): string {
  if (event.type !== "agent.session_update") return "";
  const update = asRecord(asRecord(event.data)?.update);
  if (update?.sessionUpdate !== "agent_message_chunk") return "";
  const content = asRecord(update.content);
  return typeof content?.text === "string" ? content.text : "";
}

function roleLabel(role: ConversationRole): string {
  if (role === "supervisor") return "监工";
  if (role === "assistant") return "Agent";
  return "你";
}

function roleIcon(role: ConversationRole) {
  if (role === "supervisor") return <ShieldCheck size={15} />;
  if (role === "assistant") return <Bot size={15} />;
  return <UserRound size={15} />;
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}
