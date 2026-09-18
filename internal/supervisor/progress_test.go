package supervisor

import (
	"encoding/json"
	"testing"

	"cyber-foreman/internal/domain"
)

func TestIsMeaningfulProgress(t *testing.T) {
	tests := []struct {
		name string
		kind string
		want bool
	}{
		{name: "message", kind: "agent_message_chunk", want: true},
		{name: "tool", kind: "tool_call_update", want: true},
		{name: "usage", kind: "usage_update", want: false},
		{name: "config", kind: "config_option_update", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]string{"sessionUpdate": test.kind})
			if err != nil {
				t.Fatal(err)
			}
			event := domain.Event{Type: domain.EventAgentSessionUpdate, Data: domain.AgentSessionUpdateData{Update: payload}}
			if got := IsMeaningfulProgress(event); got != test.want {
				t.Fatalf("IsMeaningfulProgress() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestObservationFromEvent(t *testing.T) {
	event := domain.Event{
		TaskID: "task-1", SessionID: "session-1", Type: domain.EventAgentExited,
		Data: domain.AgentExitData{ExitCode: 7},
	}
	observation, ok := ObservationFromEvent("event-1", event)
	if !ok {
		t.Fatal("expected observation")
	}
	if observation.Type != ObservationAgentExited || observation.Data.(AgentExitData).ExitCode != 7 {
		t.Fatalf("unexpected observation: %#v", observation)
	}
}
