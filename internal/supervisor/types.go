package supervisor

import (
	"time"

	"cyber-foreman/internal/domain"
)

type ObservationType string

const (
	ObservationAgentProgress     ObservationType = "agent.progress"
	ObservationAgentTurnFinished ObservationType = "agent.turn_finished"
	ObservationAgentDisconnected ObservationType = "agent.disconnected"
	ObservationAgentExited       ObservationType = "agent.exited"
	ObservationTimerIdle         ObservationType = "timer.idle"
	ObservationTimerHardTimeout  ObservationType = "timer.hard_timeout"
	ObservationTestFinished      ObservationType = "verification.test_finished"
	ObservationGitFinished       ObservationType = "verification.git_finished"
	ObservationVerificationReady ObservationType = "verification.ready"
)

type Observation struct {
	ID        string          `json:"id"`
	TaskID    string          `json:"task_id"`
	SessionID string          `json:"session_id,omitempty"`
	Type      ObservationType `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Data      any             `json:"data,omitempty"`
}

type AgentExitData struct {
	ExitCode int `json:"exit_code"`
}

type VerificationData struct {
	Verifier          string `json:"verifier"`
	Passed            bool   `json:"passed"`
	SecurityViolation bool   `json:"security_violation,omitempty"`
	Summary           string `json:"summary,omitempty"`
}

type Policy struct {
	IdleTimeout    time.Duration `json:"idle_timeout"`
	HardTimeout    time.Duration `json:"hard_timeout"`
	MaxNudges      int           `json:"max_nudges"`
	MaxRetries     int           `json:"max_retries"`
	MaxTestRepairs int           `json:"max_test_repairs"`
}

func DefaultPolicy() Policy {
	return Policy{
		IdleTimeout: 90 * time.Second, HardTimeout: 30 * time.Minute,
		MaxNudges: 2, MaxRetries: 2, MaxTestRepairs: 2,
	}
}

type Budget struct {
	Nudges      int `json:"nudges"`
	Retries     int `json:"retries"`
	TestRepairs int `json:"test_repairs"`
}

type VerificationState struct {
	TestFinished bool `json:"test_finished"`
	TestPassed   bool `json:"test_passed"`
	GitFinished  bool `json:"git_finished"`
	GitPassed    bool `json:"git_passed"`
}

type Snapshot struct {
	TaskID           string            `json:"task_id"`
	Status           domain.TaskStatus `json:"status"`
	StartedAt        time.Time         `json:"started_at"`
	LastProgressAt   time.Time         `json:"last_progress_at"`
	IdleSequence     int               `json:"idle_sequence"`
	Policy           Policy            `json:"policy"`
	Budget           Budget            `json:"budget"`
	Verification     VerificationState `json:"verification"`
	AppliedDecisions map[string]bool   `json:"applied_decisions,omitempty"`
}

type Action string

const (
	ActionNone              Action = "none"
	ActionNudge             Action = "nudge"
	ActionCancelAndFollowUp Action = "cancel_and_follow_up"
	ActionRetrySession      Action = "retry_session"
	ActionRepair            Action = "repair"
	ActionStartVerification Action = "start_verification"
	ActionComplete          Action = "complete"
	ActionAttentionRequired Action = "attention_required"
	ActionStop              Action = "stop"
)

type BudgetCost struct {
	Nudges      int `json:"nudges,omitempty"`
	Retries     int `json:"retries,omitempty"`
	TestRepairs int `json:"test_repairs,omitempty"`
}

type Decision struct {
	RuleID     string     `json:"rule_id"`
	Action     Action     `json:"action"`
	Reason     string     `json:"reason"`
	Evidence   []string   `json:"evidence"`
	DedupeKey  string     `json:"dedupe_key"`
	BudgetCost BudgetCost `json:"budget_cost"`
}
