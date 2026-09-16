package domain

import "time"

type TaskStatus string

const (
	TaskQueued     TaskStatus = "queued"
	TaskRunning    TaskStatus = "running"
	TaskRecovering TaskStatus = "recovering"
	TaskVerifying  TaskStatus = "verifying"
	TaskCompleted  TaskStatus = "completed"
	TaskFailed     TaskStatus = "failed"
	TaskStopped    TaskStatus = "stopped"
)

func (s TaskStatus) Terminal() bool {
	return s == TaskCompleted || s == TaskFailed || s == TaskStopped
}

type Task struct {
	ID        string     `json:"id"`
	Command   []string   `json:"command"`
	CWD       string     `json:"cwd,omitempty"`
	Status    TaskStatus `json:"status"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	Error     string     `json:"error,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type EventType string

const (
	EventTaskCreated  EventType = "task.created"
	EventTaskState    EventType = "task.state"
	EventAgentStarted EventType = "agent.started"
	EventAgentOutput  EventType = "agent.output"
	EventAgentError   EventType = "agent.error"
	EventAgentExited  EventType = "agent.exited"
)

type Event struct {
	TaskID    string    `json:"task_id"`
	SessionID string    `json:"session_id,omitempty"`
	Type      EventType `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data,omitempty"`
}

type AgentOutputData struct {
	Stream string `json:"stream"`
	Line   string `json:"line"`
}

type AgentExitData struct {
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

type TaskStateData struct {
	From   TaskStatus `json:"from"`
	To     TaskStatus `json:"to"`
	Reason string     `json:"reason,omitempty"`
}
