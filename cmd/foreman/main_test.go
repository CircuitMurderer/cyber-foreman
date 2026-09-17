package main

import (
	"encoding/json"
	"testing"

	"cyber-foreman/internal/domain"
)

func TestIsAgentMessageChunk(t *testing.T) {
	tests := []struct {
		name   string
		event  domain.Event
		wanted bool
	}{
		{
			name: "message chunk",
			event: domain.Event{Type: domain.EventAgentSessionUpdate, Data: domain.AgentSessionUpdateData{
				Update: json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}`),
			}},
			wanted: true,
		},
		{
			name: "config update",
			event: domain.Event{Type: domain.EventAgentSessionUpdate, Data: domain.AgentSessionUpdateData{
				Update: json.RawMessage(`{"sessionUpdate":"config_option_update"}`),
			}},
		},
		{name: "other event", event: domain.Event{Type: domain.EventAgentStderr}},
		{
			name: "invalid update",
			event: domain.Event{Type: domain.EventAgentSessionUpdate, Data: domain.AgentSessionUpdateData{
				Update: json.RawMessage(`{`),
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isAgentMessageChunk(test.event); got != test.wanted {
				t.Fatalf("isAgentMessageChunk() = %v, want %v", got, test.wanted)
			}
		})
	}
}
