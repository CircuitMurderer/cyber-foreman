package supervisor

import (
	"encoding/json"

	"cyber-foreman/internal/domain"
)

var meaningfulACPUpdates = map[string]bool{
	"agent_message_chunk": true,
	"agent_thought_chunk": true,
	"tool_call":           true,
	"tool_call_update":    true,
	"plan":                true,
	"plan_update":         true,
}

func IsMeaningfulProgress(event domain.Event) bool {
	switch event.Type {
	case domain.EventAgentOutput:
		return true
	case domain.EventAgentSessionUpdate:
		data, ok := event.Data.(domain.AgentSessionUpdateData)
		if !ok {
			return false
		}
		var update struct {
			SessionUpdate string `json:"sessionUpdate"`
		}
		if err := json.Unmarshal(data.Update, &update); err != nil {
			return false
		}
		return meaningfulACPUpdates[update.SessionUpdate]
	default:
		return false
	}
}

func ObservationFromEvent(id string, event domain.Event) (Observation, bool) {
	base := Observation{
		ID: id, TaskID: event.TaskID, SessionID: event.SessionID, Timestamp: event.Timestamp,
	}
	if IsMeaningfulProgress(event) {
		base.Type = ObservationAgentProgress
		return base, true
	}
	switch event.Type {
	case domain.EventAgentDisconnected:
		base.Type = ObservationAgentDisconnected
		return base, true
	case domain.EventAgentExited:
		data, ok := event.Data.(domain.AgentExitData)
		if !ok {
			return Observation{}, false
		}
		base.Type = ObservationAgentExited
		base.Data = AgentExitData{ExitCode: data.ExitCode}
		return base, true
	default:
		return Observation{}, false
	}
}
