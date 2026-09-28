package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"cyber-foreman/internal/domain"
)

func TestStoreRoundTripsTasksAndTypedEvents(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "nested", "foreman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 9, 28, 12, 30, 0, 123, time.UTC)
	exitCode := 7
	task := domain.Task{
		ID: "task-1", Kind: domain.TaskKindAgent, Adapter: "opencode",
		Command: []string{"one", "two"}, CWD: "/workspace", Status: domain.TaskFailed,
		ExitCode: &exitCode, Error: "failed", CreatedAt: now, UpdatedAt: now.Add(time.Second),
	}
	if err := store.PutTask(task); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != task.ID || tasks[0].ExitCode == nil || *tasks[0].ExitCode != exitCode ||
		len(tasks[0].Command) != 2 || !tasks[0].UpdatedAt.Equal(task.UpdatedAt) {
		t.Fatalf("unexpected tasks: %#v", tasks)
	}

	persisted, err := store.AppendEvent(domain.Event{
		TaskID: task.ID, SessionID: "session-1", Type: domain.EventConversationMessage,
		Timestamp: now, Data: domain.ConversationMessageData{Role: "user", Source: "operator", Text: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Sequence != 1 || persisted.ID != "evt-1" || persisted.Version != "v1" {
		t.Fatalf("unexpected event envelope: %#v", persisted)
	}
	if _, err := store.AppendEvent(domain.Event{TaskID: "other", Type: domain.EventAgentOutput, Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	events, err := store.EventsAfter(0, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%#v", events)
	}
	message, ok := events[0].Data.(domain.ConversationMessageData)
	if !ok || message.Text != "hello" || message.Source != "operator" {
		t.Fatalf("unexpected typed data: %#v", events[0].Data)
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreman.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := first.PutTask(domain.Task{
		ID: "task-restart", Kind: domain.TaskKindCommand, Adapter: "process",
		Status: domain.TaskCompleted, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.AppendEvent(domain.Event{TaskID: "task-restart", Type: domain.EventTaskCreated, Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	tasks, err := second.ListTasks()
	if err != nil || len(tasks) != 1 || tasks[0].ID != "task-restart" {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
	event, err := second.AppendEvent(domain.Event{TaskID: "task-restart", Type: domain.EventTaskState, Timestamp: now})
	if err != nil {
		t.Fatal(err)
	}
	if event.Sequence != 2 {
		t.Fatalf("sequence=%d, want 2", event.Sequence)
	}
}

func TestDeleteTaskRemovesSnapshotAndEventHistory(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "foreman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	task := domain.Task{
		ID: "task-delete", Kind: domain.TaskKindAgent, Adapter: "opencode",
		Status: domain.TaskCompleted, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.PutTask(task); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(domain.Event{TaskID: task.ID, Type: domain.EventTaskCreated, Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.EventsAfter(0, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 || len(events) != 0 {
		t.Fatalf("tasks=%#v events=%#v", tasks, events)
	}
}
