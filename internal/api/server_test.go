package api

import (
	"context"
	"encoding/json"
	"errors"
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

func TestCreateTaskRejectsNegativeSemanticRedirectBudget(t *testing.T) {
	service, bus := newAPITestService(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(
		`{"adapter":"test","input":{"command":["ignored"]},"supervision":{"max_semantic_redirects":-1}}`,
	))
	recorder := httptest.NewRecorder()
	NewServer(service, bus).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "max_semantic_redirects") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateTaskRejectsNegativeSemanticEscalationBudget(t *testing.T) {
	service, bus := newAPITestService(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(
		`{"adapter":"test","input":{"command":["ignored"]},"supervision":{"max_semantic_escalations":-1}}`,
	))
	recorder := httptest.NewRecorder()
	NewServer(service, bus).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "max_semantic_escalations") {
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

func TestDeleteTaskRemovesCompletedTask(t *testing.T) {
	service, bus := newAPITestService(t)
	task, err := service.StartTask(app.StartTaskRequest{Command: []string{"ignored"}, CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, getErr := service.GetTask(task.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if current.Status.Terminal() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	current, err := service.GetTask(task.ID)
	if err != nil || !current.Status.Terminal() {
		t.Fatalf("task did not finish: %#v err=%v", current, err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/tasks/"+task.ID, nil)
	request.SetPathValue("id", task.ID)
	recorder := httptest.NewRecorder()
	NewServer(service, bus).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := service.GetTask(task.ID); !errors.Is(err, app.ErrTaskNotFound) {
		t.Fatalf("GetTask error=%v, want ErrTaskNotFound", err)
	}
}

func TestAgentRequestIsInteractive(t *testing.T) {
	request := createTaskRequest{
		Adapter: "opencode", WorkspaceMode: "worktree", Input: taskInput{Prompt: "continue later"},
	}
	converted, err := request.appRequest()
	if err != nil {
		t.Fatal(err)
	}
	if !converted.Interactive || !converted.Worktree {
		t.Fatalf("REST agent task flags: interactive=%v worktree=%v", converted.Interactive, converted.Worktree)
	}
}

func TestAgentWorkspaceGateRequiresChangesByDefaultAndCanBeDisabled(t *testing.T) {
	request := createTaskRequest{
		Adapter: "opencode", Input: taskInput{Prompt: "implement the change"},
		Verification: verificationRequest{Workspace: true},
	}
	converted, err := request.appRequest()
	if err != nil {
		t.Fatal(err)
	}
	if !converted.Verification.WorkspacePolicy.RequireChanges {
		t.Fatal("agent workspace verification should require a change by default")
	}

	disabled := false
	request.Verification.WorkspacePolicy.RequireChanges = &disabled
	converted, err = request.appRequest()
	if err != nil {
		t.Fatal(err)
	}
	if converted.Verification.WorkspacePolicy.RequireChanges {
		t.Fatal("analysis-only task could not disable require_changes")
	}
}

func TestCreateTaskRejectsRequireChangesWithoutWorkspaceVerification(t *testing.T) {
	enabled := true
	_, err := (createTaskRequest{
		Adapter: "opencode", Input: taskInput{Prompt: "implement the change"},
		Verification: verificationRequest{WorkspacePolicy: workspacePolicy{RequireChanges: &enabled}},
	}).appRequest()
	if err == nil || !strings.Contains(err.Error(), "requires workspace verification") {
		t.Fatalf("error=%v, want require_changes validation", err)
	}
}

func TestWaitingTimeoutIsParsed(t *testing.T) {
	converted, err := (createTaskRequest{
		Adapter: "opencode", Input: taskInput{Prompt: "wait for me"},
		Supervision: &supervisionPolicy{WaitingTimeout: "45m"},
	}).appRequest()
	if err != nil {
		t.Fatal(err)
	}
	if converted.Supervision == nil || converted.Supervision.WaitingTimeout != 45*time.Minute {
		t.Fatalf("waiting timeout=%v", converted.Supervision)
	}
}

func TestCreateTaskRejectsUnknownWorkspaceMode(t *testing.T) {
	_, err := (createTaskRequest{
		Adapter: "opencode", WorkspaceMode: "container", Input: taskInput{Prompt: "test"},
	}).appRequest()
	if err == nil || !strings.Contains(err.Error(), "workspace_mode") {
		t.Fatalf("error=%v, want workspace_mode validation", err)
	}
}

func TestTaskResponseIncludesWorktreeMetadata(t *testing.T) {
	response := newTaskResponse(domain.Task{
		ID: "task-isolated", Kind: domain.TaskKindAgent, Adapter: "opencode",
		CWD: "/git/.git/foreman-worktrees/task-isolated/subdir", SourceCWD: "/git/subdir",
		WorktreeRoot: "/git/.git/foreman-worktrees/task-isolated", BaseRevision: "deadbeef",
	})
	if response.SourceWorkspace != "/git/subdir" || response.WorktreeRoot == "" || response.BaseRevision != "deadbeef" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestTaskDiffRejectsSharedWorkspace(t *testing.T) {
	service, bus := newAPITestService(t)
	task, err := service.StartTask(app.StartTaskRequest{Command: []string{"ignored"}, CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+task.ID+"/diff", nil)
	request.SetPathValue("id", task.ID)
	recorder := httptest.NewRecorder()
	NewServer(service, bus).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "diff_unavailable") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestFinishActionCompletesWaitingInteractiveTask(t *testing.T) {
	adapter := &interactiveAPITestAdapter{events: make(chan domain.Event, 4)}
	registry, err := agent.NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	bus := event.NewBus()
	service := app.NewServiceWithRegistry(context.Background(), registry, adapter.Name(), bus)
	task, err := service.StartTask(app.StartTaskRequest{
		Adapter: adapter.Name(), Prompt: "one turn", Interactive: true, CWD: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, getErr := service.GetTask(task.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if current.Status == domain.TaskWaiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	current, err := service.GetTask(task.ID)
	if err != nil || current.Status != domain.TaskWaiting {
		t.Fatalf("task did not reach waiting_input: %#v err=%v", current, err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+task.ID+"/actions", strings.NewReader(`{"type":"finish"}`))
	request.SetPathValue("id", task.ID)
	recorder := httptest.NewRecorder()
	NewServer(service, bus).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	finished, err := service.GetTask(task.ID)
	if err != nil || finished.Status != domain.TaskCompleted {
		t.Fatalf("finished task=%#v err=%v", finished, err)
	}
}

func TestResolvePermissionActionResumesAgent(t *testing.T) {
	adapter := &permissionAPITestAdapter{
		events: make(chan domain.Event, 4), decision: make(chan string, 1), resolved: make(chan string, 1),
	}
	registry, err := agent.NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	bus := event.NewBus()
	service := app.NewServiceWithRegistry(context.Background(), registry, adapter.Name(), bus)
	task, err := service.StartTask(app.StartTaskRequest{
		Adapter: adapter.Name(), Prompt: "protected work", Interactive: true, CWD: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.StopTask(task.ID) }()
	waitForAPITaskStatus(t, service, task.ID, domain.TaskPermission)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+task.ID+"/actions", strings.NewReader(
		`{"type":"resolve_permission","request_id":"permission-1","option_id":"allow"}`,
	))
	request.SetPathValue("id", task.ID)
	recorder := httptest.NewRecorder()
	NewServer(service, bus).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if decision := <-adapter.resolved; decision != "allow" {
		t.Fatalf("permission decision=%q", decision)
	}
	waitForAPITaskStatus(t, service, task.ID, domain.TaskWaiting)
}

func TestUnavailableAdapterIsListedButRejectsTasks(t *testing.T) {
	registry, err := agent.NewRegistry(unavailableAPITestAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	bus := event.NewBus()
	service := app.NewServiceWithRegistry(context.Background(), registry, "unavailable", bus)
	server := NewServer(service, bus).Handler()

	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/adapters", nil)
	listRecorder := httptest.NewRecorder()
	server.ServeHTTP(listRecorder, listRequest)
	if listRecorder.Code != http.StatusOK || !strings.Contains(listRecorder.Body.String(), `"installed":false`) || !strings.Contains(listRecorder.Body.String(), `"healthy":false`) {
		t.Fatalf("unexpected adapter list: status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}

	createRequest := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(
		`{"adapter":"unavailable","input":{"command":["ignored"]}}`,
	))
	createRecorder := httptest.NewRecorder()
	server.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusConflict || !strings.Contains(createRecorder.Body.String(), "adapter_unavailable") {
		t.Fatalf("status=%d body=%s", createRecorder.Code, createRecorder.Body.String())
	}
}

func TestAPITokenProtectsControlPlaneAndCreatesBrowserSession(t *testing.T) {
	service, bus := newAPITestService(t)
	handler := NewServerWithOptions(service, bus, ServerOptions{APIToken: "test-token"}).Handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil))
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("unauthorized status=%d headers=%v body=%s", unauthorized.Code, unauthorized.Header(), unauthorized.Body.String())
	}

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status=%d", health.Code)
	}

	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/v1/auth", nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"required":true`) || !strings.Contains(status.Body.String(), `"authenticated":false`) {
		t.Fatalf("auth status=%d body=%s", status.Code, status.Body.String())
	}

	badLogin := httptest.NewRecorder()
	badRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", strings.NewReader(`{"token":"wrong"}`))
	handler.ServeHTTP(badLogin, badRequest)
	if badLogin.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status=%d body=%s", badLogin.Code, badLogin.Body.String())
	}

	login := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", strings.NewReader(`{"token":"test-token"}`))
	handler.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusOK || len(login.Result().Cookies()) != 1 || login.Result().Cookies()[0].Value == "test-token" {
		t.Fatalf("login status=%d cookies=%v body=%s", login.Code, login.Result().Cookies(), login.Body.String())
	}
	if cookie := login.Result().Cookies()[0]; !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe session cookie: %#v", cookie)
	}

	authorized := httptest.NewRecorder()
	authorizedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	authorizedRequest.AddCookie(login.Result().Cookies()[0])
	handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK {
		t.Fatalf("cookie auth status=%d body=%s", authorized.Code, authorized.Body.String())
	}

	bearer := httptest.NewRecorder()
	bearerRequest := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	bearerRequest.Header.Set("Authorization", "Bearer test-token")
	handler.ServeHTTP(bearer, bearerRequest)
	if bearer.Code != http.StatusOK {
		t.Fatalf("bearer auth status=%d body=%s", bearer.Code, bearer.Body.String())
	}
}

func TestTaskPolicyDenialReturnsForbidden(t *testing.T) {
	service, bus := newAPITestService(t)
	service.SetRequestPolicy(denyAllPolicy{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(
		`{"adapter":"test","workspace":"/tmp","input":{"command":["ignored"]}}`,
	))
	recorder := httptest.NewRecorder()
	NewServer(service, bus).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "task_forbidden") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if tasks := service.ListTasks(); len(tasks) != 0 {
		t.Fatalf("forbidden task was persisted: %#v", tasks)
	}
}

type denyAllPolicy struct{}

func (denyAllPolicy) AuthorizeTask(app.TaskAuthorization) error {
	return app.ErrTaskForbidden
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

type unavailableAPITestAdapter struct{ apiTestAdapter }

type interactiveAPITestAdapter struct {
	events chan domain.Event
}

type permissionAPITestAdapter struct {
	events   chan domain.Event
	decision chan string
	resolved chan string
	taskID   string
}

func (*interactiveAPITestAdapter) Name() string { return "interactive-test" }
func (*interactiveAPITestAdapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{StructuredEvents: true, Prompt: true, CancelTurn: true}
}
func (a *interactiveAPITestAdapter) Start(_ context.Context, request agent.StartRequest) (agent.Session, error) {
	sessionID := "session-" + request.TaskID
	a.events <- domain.Event{TaskID: request.TaskID, SessionID: sessionID, Type: domain.EventAgentStarted, Timestamp: time.Now().UTC()}
	return agent.Session{ID: sessionID}, nil
}
func (a *interactiveAPITestAdapter) Events(context.Context, string) (<-chan domain.Event, error) {
	return a.events, nil
}
func (*interactiveAPITestAdapter) Prompt(context.Context, string, agent.PromptRequest) (agent.PromptResult, error) {
	return agent.PromptResult{StopReason: "end_turn"}, nil
}
func (*interactiveAPITestAdapter) Cancel(context.Context, string) error { return nil }
func (*interactiveAPITestAdapter) SetConfigOption(context.Context, string, agent.ConfigOption) error {
	return nil
}
func (*interactiveAPITestAdapter) Stop(context.Context, string) error { return nil }

func (*permissionAPITestAdapter) Name() string { return "permission-test" }
func (*permissionAPITestAdapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{StructuredEvents: true, Prompt: true, CancelTurn: true, PermissionEvents: true}
}
func (a *permissionAPITestAdapter) Start(_ context.Context, request agent.StartRequest) (agent.Session, error) {
	a.taskID = request.TaskID
	return agent.Session{ID: "session-" + request.TaskID}, nil
}
func (a *permissionAPITestAdapter) Events(context.Context, string) (<-chan domain.Event, error) {
	return a.events, nil
}
func (a *permissionAPITestAdapter) Prompt(ctx context.Context, sessionID string, _ agent.PromptRequest) (agent.PromptResult, error) {
	a.events <- domain.Event{
		TaskID: a.taskID, SessionID: sessionID, Type: domain.EventAgentPermission, Timestamp: time.Now().UTC(),
		Data: domain.AgentPermissionData{RequestID: "permission-1", Title: "allow protected work", Options: []domain.PermissionOptionData{
			{ID: "allow", Name: "Allow", Kind: "allow_once"},
			{ID: "deny", Name: "Deny", Kind: "reject_once"},
		}},
	}
	select {
	case <-a.decision:
		return agent.PromptResult{StopReason: "end_turn"}, nil
	case <-ctx.Done():
		return agent.PromptResult{}, ctx.Err()
	}
}
func (*permissionAPITestAdapter) Cancel(context.Context, string) error { return nil }
func (*permissionAPITestAdapter) SetConfigOption(context.Context, string, agent.ConfigOption) error {
	return nil
}
func (a *permissionAPITestAdapter) ResolvePermission(_ context.Context, _ string, requestID, optionID string) error {
	if requestID != "permission-1" || (optionID != "allow" && optionID != "deny") {
		return agent.ErrPermissionNotFound
	}
	a.decision <- optionID
	a.resolved <- optionID
	return nil
}
func (*permissionAPITestAdapter) Stop(context.Context, string) error { return nil }

func (unavailableAPITestAdapter) Name() string { return "unavailable" }
func (unavailableAPITestAdapter) Status() agent.Status {
	return agent.Status{Installed: false, Healthy: false, Error: "command not found"}
}

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

func waitForAPITaskStatus(t *testing.T, service *app.Service, taskID string, status domain.TaskStatus) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, err := service.GetTask(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == status {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach %s", taskID, status)
}
