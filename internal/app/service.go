package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"cyber-foreman/internal/agent"
	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/event"
	"cyber-foreman/internal/task"
	"cyber-foreman/internal/verification"
)

var (
	ErrTaskNotFound      = errors.New("task not found")
	ErrInvalidTransition = errors.New("invalid task state transition")
)

type StartTaskRequest struct {
	Command      []string            `json:"command"`
	CWD          string              `json:"cwd,omitempty"`
	Verification VerificationRequest `json:"verification,omitempty"`
}

type VerificationRequest struct {
	Commands        []verification.Command       `json:"commands,omitempty"`
	Workspace       bool                         `json:"workspace,omitempty"`
	WorkspacePolicy verification.WorkspacePolicy `json:"workspace_policy,omitempty"`
}

type Service struct {
	ctx     context.Context
	adapter agent.Adapter
	bus     *event.Bus

	mu       sync.RWMutex
	tasks    map[string]*domain.Task
	runtimes map[string]taskRuntime
}

type taskRuntime struct {
	sessionID    string
	cancel       context.CancelFunc
	verification VerificationRequest
	baseline     *verification.WorkspaceBaseline
}

func NewService(ctx context.Context, adapter agent.Adapter, bus *event.Bus) *Service {
	return &Service{
		ctx: ctx, adapter: adapter, bus: bus,
		tasks: make(map[string]*domain.Task), runtimes: make(map[string]taskRuntime),
	}
}

func (s *Service) StartTask(req StartTaskRequest) (domain.Task, error) {
	if len(req.Command) == 0 {
		return domain.Task{}, errors.New("command is required")
	}
	cwd := req.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return domain.Task{}, fmt.Errorf("get working directory: %w", err)
		}
	}
	var baseline *verification.WorkspaceBaseline
	if req.Verification.Workspace {
		captured, err := (verification.WorkspaceVerifier{}).Capture(s.ctx, cwd, req.Verification.WorkspacePolicy)
		if err != nil {
			return domain.Task{}, fmt.Errorf("capture workspace baseline: %w", err)
		}
		baseline = &captured
	}
	now := time.Now().UTC()
	t := &domain.Task{
		ID: newTaskID(), Command: append([]string(nil), req.Command...), CWD: cwd,
		Status: domain.TaskQueued, CreatedAt: now, UpdatedAt: now,
	}
	s.mu.Lock()
	s.tasks[t.ID] = t
	s.mu.Unlock()
	s.bus.Publish(domain.Event{TaskID: t.ID, Type: domain.EventTaskCreated, Timestamp: now, Data: cloneTask(t)})

	ctx, cancel := context.WithCancel(s.ctx)
	session, err := s.adapter.Start(ctx, agent.StartRequest{TaskID: t.ID, Command: t.Command, CWD: t.CWD})
	if err != nil {
		cancel()
		_ = s.fail(t.ID, -1, err.Error())
		return domain.Task{}, err
	}
	events, err := s.adapter.Events(ctx, session.ID)
	if err != nil {
		cancel()
		_ = s.fail(t.ID, -1, err.Error())
		return domain.Task{}, err
	}
	s.mu.Lock()
	s.runtimes[t.ID] = taskRuntime{
		sessionID: session.ID, cancel: cancel,
		verification: cloneVerificationRequest(req.Verification), baseline: baseline,
	}
	s.mu.Unlock()
	if err := s.transition(t.ID, domain.TaskRunning, "agent process started"); err != nil {
		cancel()
		return domain.Task{}, err
	}
	go s.consume(ctx, t.ID, events)
	return s.GetTask(t.ID)
}

func (s *Service) GetTask(id string) (domain.Task, error) {
	s.mu.RLock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.RUnlock()
		return domain.Task{}, ErrTaskNotFound
	}
	result := cloneTask(t)
	s.mu.RUnlock()
	return result, nil
}

func (s *Service) ListTasks() []domain.Task {
	s.mu.RLock()
	tasks := make([]domain.Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		tasks = append(tasks, cloneTask(t))
	}
	s.mu.RUnlock()
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.After(tasks[j].CreatedAt) })
	return tasks
}

func (s *Service) StopTask(id string) error {
	s.mu.RLock()
	runtime, ok := s.runtimes[id]
	s.mu.RUnlock()
	if !ok {
		if _, err := s.GetTask(id); err != nil {
			return err
		}
		return nil
	}
	runtime.cancel()
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.adapter.Stop(stopCtx, runtime.sessionID)
	return s.transition(id, domain.TaskStopped, "stopped by operator")
}

func (s *Service) consume(ctx context.Context, taskID string, events <-chan domain.Event) {
	for evt := range events {
		s.bus.Publish(evt)
		if evt.Type != domain.EventAgentExited {
			continue
		}
		data, ok := evt.Data.(domain.AgentExitData)
		if !ok {
			_ = s.fail(taskID, -1, "agent returned malformed exit event")
			continue
		}
		current, err := s.GetTask(taskID)
		if err != nil || current.Status.Terminal() {
			continue
		}
		if data.ExitCode != 0 {
			_ = s.fail(taskID, data.ExitCode, data.Error)
			continue
		}
		if err := s.transition(taskID, domain.TaskVerifying, "process exited successfully"); err == nil {
			s.mu.RLock()
			runtime := s.runtimes[taskID]
			s.mu.RUnlock()
			s.runVerification(ctx, taskID, runtime)
		}
	}
	s.mu.Lock()
	delete(s.runtimes, taskID)
	s.mu.Unlock()
}

func (s *Service) runVerification(ctx context.Context, taskID string, runtime taskRuntime) {
	required := false
	passed := true
	if len(runtime.verification.Commands) > 0 {
		required = true
		s.bus.Publish(domain.Event{TaskID: taskID, Type: domain.EventVerificationStart, Timestamp: time.Now().UTC(),
			Data: map[string]string{"verifier": "test"}})
		result := (verification.CommandVerifier{}).Run(ctx, s.taskCWD(taskID), runtime.verification.Commands)
		s.bus.Publish(domain.Event{TaskID: taskID, Type: domain.EventVerificationFinish, Timestamp: time.Now().UTC(),
			Data: map[string]any{"verifier": "test", "passed": result.Passed, "result": result}})
		if !result.Passed {
			passed = false
		}
	}
	if runtime.verification.Workspace {
		required = true
		s.bus.Publish(domain.Event{TaskID: taskID, Type: domain.EventVerificationStart, Timestamp: time.Now().UTC(),
			Data: map[string]string{"verifier": "workspace"}})
		var result verification.WorkspaceResult
		var err error
		if runtime.baseline == nil {
			err = errors.New("workspace baseline is missing")
		} else {
			result, err = (verification.WorkspaceVerifier{}).Verify(ctx, *runtime.baseline, runtime.verification.WorkspacePolicy)
		}
		data := map[string]any{"verifier": "workspace", "passed": err == nil && result.Passed, "result": result}
		if err != nil {
			data["error"] = err.Error()
		}
		s.bus.Publish(domain.Event{TaskID: taskID, Type: domain.EventVerificationFinish, Timestamp: time.Now().UTC(), Data: data})
		if err != nil || !result.Passed {
			passed = false
		}
	}
	if !required {
		_ = s.transition(taskID, domain.TaskCompleted, "process command exited successfully; no additional verifier configured")
		return
	}
	if passed {
		_ = s.transition(taskID, domain.TaskCompleted, "all configured verifiers passed")
		return
	}
	_ = s.requireAttention(taskID, "deterministic verification failed")
}

func (s *Service) taskCWD(taskID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if task := s.tasks[taskID]; task != nil {
		return task.CWD
	}
	return ""
}

func (s *Service) requireAttention(id, reason string) error {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return ErrTaskNotFound
	}
	from := t.Status
	if !task.CanTransition(from, domain.TaskAttention) {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, domain.TaskAttention)
	}
	now := time.Now().UTC()
	t.Status = domain.TaskAttention
	t.Error = reason
	t.UpdatedAt = now
	s.mu.Unlock()
	s.bus.Publish(domain.Event{TaskID: id, Type: domain.EventTaskState, Timestamp: now,
		Data: domain.TaskStateData{From: from, To: domain.TaskAttention, Reason: reason}})
	s.bus.Publish(domain.Event{TaskID: id, Type: domain.EventTaskAttention, Timestamp: now,
		Data: map[string]string{"reason": reason}})
	return nil
}

func (s *Service) fail(id string, exitCode int, message string) error {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return ErrTaskNotFound
	}
	if t.Status.Terminal() {
		s.mu.Unlock()
		return nil
	}
	from := t.Status
	if !task.CanTransition(from, domain.TaskFailed) {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, domain.TaskFailed)
	}
	t.Status = domain.TaskFailed
	t.ExitCode = &exitCode
	t.Error = message
	t.UpdatedAt = time.Now().UTC()
	updated := cloneTask(t)
	s.mu.Unlock()
	s.bus.Publish(domain.Event{
		TaskID: id, Type: domain.EventTaskState, Timestamp: updated.UpdatedAt,
		Data: domain.TaskStateData{From: from, To: domain.TaskFailed, Reason: message},
	})
	return nil
}

func (s *Service) transition(id string, to domain.TaskStatus, reason string) error {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return ErrTaskNotFound
	}
	from := t.Status
	if !task.CanTransition(from, to) {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	t.Status = to
	t.UpdatedAt = time.Now().UTC()
	updatedAt := t.UpdatedAt
	s.mu.Unlock()
	s.bus.Publish(domain.Event{
		TaskID: id, Type: domain.EventTaskState, Timestamp: updatedAt,
		Data: domain.TaskStateData{From: from, To: to, Reason: reason},
	})
	return nil
}

func newTaskID() string {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("task-%d", time.Now().UnixNano())
	}
	return "task-" + hex.EncodeToString(random)
}

func cloneTask(t *domain.Task) domain.Task {
	clone := *t
	clone.Command = append([]string(nil), t.Command...)
	if t.ExitCode != nil {
		exitCode := *t.ExitCode
		clone.ExitCode = &exitCode
	}
	return clone
}

func cloneVerificationRequest(request VerificationRequest) VerificationRequest {
	clone := request
	clone.Commands = make([]verification.Command, len(request.Commands))
	for i, command := range request.Commands {
		clone.Commands[i] = command
		clone.Commands[i].Argv = append([]string(nil), command.Argv...)
	}
	clone.WorkspacePolicy.IgnoredPaths = append([]string(nil), request.WorkspacePolicy.IgnoredPaths...)
	clone.WorkspacePolicy.SensitivePaths = append([]string(nil), request.WorkspacePolicy.SensitivePaths...)
	return clone
}
