import type {TaskStatus} from "./api";

export const statusLabel: Record<TaskStatus, string> = {
  queued: "排队中",
  running: "执行中",
  recovering: "恢复中",
  verifying: "验证中",
  completed: "已完成",
  failed: "失败",
  stopped: "已停止",
  attention_required: "需要处理"
};

export const statusTone: Record<TaskStatus, "default" | "accent" | "success" | "warning" | "danger"> = {
  queued: "default",
  running: "accent",
  recovering: "warning",
  verifying: "warning",
  completed: "success",
  failed: "danger",
  stopped: "default",
  attention_required: "danger"
};

export function shortID(id: string): string {
  return id.startsWith("task-") ? id.slice(5, 13) : id.slice(0, 8);
}

export function relativeTime(value: string): string {
  const delta = Date.now() - new Date(value).getTime();
  if (delta < 10_000) return "刚刚";
  if (delta < 60_000) return `${Math.floor(delta / 1000)} 秒前`;
  if (delta < 3_600_000) return `${Math.floor(delta / 60_000)} 分钟前`;
  if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)} 小时前`;
  return new Intl.DateTimeFormat("zh-CN", {month: "short", day: "numeric"}).format(new Date(value));
}

export function absoluteTime(value: string): string {
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false
  }).format(new Date(value));
}
