import {Button, Chip, Spinner} from "@heroui/react";
import {Bot, Command, Inbox, RefreshCw} from "lucide-react";
import type {Task} from "../api";
import {relativeTime, shortID, statusLabel, statusTone} from "../status";

interface TaskListProps {
  tasks: Task[];
  selectedID?: string;
  loading: boolean;
  onSelect: (id: string) => void;
  onRefresh: () => void;
}

export function TaskList({tasks, selectedID, loading, onSelect, onRefresh}: TaskListProps) {
  return (
    <section className="task-list-panel">
      <div className="section-heading">
        <div>
          <div className="eyebrow">任务管理</div>
          <h2>任务队列 <span>{tasks.length}</span></h2>
        </div>
        <Button variant="ghost" size="sm" isIconOnly aria-label="刷新任务" onPress={onRefresh}>
          <RefreshCw size={16} className={loading ? "spin" : ""} />
        </Button>
      </div>

      <div className="task-list">
        {loading && tasks.length === 0 ? (
          <div className="empty-state"><Spinner size="sm" /><span>正在读取任务…</span></div>
        ) : tasks.length === 0 ? (
          <div className="empty-state">
            <Inbox size={26} />
            <strong>队列还是空的</strong>
            <span>从上方派发第一个任务。</span>
          </div>
        ) : tasks.map((task) => (
          <button
            className={`task-row ${selectedID === task.id ? "selected" : ""}`}
            key={task.id}
            type="button"
            onClick={() => onSelect(task.id)}
          >
            <span className={`task-kind ${task.kind}`}>
              {task.kind === "agent" ? <Bot size={17} /> : <Command size={17} />}
            </span>
            <span className="task-row-main">
              <span className="task-row-top">
                <strong>{task.adapter}</strong>
                <span>{relativeTime(task.updated_at)}</span>
              </span>
              <span className="task-row-bottom">
                <code>{shortID(task.id)}</code>
                <Chip size="sm" color={statusTone[task.status]} variant="soft">{statusLabel[task.status]}</Chip>
              </span>
              {task.workspace && <span className="task-workspace" title={task.workspace}>{task.workspace}</span>}
            </span>
          </button>
        ))}
      </div>
    </section>
  );
}
