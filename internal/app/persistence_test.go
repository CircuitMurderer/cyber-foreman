package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"cyber-foreman/internal/agent"
	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/event"
	sqlitestore "cyber-foreman/internal/storage/sqlite"
)

func TestServiceRestoresTasksAndMarksInterruptedWorkForAttention(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "foreman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	for _, task := range []domain.Task{
		{ID: "running", Kind: domain.TaskKindAgent, Adapter: "test", Status: domain.TaskRunning, CreatedAt: now, UpdatedAt: now},
		{ID: "done", Kind: domain.TaskKindAgent, Adapter: "test", Status: domain.TaskCompleted, CreatedAt: now, UpdatedAt: now},
	} {
		if err := store.PutTask(task); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := agent.NewRegistry(restoredTestAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	bus := event.NewBusWithJournal(store)
	service, err := NewServiceWithRegistryAndStore(context.Background(), registry, "test", bus, store)
	if err != nil {
		t.Fatal(err)
	}
	running, err := service.GetTask("running")
	if err != nil {
		t.Fatal(err)
	}
	if running.Status != domain.TaskAttention || running.Error == "" {
		t.Fatalf("running task was not recovered safely: %#v", running)
	}
	done, err := service.GetTask("done")
	if err != nil || done.Status != domain.TaskCompleted {
		t.Fatalf("completed task changed: %#v err=%v", done, err)
	}
	events, err := store.EventsAfter(0, "running")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != domain.EventTaskState || events[1].Type != domain.EventTaskAttention {
		t.Fatalf("unexpected recovery events: %#v", events)
	}
}

func TestServiceDeletesTerminalTaskAndDurableHistory(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "foreman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	task := domain.Task{
		ID: "delete-me", Kind: domain.TaskKindAgent, Adapter: "test",
		Status: domain.TaskCompleted, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.PutTask(task); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(domain.Event{TaskID: task.ID, Type: domain.EventTaskCreated, Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	registry, err := agent.NewRegistry(restoredTestAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	bus := event.NewBusWithJournal(store)
	service, err := NewServiceWithRegistryAndStore(context.Background(), registry, "test", bus, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetTask(task.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("GetTask error=%v, want ErrTaskNotFound", err)
	}
	events, err := store.EventsAfter(0, task.ID)
	if err != nil || len(events) != 0 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

type restoredTestAdapter struct{}

func (restoredTestAdapter) Name() string { return "test" }
func (restoredTestAdapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{Prompt: true}
}
func (restoredTestAdapter) Start(context.Context, agent.StartRequest) (agent.Session, error) {
	return agent.Session{}, agent.ErrUnsupported
}
func (restoredTestAdapter) Events(context.Context, string) (<-chan domain.Event, error) {
	return nil, agent.ErrUnsupported
}
func (restoredTestAdapter) Prompt(context.Context, string, agent.PromptRequest) (agent.PromptResult, error) {
	return agent.PromptResult{}, agent.ErrUnsupported
}
func (restoredTestAdapter) Cancel(context.Context, string) error { return agent.ErrUnsupported }
func (restoredTestAdapter) SetConfigOption(context.Context, string, agent.ConfigOption) error {
	return agent.ErrUnsupported
}
func (restoredTestAdapter) Stop(context.Context, string) error { return nil }
