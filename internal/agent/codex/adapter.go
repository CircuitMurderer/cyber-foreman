package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cyber-foreman/internal/acp"
	"cyber-foreman/internal/agent"
	"cyber-foreman/internal/domain"
)

var ErrUnknownSession = errors.New("unknown Codex session")

const appServerProtocolVersion = 2

type Config struct {
	Binary           string
	Args             []string
	Env              []string
	Provider         *ProviderConfig
	HandshakeTimeout time.Duration
	GracePeriod      time.Duration
}

type Adapter struct {
	config Config

	mu       sync.RWMutex
	sessions map[string]*session
	status   agent.Status
}

var _ agent.Adapter = (*Adapter)(nil)
var _ agent.StatusProvider = (*Adapter)(nil)

type session struct {
	id       string
	taskID   string
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	client   *acp.Client
	bridge   *responseBridge
	events   chan domain.Event
	done     chan struct{}
	model    string
	promptMu sync.Mutex

	turnMu      sync.Mutex
	activeTurn  string
	waiters     map[string]chan turnOutcome
	completions map[string]turnOutcome

	stopOnce sync.Once
	stopping atomic.Bool
	eventMu  sync.Mutex
	closed   bool
	stderr   chan struct{}
	notify   chan struct{}
}

type turnOutcome struct {
	status  string
	message string
}

type initializeResponse struct {
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
	UserAgent      string `json:"userAgent"`
}

func NewAdapter(config Config) (*Adapter, error) {
	config.Binary = strings.TrimSpace(config.Binary)
	if config.Binary == "" {
		config.Binary = "codex"
	}
	if len(config.Args) == 0 {
		config.Args = []string{"app-server"}
	}
	if config.HandshakeTimeout <= 0 {
		config.HandshakeTimeout = 15 * time.Second
	}
	if config.GracePeriod <= 0 {
		config.GracePeriod = 3 * time.Second
	}
	if config.Provider != nil {
		provider := *config.Provider
		provider.BaseURL = strings.TrimSpace(provider.BaseURL)
		provider.APIKeyEnv = strings.TrimSpace(provider.APIKeyEnv)
		provider.DefaultModel = strings.TrimSpace(provider.DefaultModel)
		provider.Protocol = ProviderProtocol(strings.TrimSpace(string(provider.Protocol)))
		if provider.Protocol == "" {
			provider.Protocol = ProtocolChatCompletions
		}
		if err := provider.validate(); err != nil {
			return nil, err
		}
		config.Provider = &provider
	}
	a := &Adapter{config: config, sessions: make(map[string]*session)}
	a.refreshExecutableStatus()
	return a, nil
}

func (a *Adapter) Name() string { return "codex" }

func (a *Adapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		StructuredEvents: true,
		ResumeSession:    true,
		Prompt:           true,
		CancelTurn:       true,
		SessionConfig:    true,
		ToolEvents:       true,
		PermissionEvents: true,
	}
}

func (a *Adapter) Status() agent.Status {
	a.mu.RLock()
	defer a.mu.RUnlock()
	status := a.status
	if status.AgentInfo != nil {
		info := *status.AgentInfo
		status.AgentInfo = &info
	}
	return status
}

// Probe checks the local login cache and performs an App Server initialize
// handshake. It never creates a thread and therefore never invokes a model.
func (a *Adapter) Probe(parent context.Context) agent.Status {
	executable, err := resolveExecutable(a.config.Binary)
	if err != nil {
		a.setUnavailable(err)
		return a.Status()
	}
	ctx, cancel := context.WithTimeout(parent, a.config.HandshakeTimeout)
	defer cancel()
	version, versionErr := a.readVersion(ctx, executable)
	if a.config.Provider == nil {
		if err := a.checkLogin(ctx, executable); err != nil {
			a.setProbeFailure(executable, version, fmt.Errorf("Codex authentication unavailable: %w", err))
			return a.Status()
		}
	} else if a.config.Provider.APIKeyEnv != "" {
		if key, ok := configuredEnv(a.config.Env, a.config.Provider.APIKeyEnv); !ok || strings.TrimSpace(key) == "" {
			a.setProbeFailure(executable, version, fmt.Errorf("Codex API key environment variable %s is not set", a.config.Provider.APIKeyEnv))
			return a.Status()
		}
	}

	probeArgs := append([]string(nil), a.config.Args...)
	if a.config.Provider != nil {
		probeArgs = append(probeArgs, providerIsolationArgs()...)
	}
	cmd := exec.CommandContext(ctx, executable, probeArgs...)
	cmd.Env = a.childEnv(nil)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		a.setProbeFailure(executable, version, err)
		return a.Status()
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		a.setProbeFailure(executable, version, err)
		return a.Status()
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		a.setProbeFailure(executable, version, err)
		return a.Status()
	}
	client := newAppServerClient(ctx, stdout, stdin, nil)
	var initialized initializeResponse
	err = client.Call(ctx, "initialize", initializeParams(), &initialized)
	if err == nil {
		err = client.Notify("initialized", nil)
	}
	_ = stdin.Close()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	client.Close(nil)
	if err != nil {
		a.setProbeFailure(executable, version, fmt.Errorf("initialize Codex App Server: %w", err))
		return a.Status()
	}
	warning := ""
	if versionErr != nil {
		warning = "version probe: " + versionErr.Error()
	}
	a.setHealthy(executable, version, warning)
	return a.Status()
}

func (a *Adapter) Start(ctx context.Context, req agent.StartRequest) (agent.Session, error) {
	executable, err := resolveExecutable(a.config.Binary)
	if err != nil {
		a.setUnavailable(err)
		return agent.Session{}, fmt.Errorf("resolve Codex command: %w", err)
	}
	cwd := req.CWD
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return agent.Session{}, fmt.Errorf("get working directory: %w", err)
		}
	}
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return agent.Session{}, fmt.Errorf("resolve working directory: %w", err)
	}

	var bridge *responseBridge
	commandArgs := append([]string(nil), a.config.Args...)
	if a.config.Provider != nil {
		apiKey := ""
		if a.config.Provider.APIKeyEnv != "" {
			var ok bool
			combinedEnv := append(append([]string(nil), a.config.Env...), req.Env...)
			apiKey, ok = configuredEnv(combinedEnv, a.config.Provider.APIKeyEnv)
			if !ok || strings.TrimSpace(apiKey) == "" {
				return agent.Session{}, fmt.Errorf("Codex API key environment variable %s is not set", a.config.Provider.APIKeyEnv)
			}
		}
		bridge, err = startResponseBridge(ctx, *a.config.Provider, strings.TrimSpace(apiKey))
		if err != nil {
			return agent.Session{}, err
		}
		commandArgs = append(commandArgs, providerArgs(bridge.BaseURL())...)
	}
	cmd := exec.CommandContext(ctx, executable, commandArgs...)
	cmd.Dir = absCWD
	cmd.Env = a.childEnv(req.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		if bridge != nil {
			bridge.Close()
		}
		return agent.Session{}, fmt.Errorf("Codex stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		if bridge != nil {
			bridge.Close()
		}
		return agent.Session{}, fmt.Errorf("Codex stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		if bridge != nil {
			bridge.Close()
		}
		return agent.Session{}, fmt.Errorf("Codex stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		if bridge != nil {
			bridge.Close()
		}
		a.setProbeFailure(executable, "", err)
		return agent.Session{}, fmt.Errorf("start Codex App Server: %w", err)
	}

	s := &session{
		taskID: req.TaskID, cmd: cmd, stdin: stdin, bridge: bridge,
		events: make(chan domain.Event, 512), done: make(chan struct{}),
		waiters: make(map[string]chan turnOutcome), completions: make(map[string]turnOutcome),
		stderr: make(chan struct{}), notify: make(chan struct{}),
	}
	if a.config.Provider != nil {
		s.model = a.config.Provider.DefaultModel
	}
	s.client = newAppServerClient(ctx, stdout, stdin, s.handleRequest)
	go a.consumeStderr(s, stderr)
	go a.consumeNotifications(s)
	go a.waitProcess(s)

	handshakeCtx, cancel := context.WithTimeout(ctx, a.config.HandshakeTimeout)
	defer cancel()
	var initialized initializeResponse
	if err := s.client.Call(handshakeCtx, "initialize", initializeParams(), &initialized); err != nil {
		a.abortStart(s)
		a.setProbeFailure(executable, "", err)
		return agent.Session{}, fmt.Errorf("initialize Codex App Server: %w", err)
	}
	if err := s.client.Notify("initialized", nil); err != nil {
		a.abortStart(s)
		return agent.Session{}, fmt.Errorf("acknowledge Codex initialization: %w", err)
	}
	var created struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	threadParams := map[string]any{
		"cwd": absCWD, "approvalPolicy": "never", "sandbox": "workspace-write",
		"ephemeral": true, "serviceName": "cyber-foreman",
	}
	if a.config.Provider != nil {
		threadParams["model"] = a.config.Provider.DefaultModel
		threadParams["modelProvider"] = providerID
	}
	if err := s.client.Call(handshakeCtx, "thread/start", threadParams, &created); err != nil {
		a.abortStart(s)
		return agent.Session{}, fmt.Errorf("create Codex thread: %w", err)
	}
	if created.Thread.ID == "" {
		a.abortStart(s)
		return agent.Session{}, errors.New("Codex returned an empty thread id")
	}
	s.id = created.Thread.ID
	a.mu.Lock()
	a.sessions[s.id] = s
	a.mu.Unlock()
	a.setHealthy(executable, a.Status().Version, "")
	startedData := map[string]any{
		"adapter": "codex", "pid": cmd.Process.Pid, "protocol": "codex-app-server",
		"protocol_version": appServerProtocolVersion, "user_agent": initialized.UserAgent,
	}
	if a.config.Provider != nil {
		startedData["provider_mode"] = string(a.config.Provider.Protocol)
		startedData["model"] = a.config.Provider.DefaultModel
	}
	s.emit(domain.Event{TaskID: req.TaskID, SessionID: s.id, Type: domain.EventAgentStarted, Timestamp: time.Now().UTC(), Data: startedData})
	return agent.Session{ID: s.id}, nil
}

const providerID = "cyber_foreman"

func providerArgs(baseURL string) []string {
	arguments := []string{
		"-c", "model_provider=" + strconv.Quote(providerID),
		"-c", "model_providers." + providerID + ".name=" + strconv.Quote("Cyber Foreman API Bridge"),
		"-c", "model_providers." + providerID + ".base_url=" + strconv.Quote(baseURL),
		"-c", "model_providers." + providerID + ".wire_api=" + strconv.Quote("responses"),
		"-c", "model_providers." + providerID + ".requires_openai_auth=false",
	}
	return append(arguments, providerIsolationArgs()...)
}

func providerIsolationArgs() []string {
	return []string{
		"-c", "analytics.enabled=false",
		"-c", "features.remote_plugin=false",
		"-c", "features.plugins=false",
	}
}

func initializeParams() map[string]any {
	return map[string]any{
		"clientInfo":   map[string]string{"name": "cyber-foreman", "title": "Cyber Foreman", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": false},
	}
}

func newAppServerClient(ctx context.Context, reader io.Reader, writer io.Writer, handler acp.RequestHandler) *acp.Client {
	return acp.NewClientWithOptions(ctx, reader, writer, handler, acp.ClientOptions{
		OmitJSONRPC: true, AcceptMissingJSONRPC: true,
	})
}

func (a *Adapter) Events(_ context.Context, sessionID string) (<-chan domain.Event, error) {
	s, err := a.get(sessionID)
	if err != nil {
		return nil, err
	}
	return s.events, nil
}

func (a *Adapter) Prompt(ctx context.Context, sessionID string, req agent.PromptRequest) (agent.PromptResult, error) {
	s, err := a.get(sessionID)
	if err != nil {
		return agent.PromptResult{}, err
	}
	if strings.TrimSpace(req.Text) == "" {
		return agent.PromptResult{}, errors.New("prompt text is required")
	}
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	params := map[string]any{
		"threadId": sessionID,
		"input":    []map[string]string{{"type": "text", "text": req.Text}},
	}
	s.turnMu.Lock()
	model := s.model
	s.turnMu.Unlock()
	if model != "" {
		params["model"] = model
	}
	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := s.client.Call(ctx, "turn/start", params, &started); err != nil {
		return agent.PromptResult{}, fmt.Errorf("start Codex turn: %w", err)
	}
	if started.Turn.ID == "" {
		return agent.PromptResult{}, errors.New("Codex returned an empty turn id")
	}
	waiter, completed := s.registerTurn(started.Turn.ID)
	if completed != nil {
		return promptResult(*completed)
	}
	select {
	case outcome := <-waiter:
		return promptResult(outcome)
	case <-ctx.Done():
		_ = a.Cancel(context.Background(), sessionID)
		return agent.PromptResult{}, ctx.Err()
	case <-s.client.Closed():
		return agent.PromptResult{}, s.client.Err()
	}
}

func promptResult(outcome turnOutcome) (agent.PromptResult, error) {
	switch outcome.status {
	case "completed":
		return agent.PromptResult{StopReason: "end_turn"}, nil
	case "interrupted":
		return agent.PromptResult{StopReason: "cancelled"}, nil
	case "failed":
		if outcome.message == "" {
			outcome.message = "Codex turn failed"
		}
		return agent.PromptResult{}, errors.New(outcome.message)
	default:
		return agent.PromptResult{}, fmt.Errorf("Codex turn ended with unknown status %q", outcome.status)
	}
}

func (a *Adapter) Cancel(ctx context.Context, sessionID string) error {
	s, err := a.get(sessionID)
	if err != nil {
		return err
	}
	turnID := s.currentTurn()
	if turnID == "" {
		return nil
	}
	var result struct{}
	return s.client.Call(ctx, "turn/interrupt", map[string]string{"threadId": sessionID, "turnId": turnID}, &result)
}

func (a *Adapter) SetConfigOption(_ context.Context, sessionID string, option agent.ConfigOption) error {
	s, err := a.get(sessionID)
	if err != nil {
		return err
	}
	if option.ID != "model" {
		return fmt.Errorf("%w: Codex config option %q", agent.ErrUnsupported, option.ID)
	}
	model, ok := option.Value.(string)
	if !ok {
		return errors.New("Codex model must be a string")
	}
	s.turnMu.Lock()
	s.model = strings.TrimSpace(model)
	s.turnMu.Unlock()
	return nil
}

func (a *Adapter) Stop(ctx context.Context, sessionID string) error {
	s, err := a.get(sessionID)
	if err != nil {
		return err
	}
	defer a.remove(sessionID, s)
	s.stopping.Store(true)
	s.stopOnce.Do(func() {
		cancelCtx, cancel := context.WithTimeout(context.Background(), a.config.GracePeriod)
		defer cancel()
		_ = a.Cancel(cancelCtx, sessionID)
		_ = s.stdin.Close()
	})
	timer := time.NewTimer(a.config.GracePeriod)
	defer timer.Stop()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		_ = s.cmd.Process.Kill()
		return ctx.Err()
	case <-timer.C:
		if err := s.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		select {
		case <-s.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (a *Adapter) consumeNotifications(s *session) {
	defer close(s.notify)
	for {
		select {
		case notification := <-s.client.Notifications():
			a.handleNotification(s, notification)
		case <-s.client.Closed():
			return
		}
	}
}

func (a *Adapter) handleNotification(s *session, notification acp.Notification) {
	if notification.Method != "turn/completed" {
		var envelope struct {
			TurnID string `json:"turnId"`
			Turn   struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &envelope) == nil {
			if envelope.TurnID != "" {
				s.setActiveTurn(envelope.TurnID)
			} else if envelope.Turn.ID != "" {
				s.setActiveTurn(envelope.Turn.ID)
			}
		}
	}
	switch notification.Method {
	case "turn/started":
		var params struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &params) == nil && params.Turn.ID != "" {
			s.setActiveTurn(params.Turn.ID)
		}
	case "item/agentMessage/delta":
		a.emitTextUpdate(s, "agent_message_chunk", notification.Params)
	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta", "item/plan/delta":
		a.emitTextUpdate(s, "agent_thought_chunk", notification.Params)
	case "item/started":
		a.emitItemUpdate(s, "tool_call", notification.Params)
	case "item/completed":
		a.emitItemUpdate(s, "tool_call_update", notification.Params)
	case "item/commandExecution/outputDelta", "item/fileChange/outputDelta":
		a.emitRawUpdate(s, "tool_call_update", notification.Params)
	case "turn/plan/updated":
		a.emitRawUpdate(s, "agent_thought_chunk", notification.Params)
	case "turn/completed":
		var params struct {
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			message := ""
			if params.Turn.Error != nil {
				message = params.Turn.Error.Message
			}
			s.completeTurn(params.Turn.ID, turnOutcome{status: params.Turn.Status, message: message})
		}
	case "error":
		s.emit(domain.Event{TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentError, Timestamp: time.Now().UTC(), Data: json.RawMessage(notification.Params)})
	case "warning", "configWarning":
		s.emit(domain.Event{TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentStderr, Timestamp: time.Now().UTC(), Data: domain.AgentStderrData{Line: string(notification.Params)}})
	}
}

func (a *Adapter) emitItemUpdate(s *session, kind string, raw json.RawMessage) {
	var params struct {
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	if json.Unmarshal(raw, &params) != nil || !isToolItem(params.Item.Type) {
		return
	}
	a.emitRawUpdate(s, kind, raw)
}

func isToolItem(itemType string) bool {
	switch itemType {
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "webSearch",
		"imageGeneration", "imageView", "collabAgentToolCall", "skill":
		return true
	default:
		return false
	}
}

func (a *Adapter) emitTextUpdate(s *session, kind string, raw json.RawMessage) {
	var params struct {
		Delta string `json:"delta"`
	}
	if json.Unmarshal(raw, &params) != nil || params.Delta == "" {
		return
	}
	a.emitUpdate(s, map[string]any{
		"sessionUpdate": kind,
		"content":       map[string]string{"type": "text", "text": params.Delta},
		"raw":           json.RawMessage(raw),
	})
}

func (a *Adapter) emitRawUpdate(s *session, kind string, raw json.RawMessage) {
	a.emitUpdate(s, map[string]any{"sessionUpdate": kind, "raw": json.RawMessage(raw)})
}

func (a *Adapter) emitUpdate(s *session, update map[string]any) {
	payload, err := json.Marshal(update)
	if err != nil {
		return
	}
	s.emit(domain.Event{
		TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentSessionUpdate,
		Timestamp: time.Now().UTC(), Data: domain.AgentSessionUpdateData{Update: payload},
	})
}

func (s *session) handleRequest(_ context.Context, method string, params json.RawMessage) (any, *acp.RPCError) {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		s.emit(domain.Event{
			TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentPermission,
			Timestamp: time.Now().UTC(), Data: domain.AgentPermissionData{Options: []domain.PermissionOptionData{
				{ID: "decline", Name: "Decline", Kind: "reject_once"},
			}},
		})
		return map[string]string{"decision": "decline"}, nil
	case "item/permissions/requestApproval":
		s.emit(domain.Event{
			TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentPermission,
			Timestamp: time.Now().UTC(), Data: map[string]any{"options": []any{}, "request": json.RawMessage(params)},
		})
		return map[string]any{"permissions": map[string]any{}, "scope": "turn"}, nil
	case "item/tool/requestUserInput":
		s.emit(domain.Event{
			TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentPermission,
			Timestamp: time.Now().UTC(), Data: map[string]any{"options": []any{}, "request": json.RawMessage(params)},
		})
		return map[string]any{"answers": map[string]any{}}, nil
	case "mcpServer/elicitation/request":
		s.emit(domain.Event{
			TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentPermission,
			Timestamp: time.Now().UTC(), Data: map[string]any{"options": []any{}, "request": json.RawMessage(params)},
		})
		return map[string]string{"action": "decline"}, nil
	default:
		return nil, &acp.RPCError{Code: -32601, Message: "method not supported"}
	}
}

func (a *Adapter) consumeStderr(s *session, stderr io.Reader) {
	defer close(s.stderr)
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		s.emit(domain.Event{TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentStderr, Timestamp: time.Now().UTC(), Data: domain.AgentStderrData{Line: scanner.Text()}})
	}
}

func (a *Adapter) waitProcess(s *session) {
	err := s.cmd.Wait()
	<-s.stderr
	s.client.Close(err)
	if s.bridge != nil {
		s.bridge.Close()
	}
	<-s.notify
	disconnectError := ""
	if err != nil && !s.stopping.Load() {
		disconnectError = err.Error()
	}
	s.emit(domain.Event{TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentDisconnected, Timestamp: time.Now().UTC(), Data: domain.AgentDisconnectedData{Error: disconnectError}})
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	s.emit(domain.Event{TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentExited, Timestamp: time.Now().UTC(), Data: domain.AgentExitData{ExitCode: exitCode, Error: disconnectError}})
	close(s.done)
	s.closeEvents()
}

func (a *Adapter) readVersion(ctx context.Context, executable string) (string, error) {
	cmd := exec.CommandContext(ctx, executable, "--version")
	cmd.Env = a.childEnv(nil)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "codex-cli") {
			return line, err
		}
	}
	return strings.TrimSpace(output.String()), err
}

func (a *Adapter) checkLogin(ctx context.Context, executable string) error {
	cmd := exec.CommandContext(ctx, executable, "login", "status")
	cmd.Env = a.childEnv(nil)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		if message == "" {
			message = err.Error()
		}
		return errors.New(message)
	}
	return nil
}

func configuredEnv(overrides []string, name string) (string, bool) {
	prefix := name + "="
	for index := len(overrides) - 1; index >= 0; index-- {
		if strings.HasPrefix(overrides[index], prefix) {
			return strings.TrimPrefix(overrides[index], prefix), true
		}
	}
	return os.LookupEnv(name)
}

func (a *Adapter) childEnv(extra []string) []string {
	environment := append(append(append([]string(nil), os.Environ()...), a.config.Env...), extra...)
	if a.config.Provider == nil || a.config.Provider.APIKeyEnv == "" {
		return environment
	}
	return withoutEnv(environment, a.config.Provider.APIKeyEnv)
}

func withoutEnv(environment []string, name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func resolveExecutable(command string) (string, error) {
	if strings.ContainsAny(command, `/\\`) {
		absolute, err := filepath.Abs(command)
		if err != nil {
			return "", err
		}
		return exec.LookPath(absolute)
	}
	return exec.LookPath(command)
}

func (a *Adapter) refreshExecutableStatus() {
	executable, err := resolveExecutable(a.config.Binary)
	if err != nil {
		a.setUnavailable(err)
		return
	}
	a.mu.Lock()
	a.status = agent.Status{Installed: true, Healthy: true, Command: executable}
	a.mu.Unlock()
}

func (a *Adapter) setUnavailable(err error) {
	a.mu.Lock()
	a.status = agent.Status{Installed: false, Healthy: false, Command: a.config.Binary, Error: err.Error()}
	a.mu.Unlock()
}

func (a *Adapter) setProbeFailure(executable, version string, err error) {
	a.mu.Lock()
	a.status = agent.Status{Installed: true, Healthy: false, Command: executable, Version: version, Error: err.Error()}
	a.mu.Unlock()
}

func (a *Adapter) setHealthy(executable, version, warning string) {
	info := agent.ImplementationInfo{Name: "codex", Title: "Codex App Server", Version: version}
	a.mu.Lock()
	a.status = agent.Status{
		Installed: true, Healthy: true, Command: executable, Version: version,
		ProtocolVersion: appServerProtocolVersion, AgentInfo: &info, Error: warning,
	}
	a.mu.Unlock()
}

func (a *Adapter) get(sessionID string) (*session, error) {
	a.mu.RLock()
	s, ok := a.sessions[sessionID]
	a.mu.RUnlock()
	if !ok {
		return nil, ErrUnknownSession
	}
	return s, nil
}

func (a *Adapter) remove(sessionID string, expected *session) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessions[sessionID] == expected {
		delete(a.sessions, sessionID)
	}
}

func (a *Adapter) abortStart(s *session) {
	s.stopping.Store(true)
	if s.bridge != nil {
		s.bridge.Close()
	}
	_ = s.stdin.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

func (s *session) registerTurn(turnID string) (<-chan turnOutcome, *turnOutcome) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if outcome, ok := s.completions[turnID]; ok {
		delete(s.completions, turnID)
		return nil, &outcome
	}
	waiter := make(chan turnOutcome, 1)
	s.waiters[turnID] = waiter
	s.activeTurn = turnID
	return waiter, nil
}

func (s *session) completeTurn(turnID string, outcome turnOutcome) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if s.activeTurn == turnID {
		s.activeTurn = ""
	}
	if waiter, ok := s.waiters[turnID]; ok {
		delete(s.waiters, turnID)
		waiter <- outcome
		return
	}
	s.completions[turnID] = outcome
}

func (s *session) currentTurn() string {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	return s.activeTurn
}

func (s *session) setActiveTurn(turnID string) {
	s.turnMu.Lock()
	s.activeTurn = turnID
	s.turnMu.Unlock()
}

func (s *session) emit(event domain.Event) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.events <- event:
	default:
	}
}

func (s *session) closeEvents() {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.events)
	}
}
