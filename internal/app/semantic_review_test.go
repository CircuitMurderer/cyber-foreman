package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/event"
	"cyber-foreman/internal/supervisor"
)

func TestSemanticToolboxExposesBoundedControlPlaneFactsAndAuditsCalls(t *testing.T) {
	bus := event.NewBus()
	task := &domain.Task{
		ID: "task-1", Kind: domain.TaskKindAgent, Adapter: "opencode",
		CWD: "/secret/workspace", Status: domain.TaskVerifying,
	}
	service := &Service{bus: bus, tasks: map[string]*domain.Task{task.ID: task}}
	bus.Publish(domain.Event{
		TaskID: task.ID, Type: domain.EventTaskState, Timestamp: time.Now().UTC(),
		Data: domain.TaskStateData{From: domain.TaskRunning, To: domain.TaskVerifying, Reason: "private detail"},
	})
	toolbox := &semanticToolbox{
		service: service, taskID: task.ID,
		snapshot: supervisor.Snapshot{
			TaskID: task.ID, Status: domain.TaskVerifying, Policy: supervisor.DefaultPolicy(),
			Budget: supervisor.Budget{SemanticRedirects: 1},
		},
		report: verificationReport{Required: true, Passed: true},
	}

	result, err := toolbox.ExecuteSemanticTool(context.Background(), supervisor.SemanticToolInspectTask, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, `"adapter":"opencode"`) || strings.Contains(result, "/secret/workspace") {
		t.Fatalf("unsafe or incomplete task result: %s", result)
	}
	recent, err := toolbox.ExecuteSemanticTool(context.Background(), supervisor.SemanticToolInspectRecentActivity, json.RawMessage(`{"limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(recent, "private detail") || !strings.Contains(recent, "task.state") {
		t.Fatalf("recent activity was not content-free: %s", recent)
	}
	events := bus.RecentTaskEvents(task.ID, 10)
	foundAudit := false
	for _, item := range events {
		foundAudit = foundAudit || item.Type == domain.EventSemanticToolCall
	}
	if !foundAudit {
		t.Fatal("semantic tool call was not audited")
	}
}

func TestSemanticToolboxKeepsWorkspaceDiffOptIn(t *testing.T) {
	bus := event.NewBus()
	task := &domain.Task{ID: "task-1", Kind: domain.TaskKindAgent, Status: domain.TaskVerifying, WorktreeRoot: "/tmp/not-used"}
	service := &Service{bus: bus, tasks: map[string]*domain.Task{task.ID: task}}
	toolbox := &semanticToolbox{service: service, taskID: task.ID}
	for _, name := range toolbox.SupportedTools() {
		if name == supervisor.SemanticToolInspectWorkspaceDiff {
			t.Fatal("workspace diff tool was exposed without explicit opt-in")
		}
	}
	if _, err := toolbox.ExecuteSemanticTool(context.Background(), supervisor.SemanticToolInspectWorkspaceDiff, json.RawMessage(`{}`)); err == nil {
		t.Fatal("workspace diff tool executed without explicit opt-in")
	}
}
