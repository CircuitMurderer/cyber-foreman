package supervisor

import (
	"testing"

	"cyber-foreman/internal/domain"
)

func TestRulesDetectFailedProcess(t *testing.T) {
	event := domain.Event{Type: domain.EventAgentExited, Data: domain.AgentExitData{ExitCode: 2}}
	if got := (Rules{}).Evaluate(event); got.Action != ActionRetry {
		t.Fatalf("action = %q, want %q", got.Action, ActionRetry)
	}
}

func TestRulesDetectWaitingAgent(t *testing.T) {
	event := domain.Event{Type: domain.EventAgentOutput, Data: domain.AgentOutputData{Line: "Permission required to continue"}}
	if got := (Rules{}).Evaluate(event); got.Action != ActionNudge {
		t.Fatalf("action = %q, want %q", got.Action, ActionNudge)
	}
}
