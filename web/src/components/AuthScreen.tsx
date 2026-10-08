import {useState, type FormEvent} from "react";
import {Button, Card, Spinner} from "@heroui/react";
import {Bot, KeyRound, ShieldCheck} from "lucide-react";
import {errorMessage} from "../api";

interface AuthScreenProps {
  loading?: boolean;
  onAuthenticate?: (token: string) => Promise<void>;
}

export function AuthScreen({loading = false, onAuthenticate}: AuthScreenProps) {
  const [token, setToken] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!onAuthenticate || !token) return;
    setSubmitting(true);
    setError("");
    try {
      await onAuthenticate(token);
      setToken("");
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <main className="auth-shell">
      <Card className="auth-card" variant="secondary">
        <Card.Header className="auth-header">
          <div className="brand-icon"><Bot size={23} /></div>
          <div><Card.Title>赛博监工</Card.Title><Card.Description>受保护的本地 Agent 控制台</Card.Description></div>
        </Card.Header>
        <Card.Content>
          {loading ? (
            <div className="auth-loading"><Spinner size="sm" /><span>正在检查控制平面…</span></div>
          ) : (
            <form className="auth-form" onSubmit={submit}>
              <div className="auth-message"><ShieldCheck size={18} /><span>此 Foreman 要求 API Token。Token 只用于换取当前浏览器的 HttpOnly 会话。</span></div>
              <label className="field">
                <span className="field-label"><KeyRound size={15} /> API Token</span>
                <input
                  className="control mono"
                  type="password"
                  autoComplete="current-password"
                  value={token}
                  onChange={(event) => setToken(event.target.value)}
                  placeholder="输入部署端配置的 Token"
                  autoFocus
                />
              </label>
              {error && <div className="inline-error" role="alert">{error}</div>}
              <Button variant="primary" type="submit" fullWidth isDisabled={submitting || !token}>
                {submitting ? "正在验证…" : "进入控制台"}
              </Button>
            </form>
          )}
        </Card.Content>
      </Card>
    </main>
  );
}
