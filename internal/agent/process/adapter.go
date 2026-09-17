package process

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"cyber-foreman/internal/agent"
	"cyber-foreman/internal/domain"
)

var ErrUnknownSession = errors.New("unknown process session")

type Adapter struct {
	nextID   atomic.Uint64
	mu       sync.RWMutex
	sessions map[string]*session
}

type session struct {
	taskID string
	cmd    *exec.Cmd
	events chan domain.Event
	done   chan struct{}
}

func NewAdapter() *Adapter {
	return &Adapter{sessions: make(map[string]*session)}
}

func (a *Adapter) Name() string { return "process" }

func (a *Adapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{CancelTurn: true}
}

func (a *Adapter) Start(ctx context.Context, req agent.StartRequest) (agent.Session, error) {
	if len(req.Command) == 0 {
		return agent.Session{}, errors.New("command is required")
	}

	cmd := exec.CommandContext(ctx, req.Command[0], req.Command[1:]...)
	cmd.Dir = req.CWD
	cmd.Env = append(os.Environ(), req.Env...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return agent.Session{}, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return agent.Session{}, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return agent.Session{}, fmt.Errorf("start process: %w", err)
	}

	id := fmt.Sprintf("process-%d", a.nextID.Add(1))
	s := &session{
		taskID: req.TaskID,
		cmd:    cmd,
		events: make(chan domain.Event, 256),
		done:   make(chan struct{}),
	}
	a.mu.Lock()
	a.sessions[id] = s
	a.mu.Unlock()

	s.events <- domain.Event{
		TaskID: req.TaskID, SessionID: id, Type: domain.EventAgentStarted,
		Timestamp: time.Now().UTC(),
		Data:      map[string]any{"pid": cmd.Process.Pid, "command": req.Command},
	}

	var readers sync.WaitGroup
	readers.Add(2)
	go a.scan(id, s, "stdout", stdout, &readers)
	go a.scan(id, s, "stderr", stderr, &readers)
	go a.wait(id, s, &readers)

	return agent.Session{ID: id}, nil
}

func (a *Adapter) Events(_ context.Context, sessionID string) (<-chan domain.Event, error) {
	a.mu.RLock()
	s, ok := a.sessions[sessionID]
	a.mu.RUnlock()
	if !ok {
		return nil, ErrUnknownSession
	}
	return s.events, nil
}

func (a *Adapter) Prompt(context.Context, string, agent.PromptRequest) (agent.PromptResult, error) {
	return agent.PromptResult{}, agent.ErrUnsupported
}

func (a *Adapter) Cancel(ctx context.Context, sessionID string) error {
	return a.Stop(ctx, sessionID)
}

func (a *Adapter) SetConfigOption(context.Context, string, agent.ConfigOption) error {
	return agent.ErrUnsupported
}

func (a *Adapter) Stop(ctx context.Context, sessionID string) error {
	a.mu.RLock()
	s, ok := a.sessions[sessionID]
	a.mu.RUnlock()
	if !ok {
		return ErrUnknownSession
	}
	if s.cmd.Process == nil {
		return nil
	}
	if err := s.cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}

	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		if err := s.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		return ctx.Err()
	}
}

func (a *Adapter) scan(sessionID string, s *session, stream string, reader io.Reader, wg *sync.WaitGroup) {
	defer wg.Done()
	scanner := bufio.NewScanner(reader)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	for scanner.Scan() {
		s.events <- domain.Event{
			TaskID: s.taskID, SessionID: sessionID, Type: domain.EventAgentOutput,
			Timestamp: time.Now().UTC(),
			Data:      domain.AgentOutputData{Stream: stream, Line: scanner.Text()},
		}
	}
	if err := scanner.Err(); err != nil {
		s.events <- domain.Event{
			TaskID: s.taskID, SessionID: sessionID, Type: domain.EventAgentError,
			Timestamp: time.Now().UTC(), Data: map[string]string{"stream": stream, "error": err.Error()},
		}
	}
}

func (a *Adapter) wait(sessionID string, s *session, readers *sync.WaitGroup) {
	err := s.cmd.Wait()
	readers.Wait()
	exitCode := 0
	message := ""
	if err != nil {
		message = err.Error()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	s.events <- domain.Event{
		TaskID: s.taskID, SessionID: sessionID, Type: domain.EventAgentExited,
		Timestamp: time.Now().UTC(), Data: domain.AgentExitData{ExitCode: exitCode, Error: message},
	}
	close(s.done)
	close(s.events)
}
