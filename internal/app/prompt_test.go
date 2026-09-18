package app

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyber-foreman/internal/agent"
	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/event"
	"cyber-foreman/internal/supervisor"
)

func TestPromptTaskAutomaticallyInterruptsIdleOpenCode(t *testing.T) {
	adapter := newPromptTestAdapter(promptModeIdleThenComplete)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := event.NewBus()
	events := bus.Subscribe(ctx, 128)
	service := NewService(ctx, adapter, bus)
	policy := supervisor.DefaultPolicy()
	policy.IdleTimeout = 30 * time.Millisecond
	policy.HardTimeout = 2 * time.Second
	policy.MaxNudges = 1
	task, err := service.StartTask(StartTaskRequest{
		Prompt: "original task", Model: "google/test", CWD: t.TempDir(), Supervision: &policy,
	})
	if err != nil {
		t.Fatal(err)
	}

	seenDecision := false
	seenFollowUp := false
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt := <-events:
			if evt.TaskID != task.ID {
				continue
			}
			seenDecision = seenDecision || evt.Type == domain.EventSupervisorDecision
			seenFollowUp = seenFollowUp || evt.Type == domain.EventAgentFollowUp
			if evt.Type == domain.EventTaskState {
				state, ok := evt.Data.(domain.TaskStateData)
				if ok && state.To.Terminal() {
					final, getErr := service.GetTask(task.ID)
					if getErr != nil {
						t.Fatal(getErr)
					}
					if final.Status != domain.TaskCompleted {
						t.Fatalf("status = %q, error = %q", final.Status, final.Error)
					}
					if !seenDecision || !seenFollowUp || adapter.cancelCalls.Load() != 1 {
						t.Fatalf("decision=%v follow-up=%v cancel calls=%d", seenDecision, seenFollowUp, adapter.cancelCalls.Load())
					}
					prompts := adapter.recordedPrompts()
					if len(prompts) != 2 || prompts[0] != "original task" || !strings.Contains(prompts[1], "没有可观察进展") {
						t.Fatalf("unexpected prompts: %#v", prompts)
					}
					return
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for supervised prompt task")
		}
	}
}

func TestPromptTaskHardTimeoutRequiresAttention(t *testing.T) {
	adapter := newPromptTestAdapter(promptModeNeverComplete)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := NewService(ctx, adapter, event.NewBus())
	policy := supervisor.DefaultPolicy()
	policy.IdleTimeout = 0
	policy.HardTimeout = 40 * time.Millisecond
	task, err := service.StartTask(StartTaskRequest{
		Prompt: "never finish", CWD: t.TempDir(), Supervision: &policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForTerminalTask(t, service, task.ID)
	if final.Status != domain.TaskAttention || !strings.Contains(final.Error, "hard timeout") {
		t.Fatalf("unexpected final task: %#v", final)
	}
}

func TestPromptTaskOperatorInterruptUsesServiceMailbox(t *testing.T) {
	adapter := newPromptTestAdapter(promptModeMessageThenComplete)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := NewService(ctx, adapter, event.NewBus())
	policy := supervisor.DefaultPolicy()
	policy.IdleTimeout = time.Second
	policy.HardTimeout = 2 * time.Second
	task, err := service.StartTask(StartTaskRequest{
		Prompt: "remember context", InterruptWith: "operator follow-up", CWD: t.TempDir(), Supervision: &policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForTerminalTask(t, service, task.ID)
	if final.Status != domain.TaskCompleted || adapter.cancelCalls.Load() != 1 {
		t.Fatalf("final=%#v cancel calls=%d", final, adapter.cancelCalls.Load())
	}
	prompts := adapter.recordedPrompts()
	if len(prompts) != 2 || prompts[1] != "operator follow-up" {
		t.Fatalf("unexpected prompts: %#v", prompts)
	}
}

func TestNextIdleDelayBacksOffAfterIntervention(t *testing.T) {
	snapshot := supervisor.Snapshot{
		Policy: supervisor.Policy{IdleTimeout: time.Second},
		Budget: supervisor.Budget{Nudges: 1, Retries: 1},
	}
	if got := nextIdleDelay(snapshot); got != 3*time.Second {
		t.Fatalf("nextIdleDelay() = %s, want 3s", got)
	}
}

type promptTestMode int

const (
	promptModeIdleThenComplete promptTestMode = iota
	promptModeNeverComplete
	promptModeMessageThenComplete
)

type promptTestAdapter struct {
	mode        promptTestMode
	events      chan domain.Event
	cancel      chan struct{}
	stopOnce    sync.Once
	taskID      string
	promptMu    sync.Mutex
	prompts     []string
	promptCalls atomic.Int32
	cancelCalls atomic.Int32
	model       string
}

func newPromptTestAdapter(mode promptTestMode) *promptTestAdapter {
	return &promptTestAdapter{mode: mode, events: make(chan domain.Event, 32), cancel: make(chan struct{}, 4)}
}

func (a *promptTestAdapter) Name() string { return "fake-opencode" }

func (a *promptTestAdapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{StructuredEvents: true, Prompt: true, CancelTurn: true, SessionConfig: true}
}

func (a *promptTestAdapter) Start(_ context.Context, req agent.StartRequest) (agent.Session, error) {
	a.taskID = req.TaskID
	a.events <- domain.Event{TaskID: req.TaskID, SessionID: "session-test", Type: domain.EventAgentStarted, Timestamp: time.Now().UTC()}
	return agent.Session{ID: "session-test"}, nil
}

func (a *promptTestAdapter) Events(context.Context, string) (<-chan domain.Event, error) {
	return a.events, nil
}

func (a *promptTestAdapter) Prompt(ctx context.Context, _ string, req agent.PromptRequest) (agent.PromptResult, error) {
	a.promptMu.Lock()
	a.prompts = append(a.prompts, req.Text)
	a.promptMu.Unlock()
	call := a.promptCalls.Add(1)
	if (a.mode == promptModeIdleThenComplete || a.mode == promptModeMessageThenComplete) && call > 1 {
		a.events <- domain.Event{
			TaskID: a.taskID, SessionID: "session-test", Type: domain.EventAgentSessionUpdate, Timestamp: time.Now().UTC(),
			Data: domain.AgentSessionUpdateData{Update: []byte(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"done"}}`)},
		}
		return agent.PromptResult{StopReason: "end_turn"}, nil
	}
	if a.mode == promptModeMessageThenComplete {
		a.events <- domain.Event{
			TaskID: a.taskID, SessionID: "session-test", Type: domain.EventAgentSessionUpdate, Timestamp: time.Now().UTC(),
			Data: domain.AgentSessionUpdateData{Update: []byte(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"first"}}`)},
		}
	}
	select {
	case <-a.cancel:
		return agent.PromptResult{StopReason: "cancelled"}, nil
	case <-ctx.Done():
		return agent.PromptResult{}, ctx.Err()
	}
}

func (a *promptTestAdapter) Cancel(context.Context, string) error {
	a.cancelCalls.Add(1)
	select {
	case a.cancel <- struct{}{}:
	default:
	}
	return nil
}

func (a *promptTestAdapter) SetConfigOption(_ context.Context, _ string, option agent.ConfigOption) error {
	if option.ID == "model" {
		a.model, _ = option.Value.(string)
	}
	return nil
}

func (a *promptTestAdapter) Stop(context.Context, string) error {
	a.stopOnce.Do(func() {
		select {
		case a.cancel <- struct{}{}:
		default:
		}
		close(a.events)
	})
	return nil
}

func (a *promptTestAdapter) recordedPrompts() []string {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	return append([]string(nil), a.prompts...)
}
