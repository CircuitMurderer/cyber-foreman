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
	"cyber-foreman/internal/supervisor"
	"cyber-foreman/internal/task"
	"cyber-foreman/internal/verification"
)

var (
	ErrTaskNotFound      = errors.New("task not found")
	ErrInvalidTransition = errors.New("invalid task state transition")
	ErrTaskNotRunning    = errors.New("task is not running")
	ErrActionUnavailable = errors.New("task action is unavailable")
)

type StartTaskRequest struct {
	Adapter       string              `json:"adapter,omitempty"`
	Command       []string            `json:"command"`
	Prompt        string              `json:"prompt,omitempty"`
	Model         string              `json:"model,omitempty"`
	InterruptWith string              `json:"interrupt_with,omitempty"`
	CWD           string              `json:"cwd,omitempty"`
	Verification  VerificationRequest `json:"verification,omitempty"`
	Supervision   *supervisor.Policy  `json:"supervision,omitempty"`
}

type VerificationRequest struct {
	Commands        []verification.Command       `json:"commands,omitempty"`
	Workspace       bool                         `json:"workspace,omitempty"`
	WorkspacePolicy verification.WorkspacePolicy `json:"workspace_policy,omitempty"`
}

type Service struct {
	ctx            context.Context
	adapters       *agent.Registry
	defaultAdapter string
	bus            *event.Bus

	mu       sync.RWMutex
	tasks    map[string]*domain.Task
	runtimes map[string]taskRuntime
}

type taskRuntime struct {
	adapter       agent.Adapter
	sessionID     string
	cancel        context.CancelFunc
	actions       chan taskAction
	verification  VerificationRequest
	baseline      *verification.WorkspaceBaseline
	prompt        string
	interruptWith string
	policy        supervisor.Policy
}

type taskAction struct {
	kind    string
	message string
	result  chan error
}

const taskActionInterrupt = "interrupt"

func NewService(ctx context.Context, adapter agent.Adapter, bus *event.Bus) *Service {
	registry, err := agent.NewRegistry(adapter)
	if err != nil {
		panic(err)
	}
	return NewServiceWithRegistry(ctx, registry, adapter.Name(), bus)
}

func NewServiceWithRegistry(ctx context.Context, adapters *agent.Registry, defaultAdapter string, bus *event.Bus) *Service {
	if adapters == nil {
		panic("adapter registry is nil")
	}
	if bus == nil {
		panic("event bus is nil")
	}
	return &Service{
		ctx: ctx, adapters: adapters, defaultAdapter: defaultAdapter, bus: bus,
		tasks: make(map[string]*domain.Task), runtimes: make(map[string]taskRuntime),
	}
}

func (s *Service) StartTask(req StartTaskRequest) (domain.Task, error) {
	t, adapter, err := s.queueTask(req)
	if err != nil {
		return domain.Task{}, err
	}
	if err := s.startQueuedTask(t.ID, req, adapter); err != nil {
		return domain.Task{}, err
	}
	return s.GetTask(t.ID)
}

// SubmitTask records a queued task and performs adapter startup asynchronously.
// The REST control plane uses this so slow handshakes never delay task identity.
func (s *Service) SubmitTask(req StartTaskRequest) (domain.Task, error) {
	t, adapter, err := s.queueTask(req)
	if err != nil {
		return domain.Task{}, err
	}
	go func() { _ = s.startQueuedTask(t.ID, req, adapter) }()
	return t, nil
}

func (s *Service) queueTask(req StartTaskRequest) (domain.Task, agent.Adapter, error) {
	if (len(req.Command) == 0) == (req.Prompt == "") {
		return domain.Task{}, nil, errors.New("exactly one of command or prompt is required")
	}
	adapterName := req.Adapter
	if adapterName == "" {
		adapterName = s.defaultAdapter
	}
	adapter, err := s.adapters.Get(adapterName)
	if err != nil {
		return domain.Task{}, nil, err
	}
	if req.Prompt != "" && !adapter.Capabilities().Prompt {
		return domain.Task{}, nil, errors.New("selected adapter does not support prompt tasks")
	}
	if len(req.Command) > 0 && !adapter.Capabilities().Command {
		return domain.Task{}, nil, errors.New("selected adapter does not support command tasks")
	}
	cwd := req.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return domain.Task{}, nil, fmt.Errorf("get working directory: %w", err)
		}
	}
	now := time.Now().UTC()
	kind := domain.TaskKindCommand
	if req.Prompt != "" {
		kind = domain.TaskKindAgent
	}
	t := &domain.Task{
		ID: newTaskID(), Kind: kind, Adapter: adapter.Name(),
		Command: append([]string(nil), req.Command...), CWD: cwd,
		Status: domain.TaskQueued, CreatedAt: now, UpdatedAt: now,
	}
	s.mu.Lock()
	s.tasks[t.ID] = t
	s.mu.Unlock()
	s.bus.Publish(domain.Event{TaskID: t.ID, Type: domain.EventTaskCreated, Timestamp: now, Data: cloneTask(t)})
	return cloneTask(t), adapter, nil
}

func (s *Service) startQueuedTask(taskID string, req StartTaskRequest, adapter agent.Adapter) error {
	t, err := s.GetTask(taskID)
	if err != nil {
		return err
	}
	if t.Status != domain.TaskQueued {
		return ErrTaskNotRunning
	}
	var baseline *verification.WorkspaceBaseline
	if req.Verification.Workspace {
		captured, err := (verification.WorkspaceVerifier{}).Capture(s.ctx, t.CWD, req.Verification.WorkspacePolicy)
		if err != nil {
			wrapped := fmt.Errorf("capture workspace baseline: %w", err)
			_ = s.fail(t.ID, -1, wrapped.Error())
			return wrapped
		}
		baseline = &captured
	}
	t, err = s.GetTask(taskID)
	if err != nil {
		return err
	}
	if t.Status != domain.TaskQueued {
		return ErrTaskNotRunning
	}

	ctx, cancel := context.WithCancel(s.ctx)
	session, err := adapter.Start(ctx, agent.StartRequest{TaskID: t.ID, Command: t.Command, CWD: t.CWD})
	if err != nil {
		cancel()
		_ = s.fail(t.ID, -1, err.Error())
		return err
	}
	events, err := adapter.Events(ctx, session.ID)
	if err != nil {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = adapter.Stop(stopCtx, session.ID)
		stopCancel()
		_ = s.fail(t.ID, -1, err.Error())
		return err
	}
	if req.Model != "" {
		if err := adapter.SetConfigOption(ctx, session.ID, agent.ConfigOption{ID: "model", Value: req.Model}); err != nil {
			cancel()
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = adapter.Stop(stopCtx, session.ID)
			stopCancel()
			_ = s.fail(t.ID, -1, "select model: "+err.Error())
			return err
		}
	}
	policy := supervisor.DefaultPolicy()
	if req.Supervision != nil {
		policy = *req.Supervision
	}
	s.mu.Lock()
	s.runtimes[t.ID] = taskRuntime{
		adapter: adapter, sessionID: session.ID, cancel: cancel, actions: make(chan taskAction, 1),
		verification: cloneVerificationRequest(req.Verification), baseline: baseline,
		prompt: req.Prompt, interruptWith: req.InterruptWith, policy: policy,
	}
	s.mu.Unlock()
	if err := s.transition(t.ID, domain.TaskRunning, "agent process started"); err != nil {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = adapter.Stop(stopCtx, session.ID)
		stopCancel()
		s.mu.Lock()
		delete(s.runtimes, t.ID)
		s.mu.Unlock()
		return err
	}
	if t.Kind == domain.TaskKindAgent {
		go s.consumePrompt(ctx, t.ID, events)
	} else {
		go s.consume(ctx, t.ID, events)
	}
	return nil
}

func (s *Service) ListAdapters() []agent.Descriptor { return s.adapters.List() }

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
		current, err := s.GetTask(id)
		if err != nil {
			return err
		}
		if current.Status.Terminal() {
			return nil
		}
		return s.transition(id, domain.TaskStopped, "stopped by operator")
	}
	runtime.cancel()
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = runtime.adapter.Stop(stopCtx, runtime.sessionID)
	return s.transition(id, domain.TaskStopped, "stopped by operator")
}

// InterruptTask cancels the active agent turn and schedules a same-session
// follow-up. Completion is acknowledged only after the task mailbox accepted
// and applied the action.
func (s *Service) InterruptTask(ctx context.Context, id, message string) error {
	if message == "" {
		return errors.New("interrupt message is required")
	}
	s.mu.RLock()
	runtime, ok := s.runtimes[id]
	t := s.tasks[id]
	s.mu.RUnlock()
	if t == nil {
		return ErrTaskNotFound
	}
	if !ok || t.Status != domain.TaskRunning || runtime.actions == nil {
		return ErrTaskNotRunning
	}
	capabilities := runtime.adapter.Capabilities()
	if t.Kind != domain.TaskKindAgent || !capabilities.Prompt || !capabilities.CancelTurn {
		return ErrActionUnavailable
	}
	action := taskAction{kind: taskActionInterrupt, message: message, result: make(chan error, 1)}
	select {
	case runtime.actions <- action:
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrActionUnavailable
	}
	select {
	case err := <-action.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
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
