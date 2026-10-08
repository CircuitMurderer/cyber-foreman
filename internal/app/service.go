package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"cyber-foreman/internal/agent"
	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/event"
	"cyber-foreman/internal/storage"
	"cyber-foreman/internal/supervisor"
	"cyber-foreman/internal/task"
	"cyber-foreman/internal/verification"
	"cyber-foreman/internal/worktree"
)

var (
	ErrTaskNotFound      = errors.New("task not found")
	ErrInvalidTransition = errors.New("invalid task state transition")
	ErrTaskNotRunning    = errors.New("task is not running")
	ErrActionUnavailable = errors.New("task action is unavailable")
	ErrTaskNotDeletable  = errors.New("running task must be stopped before deletion")
	ErrDiffUnavailable   = errors.New("task does not use an isolated worktree")
	ErrTaskForbidden     = errors.New("task is forbidden by deployment policy")
)

type StartTaskRequest struct {
	Adapter       string              `json:"adapter,omitempty"`
	Command       []string            `json:"command"`
	Prompt        string              `json:"prompt,omitempty"`
	Model         string              `json:"model,omitempty"`
	InterruptWith string              `json:"interrupt_with,omitempty"`
	Interactive   bool                `json:"interactive,omitempty"`
	Worktree      bool                `json:"worktree,omitempty"`
	CWD           string              `json:"cwd,omitempty"`
	Verification  VerificationRequest `json:"verification,omitempty"`
	Supervision   *supervisor.Policy  `json:"supervision,omitempty"`
}

type VerificationRequest struct {
	Commands        []verification.Command       `json:"commands,omitempty"`
	Workspace       bool                         `json:"workspace,omitempty"`
	WorkspacePolicy verification.WorkspacePolicy `json:"workspace_policy,omitempty"`
}

type TaskAuthorization struct {
	Adapter              string
	Workspace            string
	Command              []string
	VerificationCommands [][]string
}

type RequestPolicy interface {
	AuthorizeTask(TaskAuthorization) error
}

type Service struct {
	ctx            context.Context
	adapters       *agent.Registry
	defaultAdapter string
	bus            *event.Bus
	store          storage.TaskStore
	requestPolicy  RequestPolicy

	mu       sync.RWMutex
	tasks    map[string]*domain.Task
	runtimes map[string]taskRuntime
}

// SetRequestPolicy installs deployment authorization before the service starts
// accepting tasks. Constructors remain permissive for CLI use and tests.
func (s *Service) SetRequestPolicy(policy RequestPolicy) {
	s.requestPolicy = policy
}

type taskRuntime struct {
	adapter       agent.Adapter
	sessionID     string
	startRequest  agent.StartRequest
	model         string
	cancel        context.CancelFunc
	actions       chan taskAction
	verification  VerificationRequest
	baseline      *verification.WorkspaceBaseline
	prompt        string
	interruptWith string
	interactive   bool
	policy        supervisor.Policy
}

type taskAction struct {
	kind    string
	message string
	result  chan error
}

const taskActionInterrupt = "interrupt"
const taskActionContinue = "continue"

func NewService(ctx context.Context, adapter agent.Adapter, bus *event.Bus) *Service {
	registry, err := agent.NewRegistry(adapter)
	if err != nil {
		panic(err)
	}
	return NewServiceWithRegistry(ctx, registry, adapter.Name(), bus)
}

func NewServiceWithRegistry(ctx context.Context, adapters *agent.Registry, defaultAdapter string, bus *event.Bus) *Service {
	service, err := newServiceWithRegistry(ctx, adapters, defaultAdapter, bus, nil)
	if err != nil {
		panic(err)
	}
	return service
}

// NewServiceWithRegistryAndStore restores durable task snapshots. Tasks that
// were active when the previous process stopped are made attention-required:
// process handles and ACP sessions cannot safely survive a Foreman restart.
func NewServiceWithRegistryAndStore(ctx context.Context, adapters *agent.Registry, defaultAdapter string, bus *event.Bus, store storage.TaskStore) (*Service, error) {
	return newServiceWithRegistry(ctx, adapters, defaultAdapter, bus, store)
}

func newServiceWithRegistry(ctx context.Context, adapters *agent.Registry, defaultAdapter string, bus *event.Bus, store storage.TaskStore) (*Service, error) {
	if adapters == nil {
		panic("adapter registry is nil")
	}
	if bus == nil {
		panic("event bus is nil")
	}
	service := &Service{
		ctx: ctx, adapters: adapters, defaultAdapter: defaultAdapter, bus: bus,
		store: store, tasks: make(map[string]*domain.Task), runtimes: make(map[string]taskRuntime),
	}
	if store == nil {
		return service, nil
	}
	if err := service.restoreTasks(); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *Service) restoreTasks() error {
	tasks, err := s.store.ListTasks()
	if err != nil {
		return fmt.Errorf("restore tasks: %w", err)
	}
	for i := range tasks {
		restored := cloneTask(&tasks[i])
		if !restored.Status.Terminal() {
			from := restored.Status
			restored.Status = domain.TaskAttention
			restored.Error = "foreman restarted before the task reached a terminal state"
			restored.UpdatedAt = time.Now().UTC()
			if err := s.store.PutTask(restored); err != nil {
				return fmt.Errorf("recover task %s: %w", restored.ID, err)
			}
			s.tasks[restored.ID] = &restored
			if err := s.bus.PublishChecked(domain.Event{
				TaskID: restored.ID, Type: domain.EventTaskState, Timestamp: restored.UpdatedAt,
				Data: domain.TaskStateData{From: from, To: domain.TaskAttention, Reason: restored.Error},
			}); err != nil {
				return fmt.Errorf("record task %s recovery: %w", restored.ID, err)
			}
			if err := s.bus.PublishChecked(domain.Event{
				TaskID: restored.ID, Type: domain.EventTaskAttention, Timestamp: restored.UpdatedAt,
				Data: map[string]string{"reason": restored.Error},
			}); err != nil {
				return fmt.Errorf("record task %s recovery attention: %w", restored.ID, err)
			}
			continue
		}
		s.tasks[restored.ID] = &restored
	}
	return nil
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
	status := agent.StatusOf(adapter)
	if !status.Installed || !status.Healthy {
		reason := status.Error
		if reason == "" {
			reason = "health check did not succeed"
		}
		return domain.Task{}, nil, fmt.Errorf("%w: %s: %s", agent.ErrAdapterUnavailable, adapterName, reason)
	}
	if req.Prompt != "" && !adapter.Capabilities().Prompt {
		return domain.Task{}, nil, errors.New("selected adapter does not support prompt tasks")
	}
	if len(req.Command) > 0 && !adapter.Capabilities().Command {
		return domain.Task{}, nil, errors.New("selected adapter does not support command tasks")
	}
	cwd := req.CWD
	if cwd == "" {
		cwd = agent.MetadataOf(adapter).DefaultWorkspace
		if cwd == "" {
			var err error
			cwd, err = os.Getwd()
			if err != nil {
				return domain.Task{}, nil, fmt.Errorf("get working directory: %w", err)
			}
		}
	}
	if s.requestPolicy != nil {
		verificationCommands := make([][]string, len(req.Verification.Commands))
		for index, command := range req.Verification.Commands {
			verificationCommands[index] = append([]string(nil), command.Argv...)
		}
		if err := s.requestPolicy.AuthorizeTask(TaskAuthorization{
			Adapter: adapterName, Workspace: cwd, Command: append([]string(nil), req.Command...),
			VerificationCommands: verificationCommands,
		}); err != nil {
			return domain.Task{}, nil, err
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
	if s.store != nil {
		if err := s.store.PutTask(cloneTask(t)); err != nil {
			return domain.Task{}, nil, err
		}
	}
	s.mu.Lock()
	s.tasks[t.ID] = t
	s.mu.Unlock()
	if err := s.bus.PublishChecked(domain.Event{TaskID: t.ID, Type: domain.EventTaskCreated, Timestamp: now, Data: cloneTask(t)}); err != nil {
		s.mu.Lock()
		delete(s.tasks, t.ID)
		s.mu.Unlock()
		if s.store != nil {
			_ = s.store.DeleteTask(t.ID)
		}
		return domain.Task{}, nil, fmt.Errorf("record task creation: %w", err)
	}
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
	if req.Worktree {
		prepared, prepareErr := worktree.Prepare(s.ctx, t.CWD, t.ID)
		if prepareErr != nil {
			wrapped := fmt.Errorf("prepare isolated worktree: %w", prepareErr)
			_ = s.fail(t.ID, -1, wrapped.Error())
			return wrapped
		}
		if err := s.setTaskWorktree(t.ID, prepared); err != nil {
			_ = s.fail(t.ID, -1, err.Error())
			return err
		}
		t, err = s.GetTask(taskID)
		if err != nil {
			return err
		}
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
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = agent.MetadataOf(adapter).DefaultModel
	}
	if model != "" {
		if err := adapter.SetConfigOption(ctx, session.ID, agent.ConfigOption{ID: "model", Value: model}); err != nil {
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
		adapter: adapter, sessionID: session.ID,
		startRequest: agent.StartRequest{TaskID: t.ID, Command: append([]string(nil), t.Command...), CWD: t.CWD},
		model:        model, cancel: cancel, actions: make(chan taskAction, 1),
		verification: cloneVerificationRequest(req.Verification), baseline: baseline,
		prompt: req.Prompt, interruptWith: req.InterruptWith, interactive: req.Interactive, policy: policy,
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

func (s *Service) TaskDiff(ctx context.Context, id string) (worktree.Diff, error) {
	task, err := s.GetTask(id)
	if err != nil {
		return worktree.Diff{}, err
	}
	if task.WorktreeRoot == "" {
		return worktree.Diff{}, ErrDiffUnavailable
	}
	return worktree.ReadDiff(ctx, task.WorktreeRoot, task.BaseRevision)
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

func (s *Service) AvailableActions(id string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.tasks[id]
	if t == nil {
		return nil
	}
	runtime, hasRuntime := s.runtimes[id]
	actions := make([]string, 0, 5)
	if hasRuntime && t.Kind == domain.TaskKindAgent && t.Status == domain.TaskRunning && runtime.adapter.Capabilities().CancelTurn {
		actions = append(actions, "interrupt")
	}
	if hasRuntime && runtime.interactive && (t.Status == domain.TaskWaiting || t.Status == domain.TaskAttention) {
		actions = append(actions, "continue")
	}
	if hasRuntime && runtime.interactive && t.Status == domain.TaskWaiting {
		actions = append(actions, "finish")
	}
	if !t.Status.Terminal() || (hasRuntime && t.Status == domain.TaskAttention) {
		actions = append(actions, "cancel")
	}
	if t.Status.Terminal() || t.Status == domain.TaskWaiting {
		actions = append(actions, "delete")
	}
	return actions
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
	current, err := s.GetTask(id)
	if err != nil {
		return err
	}
	if current.Status.Terminal() {
		return nil
	}
	return s.transition(id, domain.TaskStopped, "stopped by operator")
}

// FinishTask marks a verified interactive task complete and closes its idle
// Agent session. It is intentionally limited to waiting_input so an operator
// cannot bypass a failed verification or terminate an active turn as success.
func (s *Service) FinishTask(id string) error {
	s.mu.RLock()
	runtime, ok := s.runtimes[id]
	t := s.tasks[id]
	var current domain.Task
	if t != nil {
		current = cloneTask(t)
	}
	s.mu.RUnlock()
	if t == nil {
		return ErrTaskNotFound
	}
	if !ok || current.Kind != domain.TaskKindAgent || !runtime.interactive || current.Status != domain.TaskWaiting {
		return ErrActionUnavailable
	}
	if err := s.transition(id, domain.TaskCompleted, "operator finished the interactive task"); err != nil {
		return err
	}
	runtime.cancel()
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = runtime.adapter.Stop(stopCtx, runtime.sessionID)
	return nil
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

// ContinueTask sends a new prompt through the existing agent session without
// cancelling a turn. It is accepted only while an interactive task is idle.
func (s *Service) ContinueTask(ctx context.Context, id, message string) error {
	if message == "" {
		return errors.New("continue message is required")
	}
	s.mu.RLock()
	runtime, ok := s.runtimes[id]
	t := s.tasks[id]
	s.mu.RUnlock()
	if t == nil {
		return ErrTaskNotFound
	}
	if !ok || !runtime.interactive || runtime.actions == nil || t.Kind != domain.TaskKindAgent ||
		(t.Status != domain.TaskWaiting && t.Status != domain.TaskAttention) {
		return ErrActionUnavailable
	}
	action := taskAction{kind: taskActionContinue, message: message, result: make(chan error, 1)}
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

// DeleteTask removes a quiescent task and its durable event history. Active
// tasks must be stopped first so deletion cannot hide a running process.
func (s *Service) DeleteTask(id string) error {
	s.mu.RLock()
	t := s.tasks[id]
	runtime, hasRuntime := s.runtimes[id]
	s.mu.RUnlock()
	if t == nil {
		return ErrTaskNotFound
	}
	if !t.Status.Terminal() && t.Status != domain.TaskWaiting {
		return ErrTaskNotDeletable
	}
	s.bus.ForgetTask(id)
	if hasRuntime {
		runtime.cancel()
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = runtime.adapter.Stop(stopCtx, runtime.sessionID)
		cancel()
	}
	if s.store != nil {
		if err := s.store.DeleteTask(id); err != nil {
			s.bus.RememberTask(id)
			return err
		}
	}
	s.mu.Lock()
	delete(s.tasks, id)
	delete(s.runtimes, id)
	s.mu.Unlock()
	return nil
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

type verificationReport struct {
	Required       bool
	Passed         bool
	Test           *verification.TestResult
	Workspace      *verification.WorkspaceResult
	WorkspaceError string
}

func (s *Service) executeVerification(ctx context.Context, taskID string, runtime taskRuntime) verificationReport {
	report := verificationReport{Passed: true}
	if runtime.verification.Workspace {
		report.Required = true
		s.bus.Publish(domain.Event{TaskID: taskID, Type: domain.EventVerificationStart, Timestamp: time.Now().UTC(),
			Data: map[string]string{"verifier": "workspace"}})
		var result verification.WorkspaceResult
		var err error
		if runtime.baseline == nil {
			err = errors.New("workspace baseline is missing")
		} else {
			result, err = (verification.WorkspaceVerifier{}).Verify(ctx, *runtime.baseline, runtime.verification.WorkspacePolicy)
		}
		report.Workspace = &result
		data := map[string]any{"verifier": "workspace", "passed": err == nil && result.Passed, "result": result}
		if err != nil {
			report.WorkspaceError = err.Error()
			data["error"] = report.WorkspaceError
		}
		s.bus.Publish(domain.Event{TaskID: taskID, Type: domain.EventVerificationFinish, Timestamp: time.Now().UTC(), Data: data})
		if err != nil || !result.Passed {
			report.Passed = false
		}
	}
	// Workspace policy is the safety gate. Never execute repository-controlled
	// verification commands after it reports an unsafe or invalid workspace.
	if report.Passed && len(runtime.verification.Commands) > 0 {
		report.Required = true
		s.bus.Publish(domain.Event{TaskID: taskID, Type: domain.EventVerificationStart, Timestamp: time.Now().UTC(),
			Data: map[string]string{"verifier": "test"}})
		result := (verification.CommandVerifier{}).Run(ctx, s.taskCWD(taskID), runtime.verification.Commands)
		report.Test = &result
		s.bus.Publish(domain.Event{TaskID: taskID, Type: domain.EventVerificationFinish, Timestamp: time.Now().UTC(),
			Data: map[string]any{"verifier": "test", "passed": result.Passed, "result": result}})
		if !result.Passed {
			report.Passed = false
		}
	} else if len(runtime.verification.Commands) > 0 {
		report.Required = true
	}
	return report
}

func (s *Service) runVerification(ctx context.Context, taskID string, runtime taskRuntime) {
	report := s.executeVerification(ctx, taskID, runtime)
	s.finishVerification(taskID, runtime, report)
}

func (s *Service) finishVerification(taskID string, runtime taskRuntime, report verificationReport) {
	if !report.Required {
		status := domain.TaskCompleted
		reason := "process command exited successfully; no additional verifier configured"
		if runtime.interactive {
			status = domain.TaskWaiting
			reason = "agent turn finished; waiting for operator follow-up"
		}
		_ = s.transition(taskID, status, reason)
		return
	}
	if report.Passed {
		status := domain.TaskCompleted
		reason := "all configured verifiers passed"
		if runtime.interactive {
			status = domain.TaskWaiting
			reason = "all configured verifiers passed; waiting for operator follow-up"
		}
		_ = s.transition(taskID, status, reason)
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
	updated := cloneTask(t)
	updated.Status = domain.TaskAttention
	updated.Error = reason
	updated.UpdatedAt = now
	if s.store != nil {
		if err := s.store.PutTask(updated); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	*t = updated
	s.mu.Unlock()
	if err := s.bus.PublishChecked(domain.Event{TaskID: id, Type: domain.EventTaskState, Timestamp: now,
		Data: domain.TaskStateData{From: from, To: domain.TaskAttention, Reason: reason}}); err != nil {
		return err
	}
	if err := s.bus.PublishChecked(domain.Event{TaskID: id, Type: domain.EventTaskAttention, Timestamp: now,
		Data: map[string]string{"reason": reason}}); err != nil {
		return err
	}
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
	updated := cloneTask(t)
	updated.Status = domain.TaskFailed
	updated.ExitCode = &exitCode
	updated.Error = message
	updated.UpdatedAt = time.Now().UTC()
	if s.store != nil {
		if err := s.store.PutTask(updated); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	*t = updated
	s.mu.Unlock()
	if err := s.bus.PublishChecked(domain.Event{
		TaskID: id, Type: domain.EventTaskState, Timestamp: updated.UpdatedAt,
		Data: domain.TaskStateData{From: from, To: domain.TaskFailed, Reason: message},
	}); err != nil {
		return err
	}
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
	updated := cloneTask(t)
	updated.Status = to
	updated.UpdatedAt = time.Now().UTC()
	if to == domain.TaskRunning && (from == domain.TaskWaiting || from == domain.TaskAttention) {
		updated.Error = ""
		updated.ExitCode = nil
	}
	if s.store != nil {
		if err := s.store.PutTask(updated); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	*t = updated
	s.mu.Unlock()
	if err := s.bus.PublishChecked(domain.Event{
		TaskID: id, Type: domain.EventTaskState, Timestamp: updated.UpdatedAt,
		Data: domain.TaskStateData{From: from, To: to, Reason: reason},
	}); err != nil {
		return err
	}
	return nil
}

func (s *Service) setTaskWorktree(id string, prepared worktree.Prepared) error {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return ErrTaskNotFound
	}
	updated := cloneTask(t)
	updated.SourceCWD = prepared.SourceCWD
	updated.WorktreeRoot = prepared.Root
	updated.CWD = prepared.CWD
	updated.BaseRevision = prepared.BaseRevision
	updated.UpdatedAt = time.Now().UTC()
	if s.store != nil {
		if err := s.store.PutTask(updated); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	*t = updated
	s.mu.Unlock()
	return s.bus.PublishChecked(domain.Event{
		TaskID: id, Type: domain.EventTaskWorkspace, Timestamp: updated.UpdatedAt,
		Data: map[string]string{
			"source_workspace": prepared.SourceCWD, "source_root": prepared.SourceRoot, "worktree": prepared.Root,
			"workspace": prepared.CWD, "base_revision": prepared.BaseRevision,
		},
	})
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
