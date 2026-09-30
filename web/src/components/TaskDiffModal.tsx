import {useState} from "react";
import {Button, Chip, Modal} from "@heroui/react";
import {FileDiff, GitPullRequestArrow, RefreshCw, Send} from "lucide-react";
import {
  continueTask,
  errorMessage,
  getTask,
  getTaskDiff,
  type Task,
  type TaskDiff
} from "../api";

interface TaskDiffModalProps {
  task: Task;
  onChanged: (task: Task) => void;
}

export function TaskDiffModal({task, onChanged}: TaskDiffModalProps) {
  const [diff, setDiff] = useState<TaskDiff>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [sending, setSending] = useState(false);
  const canReview = task.available_actions?.includes("continue") === true;

  async function load() {
    setLoading(true);
    setError("");
    try {
      setDiff(await getTaskDiff(task.id));
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setLoading(false);
    }
  }

  async function sendReview() {
    if (!feedback.trim()) return;
    setSending(true);
    setError("");
    try {
      await continueTask(task.id, `代码审查反馈：\n${feedback.trim()}\n\n请根据反馈修改实现，并重新运行必要验证。`);
      setFeedback("");
      onChanged(await getTask(task.id));
      await load();
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setSending(false);
    }
  }

  return (
    <Modal>
      <Button variant="secondary" size="sm" onPress={() => void load()}>
        <FileDiff size={15} /> 任务变更
      </Button>
      <Modal.Backdrop variant="blur">
        <Modal.Container size="lg" scroll="inside" placement="center">
          <Modal.Dialog className="diff-dialog">
            <Modal.CloseTrigger />
            <Modal.Header className="conversation-header">
              <div>
                <Modal.Heading>任务变更审查</Modal.Heading>
                <p>{task.source_workspace || "源仓库"} · base {shortRevision(diff?.base_revision || task.base_revision)}</p>
              </div>
              <Button variant="ghost" size="sm" isIconOnly aria-label="刷新 diff" onPress={() => void load()} isDisabled={loading}>
                <RefreshCw size={15} />
              </Button>
            </Modal.Header>
            <Modal.Body className="diff-body">
              {error && <div className="inline-error">{error}</div>}
              {loading && !diff ? (
                <div className="diff-empty">正在读取 worktree 变更……</div>
              ) : diff ? (
                <>
                  <div className="diff-files">
                    <div>
                      <GitPullRequestArrow size={16} />
                      <strong>{diff.files.length} 个文件</strong>
                      {diff.truncated && <Chip size="sm" color="warning" variant="soft">内容已截断</Chip>}
                    </div>
                    {diff.files.length === 0 ? (
                      <span>当前 worktree 没有未提交变更。</span>
                    ) : (
                      <ul>
                        {diff.files.map((file) => (
                          <li key={`${file.status}-${file.path}`}>
                            <code>{file.status}</code><span>{file.path}</span>{file.redacted && <small>已隐藏</small>}
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
                  <pre className="diff-patch">{diff.patch || "没有可展示的文本 diff。"}</pre>
                  <section className="review-feedback">
                    <strong>审查反馈</strong>
                    <textarea
                      className="control textarea"
                      rows={3}
                      value={feedback}
                      onChange={(event) => setFeedback(event.target.value)}
                      placeholder={canReview ? "指出需要调整的文件、行为或测试……" : "任务等待输入时才能发送审查反馈"}
                      disabled={!canReview || sending}
                    />
                    <Button variant="primary" onPress={() => void sendReview()} isDisabled={!canReview || sending || !feedback.trim()}>
                      <Send size={15} /> 发送给 Agent 修改
                    </Button>
                  </section>
                </>
              ) : null}
            </Modal.Body>
          </Modal.Dialog>
        </Modal.Container>
      </Modal.Backdrop>
    </Modal>
  );
}

function shortRevision(value?: string): string {
  return value ? value.slice(0, 12) : "unknown";
}
