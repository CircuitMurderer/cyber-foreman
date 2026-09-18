package supervisor

import (
	"context"
	"errors"
	"testing"
	"time"

	"cyber-foreman/internal/domain"
)

func TestExecutorDeduplicatesAndPublishesAuditEvents(t *testing.T) {
	publisher := &recordingPublisher{}
	performer := &recordingPerformer{supported: map[Action]bool{ActionNudge: true}}
	clock := fixedClock{now: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)}
	executor := Executor{Publisher: publisher, Clock: clock}
	snapshot := Snapshot{TaskID: "task-1", Policy: DefaultPolicy()}
	decision := Decision{
		RuleID: "idle-nudge", Action: ActionNudge, DedupeKey: "idle-1:idle-nudge",
		Evidence: []string{"idle-1"}, BudgetCost: BudgetCost{Nudges: 1},
	}

	next, result, err := executor.Execute(context.Background(), snapshot, decision, performer)
	if err != nil || !result.Applied || performer.calls != 1 || next.Budget.Nudges != 1 {
		t.Fatalf("first execution: next=%#v result=%#v calls=%d err=%v", next, result, performer.calls, err)
	}
	if len(publisher.events) != 3 || publisher.events[0].Type != domain.EventSupervisorDecision ||
		publisher.events[2].Type != domain.EventSupervisorFinished {
		t.Fatalf("unexpected audit events: %#v", publisher.events)
	}

	again, result, err := executor.Execute(context.Background(), next, decision, performer)
	if err != nil || !result.Duplicate || performer.calls != 1 || again.Budget.Nudges != 1 {
		t.Fatalf("duplicate execution: next=%#v result=%#v calls=%d err=%v", again, result, performer.calls, err)
	}
	if len(publisher.events) != 3 {
		t.Fatalf("duplicate emitted audit events: %#v", publisher.events)
	}
}

func TestExecutorRejectsUnsupportedActionBeforeChargingBudget(t *testing.T) {
	snapshot := Snapshot{TaskID: "task-1", Policy: DefaultPolicy()}
	decision := Decision{Action: ActionRetrySession, DedupeKey: "retry-1", BudgetCost: BudgetCost{Retries: 1}}
	next, _, err := (Executor{}).Execute(context.Background(), snapshot, decision, &recordingPerformer{})
	if !errors.Is(err, ErrUnsupportedAction) || next.Budget.Retries != 0 {
		t.Fatalf("next=%#v err=%v", next, err)
	}
}

type recordingPublisher struct{ events []domain.Event }

func (p *recordingPublisher) Publish(event domain.Event) { p.events = append(p.events, event) }

type recordingPerformer struct {
	supported map[Action]bool
	calls     int
	err       error
}

func (p *recordingPerformer) Supports(action Action) bool { return p.supported[action] }

func (p *recordingPerformer) Perform(context.Context, Decision) error {
	p.calls++
	return p.err
}
