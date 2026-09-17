package opencode

import (
	"bufio"
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

var ErrUnknownSession = errors.New("unknown OpenCode session")

const protocolVersion = 1

type Config struct {
	Binary           string
	Args             []string
	Env              []string
	HandshakeTimeout time.Duration
	GracePeriod      time.Duration
}

type Adapter struct {
	config   Config
	mu       sync.RWMutex
	sessions map[string]*session
}

var _ agent.Adapter = (*Adapter)(nil)

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

	eventMu      sync.Mutex
	eventsClosed bool
	stderrDone   chan struct{}
	notifyDone   chan struct{}
}

type initializeResponse struct {
	ProtocolVersion   int             `json:"protocolVersion"`
	AgentCapabilities json.RawMessage `json:"agentCapabilities"`
	AgentInfo         json.RawMessage `json:"agentInfo"`
}

type newSessionResponse struct {
	SessionID     string          `json:"sessionId"`
	ConfigOptions json.RawMessage `json:"configOptions"`
}

func NewAdapter(config Config) (*Adapter, error) {
	if strings.TrimSpace(config.Binary) == "" {
		return nil, errors.New("OpenCode binary path is required")
	}
	binary, err := filepath.Abs(config.Binary)
	if err != nil {
		return nil, fmt.Errorf("resolve OpenCode binary: %w", err)
	}
	config.Binary = binary
	if config.HandshakeTimeout <= 0 {
		config.HandshakeTimeout = 15 * time.Second
	}
	if config.GracePeriod <= 0 {
		config.GracePeriod = 3 * time.Second
	}
	return &Adapter{config: config, sessions: make(map[string]*session)}, nil
}

func (a *Adapter) Name() string { return "opencode" }

func (a *Adapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		StructuredEvents: true,
		Prompt:           true,
		CancelTurn:       true,
		SessionConfig:    true,
		ToolEvents:       true,
		PermissionEvents: true,
	}
}

func (a *Adapter) Start(ctx context.Context, req agent.StartRequest) (agent.Session, error) {
	cwd := req.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return agent.Session{}, fmt.Errorf("get working directory: %w", err)
		}
	}
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return agent.Session{}, fmt.Errorf("resolve working directory: %w", err)
	}

	args := append(append([]string(nil), a.config.Args...), "acp")
	cmd := exec.CommandContext(ctx, a.config.Binary, args...)
	cmd.Dir = absCWD
	cmd.Env = append(append(os.Environ(), a.config.Env...), req.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return agent.Session{}, fmt.Errorf("OpenCode stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return agent.Session{}, fmt.Errorf("OpenCode stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return agent.Session{}, fmt.Errorf("OpenCode stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return agent.Session{}, fmt.Errorf("start OpenCode ACP: %w", err)
	}

	s := &session{
		taskID: req.TaskID,
		cmd:    cmd, stdin: stdin,
		events: make(chan domain.Event, 512), done: make(chan struct{}),
		stderrDone: make(chan struct{}), notifyDone: make(chan struct{}),
	}
	s.client = acp.NewClient(ctx, stdout, stdin, a.handleRequest)
	go a.consumeStderr(s, stderr)
	go a.consumeNotifications(s)
	go a.waitProcess(s)

	handshakeCtx, cancel := context.WithTimeout(ctx, a.config.HandshakeTimeout)
	defer cancel()
	var initialized initializeResponse
	if err := s.client.Call(handshakeCtx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
			"auth":     map[string]bool{"terminal": false},
		},
		"clientInfo": map[string]string{
			"name": "cyber-foreman", "title": "Cyber Foreman", "version": "0.1.0",
		},
	}, &initialized); err != nil {
		a.abortStart(s)
		return agent.Session{}, fmt.Errorf("initialize OpenCode ACP: %w", err)
	}
	if initialized.ProtocolVersion != protocolVersion {
		a.abortStart(s)
		return agent.Session{}, fmt.Errorf("unsupported ACP protocol version %d", initialized.ProtocolVersion)
	}

	var created newSessionResponse
	if err := s.client.Call(handshakeCtx, "session/new", map[string]any{
		"cwd": absCWD, "mcpServers": []any{},
	}, &created); err != nil {
		a.abortStart(s)
		return agent.Session{}, fmt.Errorf("create OpenCode ACP session: %w", err)
	}
	if created.SessionID == "" {
		a.abortStart(s)
		return agent.Session{}, errors.New("OpenCode returned an empty session id")
	}
	s.id = created.SessionID
	a.mu.Lock()
	a.sessions[s.id] = s
	a.mu.Unlock()
	s.emit(domain.Event{
		TaskID: req.TaskID, SessionID: s.id, Type: domain.EventAgentStarted,
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"adapter": "opencode", "pid": cmd.Process.Pid,
			"protocol_version": initialized.ProtocolVersion,
			"agent_info":       initialized.AgentInfo,
			"config_options":   created.ConfigOptions,
		},
	})
	return agent.Session{ID: s.id}, nil
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

func (a *Adapter) handleRequest(_ context.Context, method string, params json.RawMessage) (any, *acp.RPCError) {
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
	if s, err := a.get(request.SessionID); err == nil {
		options := make([]domain.PermissionOptionData, 0, len(request.Options))
		for _, option := range request.Options {
			options = append(options, domain.PermissionOptionData{ID: option.ID, Name: option.Name, Kind: option.Kind})
		}
		s.emit(domain.Event{
			TaskID: s.taskID, SessionID: s.id, Type: domain.EventAgentPermission,
			Timestamp: time.Now().UTC(), Data: domain.AgentPermissionData{Options: options},
		})
	}
	for _, preferredKind := range []string{"reject_once", "reject_always"} {
		for _, option := range request.Options {
			name := strings.ToLower(option.Name)
			if option.Kind == preferredKind || strings.Contains(name, "deny") || strings.Contains(name, "reject") {
				return map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": option.ID}}, nil
			}
		}
	}
	return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}, nil
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
