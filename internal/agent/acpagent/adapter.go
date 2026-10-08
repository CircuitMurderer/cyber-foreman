package acpagent

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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cyber-foreman/internal/acp"
	"cyber-foreman/internal/agent"
	"cyber-foreman/internal/domain"
)

var ErrUnknownSession = errors.New("unknown ACP agent session")

const protocolVersion = 1

type Config struct {
	Profile          Profile
	Env              []string
	HandshakeTimeout time.Duration
	GracePeriod      time.Duration
}

type Adapter struct {
	config Config

	mu       sync.RWMutex
	sessions map[string]*session
	status   agent.Status
	caps     agent.Capabilities
}

var _ agent.Adapter = (*Adapter)(nil)
var _ agent.StatusProvider = (*Adapter)(nil)

type session struct {
	id     string
	taskID string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	client *acp.Client
	events chan domain.Event
	done   chan struct{}

	promptMu sync.Mutex
	stopOnce sync.Once
	stopping atomic.Bool

	eventMu       sync.Mutex
	eventsClosed  bool
	stderrDone    chan struct{}
	notifyDone    chan struct{}
	permissionSeq atomic.Uint64
	permissionMu  sync.Mutex
	permissions   map[string]*pendingPermission
}

type pendingPermission struct {
	options  map[string]bool
	decision chan string
}

type initializeResponse struct {
	ProtocolVersion   int                      `json:"protocolVersion"`
	AgentCapabilities json.RawMessage          `json:"agentCapabilities"`
	AgentInfo         agent.ImplementationInfo `json:"agentInfo"`
	AuthMethods       []authMethod             `json:"authMethods"`
}

type authMethod struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type newSessionResponse struct {
	SessionID     string          `json:"sessionId"`
	ConfigOptions json.RawMessage `json:"configOptions"`
}

func NewAdapter(config Config) (*Adapter, error) {
	if err := config.Profile.Validate(); err != nil {
		return nil, err
	}
	config.Profile.Name = strings.TrimSpace(config.Profile.Name)
	config.Profile.Command = strings.TrimSpace(config.Profile.Command)
	if config.HandshakeTimeout <= 0 {
		config.HandshakeTimeout = 15 * time.Second
	}
	if config.GracePeriod <= 0 {
		config.GracePeriod = 3 * time.Second
	}
	adapter := &Adapter{
		config: config, sessions: make(map[string]*session),
		caps: agent.Capabilities{
			StructuredEvents: true, Prompt: true, CancelTurn: true, SessionConfig: true,
			ToolEvents: true, PermissionEvents: true,
		},
	}
	adapter.refreshExecutableStatus()
	return adapter, nil
}

func (a *Adapter) Name() string { return a.config.Profile.Name }

func (a *Adapter) Capabilities() agent.Capabilities {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.caps
}

func (a *Adapter) Status() agent.Status {
	a.mu.RLock()
	defer a.mu.RUnlock()
	result := a.status
	if result.AgentInfo != nil {
		info := *result.AgentInfo
		result.AgentInfo = &info
	}
	return result
}

// Probe validates that the command speaks compatible ACP without creating a
// session or invoking a model.
func (a *Adapter) Probe(parent context.Context) agent.Status {
	executable, err := resolveExecutable(a.config.Profile.Command)
	if err != nil {
		a.setUnavailable(err)
		return a.Status()
	}

	ctx, cancel := context.WithTimeout(parent, a.config.HandshakeTimeout)
	defer cancel()
	version, versionErr := a.readVersion(ctx, executable)
	cmd := exec.CommandContext(ctx, executable, a.config.Profile.Args...)
	cmd.Env = append(os.Environ(), a.config.Env...)
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
	client := acp.NewClient(ctx, stdout, stdin, nil)
	var initialized initializeResponse
	err = client.Call(ctx, "initialize", initializeParams(), &initialized)
	_ = stdin.Close()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	client.Close(nil)
	if err != nil {
		a.setProbeFailure(executable, version, fmt.Errorf("initialize ACP: %w", err))
		return a.Status()
	}
	if initialized.ProtocolVersion != protocolVersion {
		a.setProbeFailure(executable, version, fmt.Errorf("unsupported ACP protocol version %d", initialized.ProtocolVersion))
		return a.Status()
	}
	warning := ""
	if versionErr != nil {
		warning = "version probe: " + versionErr.Error()
	}
	a.setHealthy(executable, version, initialized, warning)
	return a.Status()
}

func (a *Adapter) Start(ctx context.Context, req agent.StartRequest) (agent.Session, error) {
	executable, err := resolveExecutable(a.config.Profile.Command)
	if err != nil {
		a.setUnavailable(err)
		return agent.Session{}, fmt.Errorf("resolve %s command: %w", a.Name(), err)
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

	cmd := exec.CommandContext(ctx, executable, a.config.Profile.Args...)
	cmd.Dir = absCWD
	cmd.Env = append(append(os.Environ(), a.config.Env...), req.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return agent.Session{}, fmt.Errorf("%s stdin: %w", a.Name(), err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return agent.Session{}, fmt.Errorf("%s stdout: %w", a.Name(), err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return agent.Session{}, fmt.Errorf("%s stderr: %w", a.Name(), err)
	}
	if err := cmd.Start(); err != nil {
		a.setProbeFailure(executable, "", err)
		return agent.Session{}, fmt.Errorf("start %s ACP: %w", a.Name(), err)
	}

	s := &session{
		taskID: req.TaskID, cmd: cmd, stdin: stdin,
		events: make(chan domain.Event, 512), done: make(chan struct{}),
		stderrDone: make(chan struct{}), notifyDone: make(chan struct{}),
		permissions: make(map[string]*pendingPermission),
	}
	s.client = acp.NewClient(ctx, stdout, stdin, a.handleRequest)
	go a.consumeStderr(s, stderr)
	go a.consumeNotifications(s)
	go a.waitProcess(s)

	handshakeCtx, cancel := context.WithTimeout(ctx, a.config.HandshakeTimeout)
	defer cancel()
	var initialized initializeResponse
	if err := s.client.Call(handshakeCtx, "initialize", initializeParams(), &initialized); err != nil {
		a.abortStart(s)
		a.setProbeFailure(executable, "", err)
		return agent.Session{}, fmt.Errorf("initialize %s ACP: %w", a.Name(), err)
	}
	if initialized.ProtocolVersion != protocolVersion {
		a.abortStart(s)
		err := fmt.Errorf("unsupported ACP protocol version %d", initialized.ProtocolVersion)
		a.setProbeFailure(executable, "", err)
		return agent.Session{}, err
	}
	authErr := a.authenticate(handshakeCtx, s.client, initialized.AuthMethods)
	if authErr != nil && !a.config.Profile.AuthOptional {
		a.abortStart(s)
		a.setProbeFailure(executable, "", authErr)
		return agent.Session{}, fmt.Errorf("authenticate %s ACP: %w", a.Name(), authErr)
	}

	var created newSessionResponse
	if err := s.client.Call(handshakeCtx, "session/new", map[string]any{
		"cwd": absCWD, "mcpServers": []any{},
	}, &created); err != nil {
		a.abortStart(s)
		a.setProbeFailure(executable, "", err)
		return agent.Session{}, fmt.Errorf("create %s ACP session: %w", a.Name(), err)
	}
	if created.SessionID == "" {
		a.abortStart(s)
		return agent.Session{}, fmt.Errorf("%s returned an empty session id", a.Name())
	}
	s.id = created.SessionID
	a.mu.Lock()
	a.sessions[s.id] = s
	a.mu.Unlock()
	warning := ""
	if authErr != nil {
		warning = "ACP authentication skipped after configured methods failed: " + authErr.Error()
	}
	a.setHealthy(executable, a.Status().Version, initialized, warning)
	startedData := map[string]any{
		"adapter": a.Name(), "pid": cmd.Process.Pid,
		"protocol_version": initialized.ProtocolVersion,
		"agent_info":       initialized.AgentInfo, "config_options": created.ConfigOptions,
	}
	if authErr != nil {
		startedData["authentication_warning"] = authErr.Error()
	}
	s.emit(domain.Event{
		TaskID: req.TaskID, SessionID: s.id, Type: domain.EventAgentStarted,
		Timestamp: time.Now().UTC(), Data: startedData,
	})
	return agent.Session{ID: s.id}, nil
}

func initializeParams() map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false, "auth": map[string]bool{"terminal": false},
		},
		"clientInfo": map[string]string{
			"name": "cyber-foreman", "title": "Cyber Foreman", "version": "0.1.0",
		},
	}
}

func (a *Adapter) authenticate(ctx context.Context, client *acp.Client, advertised []authMethod) error {
	if len(advertised) == 0 || len(a.config.Profile.AuthMethods) == 0 {
		return nil
	}
	available := make(map[string]struct{}, len(advertised))
	for _, method := range advertised {
		available[method.ID] = struct{}{}
	}
	matched := false
	failures := make([]string, 0, len(a.config.Profile.AuthMethods))
	for _, preferred := range a.config.Profile.AuthMethods {
		if _, ok := available[preferred]; !ok {
			continue
		}
		matched = true
		err := client.Call(ctx, "authenticate", map[string]any{
			"methodId": preferred, "_meta": map[string]bool{"headless": true},
		}, &struct{}{})
		if err == nil {
			return nil
		}
		failures = append(failures, preferred+": "+err.Error())
	}
	ids := make([]string, 0, len(advertised))
	for _, method := range advertised {
		ids = append(ids, method.ID)
	}
	if !matched {
		return fmt.Errorf("agent requires authentication; advertised methods %q do not match configured methods %q", ids, a.config.Profile.AuthMethods)
	}
	return fmt.Errorf("configured ACP authentication methods failed: %s", strings.Join(failures, "; "))
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
	var result struct {
		StopReason string `json:"stopReason"`
	}
	err = s.client.Call(ctx, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    []map[string]string{{"type": "text", "text": req.Text}},
	}, &result)
	if err != nil {
		if ctx.Err() != nil {
			_ = s.client.Notify("session/cancel", map[string]string{"sessionId": sessionID})
		}
		return agent.PromptResult{}, err
	}
	return agent.PromptResult{StopReason: result.StopReason}, nil
}

func (a *Adapter) Cancel(_ context.Context, sessionID string) error {
	s, err := a.get(sessionID)
	if err != nil {
		return err
	}
	return s.client.Notify("session/cancel", map[string]string{"sessionId": sessionID})
}

func (a *Adapter) SetConfigOption(ctx context.Context, sessionID string, option agent.ConfigOption) error {
	s, err := a.get(sessionID)
	if err != nil {
		return err
	}
	params := map[string]any{"sessionId": sessionID, "configId": option.ID, "value": option.Value}
	if _, ok := option.Value.(bool); ok {
		params["type"] = "boolean"
	} else if _, ok := option.Value.(string); !ok {
		return errors.New("ACP session config value must be a string or boolean")
	}
	var response struct {
		ConfigOptions json.RawMessage `json:"configOptions"`
	}
	return s.client.Call(ctx, "session/set_config_option", params, &response)
}

func (a *Adapter) Stop(ctx context.Context, sessionID string) error {
	s, err := a.get(sessionID)
	if err != nil {
		return err
	}
	defer a.remove(sessionID, s)
	s.stopping.Store(true)
	s.stopOnce.Do(func() {
		_ = s.client.Notify("session/cancel", map[string]string{"sessionId": sessionID})
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
	defer close(s.notifyDone)
	for {
		select {
		case notification := <-s.client.Notifications():
			if notification.Method != "session/update" {
				continue
			}
			var params struct {
				SessionID string          `json:"sessionId"`
				Update    json.RawMessage `json:"update"`
			}
			if err := json.Unmarshal(notification.Params, &params); err != nil {
				s.emit(domain.Event{
					TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentError,
					Timestamp: time.Now().UTC(), Data: map[string]string{"error": "invalid session/update"},
				})
				continue
			}
			s.emit(domain.Event{
				TaskID: s.taskID, SessionID: params.SessionID, Type: domain.EventAgentSessionUpdate,
				Timestamp: time.Now().UTC(), Data: domain.AgentSessionUpdateData{Update: params.Update},
			})
		case <-s.client.Closed():
			return
		}
	}
}

func (a *Adapter) consumeStderr(s *session, stderr io.Reader) {
	defer close(s.stderrDone)
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		s.emit(domain.Event{
			TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentStderr,
			Timestamp: time.Now().UTC(), Data: domain.AgentStderrData{Line: scanner.Text()},
		})
	}
}

func (a *Adapter) waitProcess(s *session) {
	err := s.cmd.Wait()
	<-s.stderrDone
	s.client.Close(err)
	<-s.notifyDone
	disconnectError := ""
	if err != nil && !s.stopping.Load() {
		disconnectError = err.Error()
	}
	s.emit(domain.Event{
		TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentDisconnected,
		Timestamp: time.Now().UTC(), Data: domain.AgentDisconnectedData{Error: disconnectError},
	})
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	s.emit(domain.Event{
		TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentExited,
		Timestamp: time.Now().UTC(), Data: domain.AgentExitData{ExitCode: exitCode, Error: disconnectError},
	})
	close(s.done)
	s.closeEvents()
}

func (a *Adapter) handleRequest(ctx context.Context, method string, params json.RawMessage) (any, *acp.RPCError) {
	if method != "session/request_permission" {
		return nil, &acp.RPCError{Code: -32601, Message: "method not supported"}
	}
	var request struct {
		SessionID string `json:"sessionId"`
		Options   []struct {
			ID   string `json:"optionId"`
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, &acp.RPCError{Code: -32602, Message: "invalid permission request"}
	}
	s, err := a.get(request.SessionID)
	if err != nil {
		return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}, nil
	}
	requestID := fmt.Sprintf("permission-%d", s.permissionSeq.Add(1))
	pending := &pendingPermission{options: make(map[string]bool, len(request.Options)), decision: make(chan string, 1)}
	options := make([]domain.PermissionOptionData, 0, len(request.Options))
	for _, option := range request.Options {
		if strings.TrimSpace(option.ID) == "" {
			continue
		}
		pending.options[option.ID] = true
		options = append(options, domain.PermissionOptionData{ID: option.ID, Name: option.Name, Kind: option.Kind})
	}
	if len(options) == 0 {
		return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}, nil
	}
	s.permissionMu.Lock()
	s.permissions[requestID] = pending
	s.permissionMu.Unlock()
	defer func() {
		s.permissionMu.Lock()
		delete(s.permissions, requestID)
		s.permissionMu.Unlock()
	}()
	s.emit(domain.Event{
		TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentPermission,
		Timestamp: time.Now().UTC(), Data: domain.AgentPermissionData{
			RequestID: requestID, Title: "Agent 请求执行权限", Options: options,
		},
	})
	select {
	case optionID := <-pending.decision:
		return map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": optionID}}, nil
	case <-ctx.Done():
		return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}, nil
	case <-s.done:
		return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}, nil
	}
}

func (a *Adapter) ResolvePermission(ctx context.Context, sessionID, requestID, optionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := a.get(sessionID)
	if err != nil {
		return err
	}
	s.permissionMu.Lock()
	pending := s.permissions[requestID]
	if pending == nil || !pending.options[optionID] {
		s.permissionMu.Unlock()
		return agent.ErrPermissionNotFound
	}
	if err := ctx.Err(); err != nil {
		s.permissionMu.Unlock()
		return err
	}
	delete(s.permissions, requestID)
	s.permissionMu.Unlock()
	select {
	case pending.decision <- optionID:
		return nil
	case <-s.done:
		return ErrUnknownSession
	}
}

func (a *Adapter) readVersion(ctx context.Context, executable string) (string, error) {
	if len(a.config.Profile.VersionArgs) == 0 {
		return "", nil
	}
	cmd := exec.CommandContext(ctx, executable, a.config.Profile.VersionArgs...)
	cmd.Env = append(os.Environ(), a.config.Env...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	version := strings.TrimSpace(output.String())
	if newline := strings.IndexByte(version, '\n'); newline >= 0 {
		version = strings.TrimSpace(version[:newline])
	}
	return version, err
}

func (a *Adapter) refreshExecutableStatus() {
	executable, err := resolveExecutable(a.config.Profile.Command)
	if err != nil {
		a.setUnavailable(err)
		return
	}
	a.mu.Lock()
	a.status = agent.Status{Installed: true, Healthy: true, Command: executable}
	a.mu.Unlock()
}

func resolveExecutable(command string) (string, error) {
	if strings.ContainsAny(command, `/\\`) {
		absolute, err := filepath.Abs(command)
		if err != nil {
			return "", err
		}
		resolved, err := exec.LookPath(absolute)
		if err != nil {
			return "", err
		}
		return resolved, nil
	}
	return exec.LookPath(command)
}

func (a *Adapter) setUnavailable(err error) {
	a.mu.Lock()
	a.status = agent.Status{Installed: false, Healthy: false, Command: a.config.Profile.Command, Error: err.Error()}
	a.mu.Unlock()
}

func (a *Adapter) setProbeFailure(executable, version string, err error) {
	a.mu.Lock()
	a.status = agent.Status{Installed: true, Healthy: false, Command: executable, Version: version, Error: err.Error()}
	a.mu.Unlock()
}

func (a *Adapter) setHealthy(executable, version string, initialized initializeResponse, warning string) {
	info := initialized.AgentInfo
	a.mu.Lock()
	a.status = agent.Status{
		Installed: true, Healthy: true, Command: executable, Version: version,
		ProtocolVersion: initialized.ProtocolVersion, AgentInfo: &info, Error: warning,
	}
	var capabilities struct {
		LoadSession         bool `json:"loadSession"`
		SessionCapabilities struct {
			Resume json.RawMessage `json:"resume"`
		} `json:"sessionCapabilities"`
	}
	if json.Unmarshal(initialized.AgentCapabilities, &capabilities) == nil {
		a.caps.ResumeSession = capabilities.LoadSession || len(capabilities.SessionCapabilities.Resume) > 0
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
	_ = s.stdin.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

func (s *session) emit(event domain.Event) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if s.eventsClosed {
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
	if s.eventsClosed {
		return
	}
	s.eventsClosed = true
	close(s.events)
}
