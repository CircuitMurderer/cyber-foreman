package supervisor

import (
	"context"
	"errors"
	"time"

	"cyber-foreman/internal/domain"
)

var ErrUnsupportedAction = errors.New("supervision action is unsupported")

type EventPublisher interface {
	Publish(domain.Event)
}

type ActionPerformer interface {
	Supports(Action) bool
	Perform(context.Context, Decision) error
}

type ExecutionResult struct {
	Applied   bool   `json:"applied"`
	Duplicate bool   `json:"duplicate"`
	Error     string `json:"error,omitempty"`
}

type Executor struct {
	Publisher EventPublisher
	Clock     Clock
}

func (e Executor) Execute(
	ctx context.Context,
	snapshot Snapshot,
	decision Decision,
	performer ActionPerformer,
) (Snapshot, ExecutionResult, error) {
	if performer == nil || !performer.Supports(decision.Action) {
		return snapshot, ExecutionResult{Error: ErrUnsupportedAction.Error()}, ErrUnsupportedAction
	}
	next, duplicate, err := ApplyDecision(snapshot, decision)
	if err != nil {
		return snapshot, ExecutionResult{Error: err.Error()}, err
	}
	if duplicate {
		return next, ExecutionResult{Duplicate: true}, nil
	}

	now := e.now()
	e.publish(domain.Event{
		TaskID: snapshot.TaskID, Type: domain.EventSupervisorDecision, Timestamp: now, Data: decision,
	})
	e.publish(domain.Event{
		TaskID: snapshot.TaskID, Type: domain.EventSupervisorStarted, Timestamp: now,
		Data: map[string]any{"rule_id": decision.RuleID, "action": decision.Action, "dedupe_key": decision.DedupeKey},
	})
	performErr := performer.Perform(ctx, decision)
	result := ExecutionResult{Applied: true}
	if performErr != nil {
		result.Error = performErr.Error()
	}
	e.publish(domain.Event{
		TaskID: snapshot.TaskID, Type: domain.EventSupervisorFinished, Timestamp: e.now(),
		Data: map[string]any{
			"rule_id": decision.RuleID, "action": decision.Action,
			"dedupe_key": decision.DedupeKey, "success": performErr == nil, "error": result.Error,
		},
	})
	return next, result, performErr
}

func (e Executor) now() time.Time {
	if e.Clock != nil {
		return e.Clock.Now().UTC()
	}
	return time.Now().UTC()
}

func (e Executor) publish(event domain.Event) {
	if e.Publisher != nil {
		e.Publisher.Publish(event)
	}
}
