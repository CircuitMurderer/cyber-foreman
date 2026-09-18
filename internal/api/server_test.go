package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cyber-foreman/internal/agent"
	"cyber-foreman/internal/app"
	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/event"
)

func TestCreateTaskUsesStableV1DTO(t *testing.T) {
	service, bus := newAPITestService(t)
	body := `{"kind":"command","adapter":"test","workspace":"` + t.TempDir() + `","input":{"command":["ignored"]},"supervision":{"idle_timeout":"2s"}}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	NewServer(service, bus).Handler().ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.StatusCode, recorder.Body.String())
	}
	var task taskResponse
	if err := json.NewDecoder(response.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if task.ID == "" || task.Adapter != "test" || task.Status != domain.TaskQueued || task.Links.Events == "" {
		t.Fatalf("unexpected task response: %#v", task)
	}
	if response.Header.Get("Location") != task.Links.Self {
		t.Fatalf("Location = %q, want %q", response.Header.Get("Location"), task.Links.Self)
	}
}

func TestCreateTaskRejectsNumericDuration(t *testing.T) {
	service, bus := newAPITestService(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(
		`{"adapter":"test","input":{"command":["ignored"]},"supervision":{"idle_timeout":1000}}`,
	))
	recorder := httptest.NewRecorder()
	NewServer(service, bus).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_request") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestEventStreamReplaysStableEnvelope(t *testing.T) {
	service, bus := newAPITestService(t)
	bus.Publish(domain.Event{TaskID: "task-replay", Type: domain.EventAgentOutput, Timestamp: time.Now().UTC(), Data: map[string]string{"line": "hello"}})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	NewServer(service, bus).Handler().ServeHTTP(recorder, request)
	joined := recorder.Body.String()
	if !strings.Contains(joined, "id: evt-1") || !strings.Contains(joined, `"version":"v1"`) || !strings.Contains(joined, `"occurred_at"`) {
		t.Fatalf("unexpected SSE frame: %s", joined)
	}
}

func TestEventResponseRedactsSecretsAndCapsPayload(t *testing.T) {
	event := domain.Event{
		Type: domain.EventAgentSessionUpdate,
		Data: map[string]any{
			"api_key": "should-not-leak",
			"output":  "GOOGLE_GENERATIVE_AI_API_KEY=should-not-leak",
		},
	}
	encoded, err := json.Marshal(eventResponse(event))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "should-not-leak") || !strings.Contains(string(encoded), "REDACTED") {
		t.Fatalf("secret was not redacted: %s", encoded)
	}

	large := boundedEventData(map[string]string{"output": strings.Repeat("x", maxEventDataBytes+1)})
	encoded, err = json.Marshal(large)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"truncated":true`) {
		t.Fatalf("large event was not capped: %s", encoded)
	}
}

func newAPITestService(t *testing.T) (*app.Service, *event.Bus) {
	t.Helper()
	registry, err := agent.NewRegistry(apiTestAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	bus := event.NewBus()
	return app.NewServiceWithRegistry(context.Background(), registry, "test", bus), bus
}

type apiTestAdapter struct{}

func (apiTestAdapter) Name() string { return "test" }
func (apiTestAdapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{Command: true}
}
func (apiTestAdapter) Start(_ context.Context, request agent.StartRequest) (agent.Session, error) {
	return agent.Session{ID: "session-" + request.TaskID}, nil
}
func (apiTestAdapter) Events(_ context.Context, sessionID string) (<-chan domain.Event, error) {
	events := make(chan domain.Event, 1)
	events <- domain.Event{SessionID: sessionID, Type: domain.EventAgentExited, Timestamp: time.Now().UTC(), Data: domain.AgentExitData{}}
	close(events)
	return events, nil
}
func (apiTestAdapter) Prompt(context.Context, string, agent.PromptRequest) (agent.PromptResult, error) {
	return agent.PromptResult{}, agent.ErrUnsupported
}
func (apiTestAdapter) Cancel(context.Context, string) error { return nil }
func (apiTestAdapter) SetConfigOption(context.Context, string, agent.ConfigOption) error {
	return agent.ErrUnsupported
}
func (apiTestAdapter) Stop(context.Context, string) error { return nil }
