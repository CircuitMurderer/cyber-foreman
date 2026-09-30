export type TaskStatus =
  | "queued"
  | "running"
  | "recovering"
  | "verifying"
  | "waiting_input"
  | "completed"
  | "failed"
  | "stopped"
  | "attention_required";

export type TaskKind = "agent" | "command";

export interface AdapterCapabilities {
  command: boolean;
  structured_events: boolean;
  resume_session: boolean;
  prompt: boolean;
  mid_turn_message: boolean;
  cancel_turn: boolean;
  session_config: boolean;
  tool_events: boolean;
  permission_events: boolean;
}

export interface AdapterDescriptor {
  name: string;
  capabilities: AdapterCapabilities;
  installed: boolean;
  healthy: boolean;
  command?: string;
  version?: string;
  protocol_version?: number;
  agent_info?: {name?: string; title?: string; version?: string};
  error?: string;
}

export interface TaskLinks {
  self: string;
  events: string;
  actions: string;
}

export interface Task {
  id: string;
  kind: TaskKind;
  adapter: string;
  workspace?: string;
  status: TaskStatus;
  exit_code?: number;
  error?: string;
  created_at: string;
  updated_at: string;
  available_actions?: Array<"interrupt" | "continue" | "finish" | "cancel" | "delete">;
  links: TaskLinks;
}

export interface ForemanEvent {
  id: string;
  version: string;
  sequence: number;
  task_id: string;
  session_id?: string;
  type: string;
  occurred_at: string;
  data?: unknown;
}

export interface CreateTaskRequest {
  kind: TaskKind;
  adapter: string;
  workspace?: string;
  input: {prompt?: string; command?: string[]};
  model?: string;
  supervision?: {
    idle_timeout?: string;
    hard_timeout?: string;
    max_nudges?: number;
    max_retries?: number;
    max_test_repairs?: number;
  };
  verification?: {
    workspace?: boolean;
    commands?: Array<{argv: string[]; timeout?: string}>;
  };
}

interface ApiErrorBody {
  error?: {code?: string; message?: string};
}

export class ApiError extends Error {
  constructor(
    message: string,
    public readonly status: number,
    public readonly code = "request_failed"
  ) {
    super(message);
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: {
      Accept: "application/json",
      ...(init?.body ? {"Content-Type": "application/json"} : {}),
      ...init?.headers
    }
  });
  if (!response.ok) {
    let body: ApiErrorBody | undefined;
    try {
      body = (await response.json()) as ApiErrorBody;
    } catch {
      // The status text below is enough when a proxy returns a non-JSON error.
    }
    throw new ApiError(
      body?.error?.message || response.statusText || "请求失败",
      response.status,
      body?.error?.code
    );
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return (await response.json()) as T;
}

export async function getAdapters(): Promise<AdapterDescriptor[]> {
  const response = await request<{adapters: AdapterDescriptor[]}>("/api/v1/adapters");
  return response.adapters;
}

export async function getTasks(): Promise<Task[]> {
  const response = await request<{tasks: Task[]}>("/api/v1/tasks");
  return response.tasks;
}

export function getTask(id: string): Promise<Task> {
  return request<Task>(`/api/v1/tasks/${encodeURIComponent(id)}`);
}

export function createTask(payload: CreateTaskRequest): Promise<Task> {
  return request<Task>("/api/v1/tasks", {method: "POST", body: JSON.stringify(payload)});
}

export function interruptTask(id: string, message: string): Promise<void> {
  return request<void>(`/api/v1/tasks/${encodeURIComponent(id)}/actions`, {
    method: "POST",
    body: JSON.stringify({type: "interrupt", message})
  });
}

export function continueTask(id: string, message: string): Promise<void> {
  return request<void>(`/api/v1/tasks/${encodeURIComponent(id)}/actions`, {
    method: "POST",
    body: JSON.stringify({type: "continue", message})
  });
}

export function cancelTask(id: string): Promise<void> {
  return request<void>(`/api/v1/tasks/${encodeURIComponent(id)}/actions`, {
    method: "POST",
    body: JSON.stringify({type: "cancel"})
  });
}

export function finishTask(id: string): Promise<void> {
  return request<void>(`/api/v1/tasks/${encodeURIComponent(id)}/actions`, {
    method: "POST",
    body: JSON.stringify({type: "finish"})
  });
}

export function deleteTask(id: string): Promise<void> {
  return request<void>(`/api/v1/tasks/${encodeURIComponent(id)}`, {method: "DELETE"});
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "发生未知错误";
}

export const terminalStatuses = new Set<TaskStatus>([
  "completed",
  "failed",
  "stopped",
  "attention_required"
]);

export const eventTypes = [
  "task.created",
  "task.state",
  "task.attention_required",
  "agent.started",
  "agent.output",
  "agent.error",
  "agent.exited",
  "agent.session_update",
  "agent.stderr",
  "agent.disconnected",
  "agent.permission_requested",
  "agent.interrupt_requested",
  "agent.follow_up_started",
  "conversation.message",
  "supervisor.decision",
  "supervisor.action_started",
  "supervisor.action_finished",
  "verification.started",
  "verification.finished",
  "stream.gap"
] as const;
