import {useCallback, useEffect, useMemo, useState} from "react";
import {Button, Chip} from "@heroui/react";
import {Activity, Bot, PanelLeftClose, PanelLeftOpen, ShieldCheck, Wifi, WifiOff} from "lucide-react";
import {ApiError, createAuthSession, deleteTask, errorMessage, getAdapters, getAuthStatus, getTasks, type AdapterDescriptor, type Task} from "./api";
import {AuthScreen} from "./components/AuthScreen";
import {TaskComposer} from "./components/TaskComposer";
import {TaskDetail} from "./components/TaskDetail";
import {TaskList} from "./components/TaskList";

export function App() {
  const [adapters, setAdapters] = useState<AdapterDescriptor[]>([]);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [selectedID, setSelectedID] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [connected, setConnected] = useState(false);
  const [error, setError] = useState("");
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [authChecked, setAuthChecked] = useState(false);
  const [authRequired, setAuthRequired] = useState(false);
  const [authenticated, setAuthenticated] = useState(false);

  const loadTasks = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const [nextAdapters, nextTasks] = await Promise.all([getAdapters(), getTasks()]);
      setAdapters(nextAdapters);
      setTasks(nextTasks);
      setConnected(true);
      setError("");
      setSelectedID((current) => current ?? nextTasks[0]?.id);
    } catch (caught) {
      setConnected(false);
      if (caught instanceof ApiError && caught.status === 401) setAuthenticated(false);
      if (!silent) setError(errorMessage(caught));
    } finally {
      if (!silent) setLoading(false);
    }
  }, []);

  useEffect(() => {
    let active = true;
    void getAuthStatus()
      .then(async (auth) => {
        if (!active) return;
        setAuthRequired(auth.required);
        setAuthenticated(auth.authenticated);
        if (!auth.authenticated) {
          setLoading(false);
          return;
        }
        const [nextAdapters, nextTasks] = await Promise.all([getAdapters(), getTasks()]);
        if (!active) return;
        setAdapters(nextAdapters);
        setTasks(nextTasks);
        setSelectedID(nextTasks[0]?.id);
        setConnected(true);
        setError("");
      })
      .catch((caught) => {
        if (!active) return;
        setError(errorMessage(caught));
        setConnected(false);
      })
      .finally(() => {
        if (!active) return;
        setAuthChecked(true);
        setLoading(false);
      });
    return () => { active = false; };
  }, []);

  useEffect(() => {
    if (!authenticated) return;
    const timer = window.setInterval(() => void loadTasks(true), 2500);
    return () => window.clearInterval(timer);
  }, [authenticated, loadTasks]);

  const selectedTask = tasks.find((task) => task.id === selectedID);
  const activeCount = useMemo(() => tasks.filter((task) => ["queued", "running", "recovering", "verifying", "waiting_permission"].includes(task.status)).length, [tasks]);
  const attentionCount = useMemo(() => tasks.filter((task) => task.status === "attention_required").length, [tasks]);

  function handleCreated(task: Task) {
    setTasks((current) => [task, ...current.filter((item) => item.id !== task.id)]);
    setSelectedID(task.id);
    setSidebarOpen(true);
    window.setTimeout(() => void loadTasks(true), 500);
  }

  function handleChanged(task: Task) {
    setTasks((current) => current.map((item) => item.id === task.id ? task : item));
  }

  async function handleDelete(task: Task) {
    try {
      await deleteTask(task.id);
      const next = tasks.filter((item) => item.id !== task.id);
      setTasks(next);
      setSelectedID((selected) => selected === task.id ? next[0]?.id : selected);
      setError("");
    } catch (caught) {
      setError(errorMessage(caught));
    }
  }

  async function handleAuthenticate(token: string) {
    await createAuthSession(token);
    setLoading(true);
    try {
      const [nextAdapters, nextTasks] = await Promise.all([getAdapters(), getTasks()]);
      setAdapters(nextAdapters);
      setTasks(nextTasks);
      setSelectedID((current) => current ?? nextTasks[0]?.id);
      setConnected(true);
      setError("");
      setAuthenticated(true);
    } finally {
      setLoading(false);
    }
  }

  if (!authChecked) {
    return <AuthScreen loading />;
  }

  if (authRequired && !authenticated) {
    return <AuthScreen onAuthenticate={handleAuthenticate} />;
  }

  return (
    <div className="app-shell">
      <div className="ambient ambient-one" />
      <div className="ambient ambient-two" />

      <header className="app-header">
        <div className="brand-block">
          <div className="brand-icon"><Bot size={23} /></div>
          <div>
            <div className="brand-title">赛博监工</div>
            <div className="brand-subtitle">Cyber Foreman · 本地 Agent 控制台</div>
          </div>
        </div>
        <div className="header-metrics">
          <span><Activity size={15} /> {activeCount} 个执行中</span>
          {attentionCount > 0 && <Chip size="sm" color="danger" variant="soft">{attentionCount} 个待处理</Chip>}
          <span className={`connection ${connected ? "online" : "offline"}`}>
            {connected ? <Wifi size={15} /> : <WifiOff size={15} />}
            {connected ? "后端在线" : "后端离线"}
          </span>
        </div>
      </header>

      {error && (
        <div className="global-error" role="alert">
          <WifiOff size={18} />
          <span><strong>无法连接 Foreman</strong>{error}</span>
          <Button size="sm" variant="danger-soft" onPress={() => void loadTasks()}>重试</Button>
        </div>
      )}

      <main className={`workspace-layout ${sidebarOpen ? "" : "sidebar-collapsed"}`}>
        <aside className="control-sidebar">
          <TaskComposer adapters={adapters} onCreated={handleCreated} />
          <TaskList
            tasks={tasks}
            selectedID={selectedID}
            loading={loading}
            onSelect={setSelectedID}
            onDelete={(task) => void handleDelete(task)}
            onRefresh={() => void loadTasks()}
          />
          <footer className="sidebar-footer"><ShieldCheck size={14} /> 完成状态只由后端验证门禁决定</footer>
        </aside>

        <div className="detail-panel">
          <Button
            className="sidebar-toggle"
            variant="ghost"
            size="sm"
            isIconOnly
            aria-label={sidebarOpen ? "收起侧栏" : "展开侧栏"}
            onPress={() => setSidebarOpen((value) => !value)}
          >
            {sidebarOpen ? <PanelLeftClose size={17} /> : <PanelLeftOpen size={17} />}
          </Button>
          <TaskDetail task={selectedTask} onChanged={handleChanged} />
        </div>
      </main>
    </div>
  );
}
