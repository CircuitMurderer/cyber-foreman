package supervisor

import (
	"strings"

	"cyber-foreman/internal/domain"
)

type Action string

const (
	ActionNone     Action = "none"
	ActionNudge    Action = "nudge"
	ActionRetry    Action = "retry"
	ActionEscalate Action = "escalate"
)

type Decision struct {
	Action Action `json:"action"`
	Reason string `json:"reason,omitempty"`
}

type Rules struct{}

func (Rules) Evaluate(event domain.Event) Decision {
	switch event.Type {
	case domain.EventAgentExited:
		data, ok := event.Data.(domain.AgentExitData)
		if ok && data.ExitCode != 0 {
			return Decision{Action: ActionRetry, Reason: "agent process exited unsuccessfully"}
		}
	case domain.EventAgentOutput:
		data, ok := event.Data.(domain.AgentOutputData)
		if !ok {
			break
		}
		line := strings.ToLower(data.Line)
		if strings.Contains(line, "waiting for input") || strings.Contains(line, "permission required") {
			return Decision{Action: ActionNudge, Reason: "agent is waiting for operator input"}
		}
	}
	return Decision{Action: ActionNone}
}
