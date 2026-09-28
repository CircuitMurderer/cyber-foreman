import type {ForemanEvent} from "./api";

export type ConversationRole = "operator" | "supervisor" | "assistant";

export interface ConversationMessage {
  id: string;
  role: ConversationRole;
  text: string;
  occurredAt: string;
  responseIndex?: number;
}

export function buildConversation(events: ForemanEvent[]): ConversationMessage[] {
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

export function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}
