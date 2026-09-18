package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cyber-foreman/internal/agent"
	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/supervisor"
)

type promptOutcome struct {
	result agent.PromptResult
	err    error
}

func (s *Service) consumePrompt(ctx context.Context, taskID string, events <-chan domain.Event) {
	s.mu.RLock()
	runtime, ok := s.runtimes[taskID]
	s.mu.RUnlock()
	if !ok {
		_ = s.fail(taskID, -1, "prompt runtime is missing")
		return
	}
	defer func() {
		runtime.cancel()
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = runtime.adapter.Stop(stopCtx, runtime.sessionID)
		s.mu.Lock()
		delete(s.runtimes, taskID)
		s.mu.Unlock()
	}()

	now := time.Now().UTC()
	snapshot := supervisor.Snapshot{
		TaskID: taskID, Status: domain.TaskRunning, Policy: runtime.policy,
		StartedAt: now, LastProgressAt: now, AppliedDecisions: make(map[string]bool),
	}
	outcomes := make(chan promptOutcome, 1)
	promptActive := false
	startPrompt := func(text string) {
		promptActive = true
		snapshot.LastProgressAt = time.Now().UTC()
		go func() {
			result, err := runtime.adapter.Prompt(ctx, runtime.sessionID, agent.PromptRequest{Text: text})
			outcomes <- promptOutcome{result: result, err: err}
		}()
	}

	idleTimer, idleC := taskTimer(runtime.policy.IdleTimeout)
	if idleTimer != nil {
		defer idleTimer.Stop()
	}
	hardTimer, hardC := taskTimer(runtime.policy.HardTimeout)
	if hardTimer != nil {
		defer hardTimer.Stop()
	}

	engine := supervisor.Engine{}
	executor := supervisor.Executor{Publisher: s.bus}
	performer := promptActionPerformer{adapter: runtime.adapter, sessionID: runtime.sessionID}
	sequence := 0
	pendingFollowUp := ""
	manualInterrupted := false
	startPrompt(runtime.prompt)

	for {
		select {
		case action := <-runtime.actions:
			if action.kind != taskActionInterrupt || !promptActive || pendingFollowUp != "" {
				action.result <- ErrActionUnavailable
				continue
			}
			sequence++
			decision := supervisor.Decision{
				RuleID: "operator-interrupt", Action: supervisor.ActionCancelAndFollowUp,
				Reason:    "operator interrupted the active agent turn",
				Evidence:  []string{fmt.Sprintf("%s:operator-action:%d", taskID, sequence)},
				DedupeKey: fmt.Sprintf("%s:operator-action:%d", taskID, sequence),
			}
			next, _, err := executor.Execute(ctx, snapshot, decision, performer)
			if err != nil {
				action.result <- err
				continue
			}
			snapshot = next
			pendingFollowUp = action.message
			idleC = nil
			s.bus.Publish(domain.Event{
				TaskID: taskID, SessionID: runtime.sessionID, Type: domain.EventAgentInterrupt,
				Timestamp: time.Now().UTC(), Data: map[string]string{"rule_id": decision.RuleID},
			})
			action.result <- nil
		case event, open := <-events:
			if !open {
				if ctx.Err() == nil {
					_ = s.requireAttention(taskID, "OpenCode ACP event stream disconnected")
				}
				return
			}
			s.bus.Publish(event)
			if supervisor.IsMeaningfulProgress(event) {
				snapshot.LastProgressAt = event.Timestamp
				if snapshot.LastProgressAt.IsZero() {
					snapshot.LastProgressAt = time.Now().UTC()
				}
				if pendingFollowUp == "" {
					idleC = resetTaskTimer(idleTimer, runtime.policy.IdleTimeout)
				}
			}
			if runtime.interruptWith != "" && !manualInterrupted && promptActive &&
				supervisor.IsAgentMessageChunk(event) {
				sequence++
				decision := supervisor.Decision{
					RuleID: "operator-interrupt", Action: supervisor.ActionCancelAndFollowUp,
					Reason:    "operator requested an interrupt after the first agent output",
					Evidence:  []string{fmt.Sprintf("%s:event:%d", taskID, sequence)},
					DedupeKey: fmt.Sprintf("%s:operator-interrupt", taskID),
				}
				next, _, err := executor.Execute(ctx, snapshot, decision, performer)
				if err != nil {
					_ = s.requireAttention(taskID, "operator interrupt failed: "+err.Error())
					return
				}
				snapshot = next
				pendingFollowUp = runtime.interruptWith
				manualInterrupted = true
				idleC = nil
				s.bus.Publish(domain.Event{
					TaskID: taskID, SessionID: runtime.sessionID, Type: domain.EventAgentInterrupt,
					Timestamp: time.Now().UTC(), Data: map[string]string{"rule_id": decision.RuleID},
				})
			}
		case outcome := <-outcomes:
			promptActive = false
			if pendingFollowUp != "" && ctx.Err() == nil {
				followUp := pendingFollowUp
				pendingFollowUp = ""
				s.bus.Publish(domain.Event{
					TaskID: taskID, SessionID: runtime.sessionID, Type: domain.EventAgentFollowUp,
					Timestamp: time.Now().UTC(), Data: map[string]any{"stop_reason": outcome.result.StopReason},
				})
				startPrompt(followUp)
				idleC = resetTaskTimer(idleTimer, nextIdleDelay(snapshot))
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if outcome.err != nil {
				_ = s.requireAttention(taskID, "OpenCode prompt failed: "+outcome.err.Error())
				return
			}
			sequence++
			observation := supervisor.Observation{
				ID: fmt.Sprintf("%s:turn:%d", taskID, sequence), TaskID: taskID,
				SessionID: runtime.sessionID, Type: supervisor.ObservationAgentTurnFinished,
				Timestamp: time.Now().UTC(), Data: map[string]string{"stop_reason": outcome.result.StopReason},
			}
			decisions := engine.Evaluate(snapshot, observation)
			if len(decisions) != 1 || decisions[0].Action != supervisor.ActionStartVerification {
				_ = s.requireAttention(taskID, "supervisor did not authorize verification")
				return
			}
			next, _, err := executor.Execute(ctx, snapshot, decisions[0], noOpPerformer{supervisor.ActionStartVerification: true})
			if err != nil {
				_ = s.requireAttention(taskID, "start verification action failed: "+err.Error())
				return
			}
			snapshot = next
			if err := s.transition(taskID, domain.TaskVerifying, "agent turn finished"); err != nil {
				return
			}
			s.runVerification(ctx, taskID, runtime)
			return
		case <-idleC:
			idleC = nil
			if !promptActive {
				continue
			}
			sequence++
			snapshot.IdleSequence++
			observation := supervisor.Observation{
				ID: fmt.Sprintf("%s:idle:%d", taskID, snapshot.IdleSequence), TaskID: taskID,
				SessionID: runtime.sessionID, Type: supervisor.ObservationTimerIdle,
				Timestamp: time.Now().UTC(),
			}
			decisions := engine.Evaluate(snapshot, observation)
			if len(decisions) == 0 {
				idleC = resetTaskTimer(idleTimer, runtime.policy.IdleTimeout)
				continue
			}
			decision := decisions[0]
			if decision.Action == supervisor.ActionAttentionRequired {
				_, _, _ = executor.Execute(ctx, snapshot, decision, performer)
				_ = s.requireAttention(taskID, decision.Reason)
				return
			}
			next, _, err := executor.Execute(ctx, snapshot, decision, performer)
			if err != nil {
				_ = s.requireAttention(taskID, "idle recovery failed: "+err.Error())
				return
			}
			snapshot = next
			pendingFollowUp = timeoutFollowUp(decision)
			s.bus.Publish(domain.Event{
				TaskID: taskID, SessionID: runtime.sessionID, Type: domain.EventAgentInterrupt,
				Timestamp: time.Now().UTC(), Data: map[string]string{"rule_id": decision.RuleID},
			})
		case <-hardC:
			hardC = nil
			sequence++
			observation := supervisor.Observation{
				ID: fmt.Sprintf("%s:hard-timeout:%d", taskID, sequence), TaskID: taskID,
				SessionID: runtime.sessionID, Type: supervisor.ObservationTimerHardTimeout,
				Timestamp: time.Now().UTC(),
			}
			decisions := engine.Evaluate(snapshot, observation)
			if len(decisions) > 0 {
				snapshot, _, _ = executor.Execute(ctx, snapshot, decisions[0], performer)
				_ = s.requireAttention(taskID, decisions[0].Reason)
			} else {
				_ = s.requireAttention(taskID, "task exceeded hard timeout")
			}
			return
		case <-ctx.Done():
			return
		}
	}
}

type promptActionPerformer struct {
	adapter   agent.Adapter
	sessionID string
}

func (p promptActionPerformer) Supports(action supervisor.Action) bool {
	capabilities := p.adapter.Capabilities()
	switch action {
	case supervisor.ActionNudge, supervisor.ActionCancelAndFollowUp:
		return capabilities.CancelTurn && capabilities.Prompt
	case supervisor.ActionAttentionRequired, supervisor.ActionStop:
		return true
	default:
		return false
	}
}

func (p promptActionPerformer) Perform(ctx context.Context, decision supervisor.Decision) error {
	switch decision.Action {
	case supervisor.ActionNudge, supervisor.ActionCancelAndFollowUp:
		return p.adapter.Cancel(ctx, p.sessionID)
	case supervisor.ActionAttentionRequired, supervisor.ActionStop:
		cancelErr := p.adapter.Cancel(ctx, p.sessionID)
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		stopErr := p.adapter.Stop(stopCtx, p.sessionID)
		return errors.Join(cancelErr, stopErr)
	default:
		return supervisor.ErrUnsupportedAction
	}
}

type noOpPerformer map[supervisor.Action]bool

func (p noOpPerformer) Supports(action supervisor.Action) bool           { return p[action] }
func (noOpPerformer) Perform(context.Context, supervisor.Decision) error { return nil }

func taskTimer(duration time.Duration) (*time.Timer, <-chan time.Time) {
	if duration <= 0 {
		return nil, nil
	}
	timer := time.NewTimer(duration)
	return timer, timer.C
}

func resetTaskTimer(timer *time.Timer, duration time.Duration) <-chan time.Time {
	if timer == nil || duration <= 0 {
		return nil
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
	return timer.C
}

func timeoutFollowUp(decision supervisor.Decision) string {
	if decision.Action == supervisor.ActionNudge {
		return "监工检测到当前任务长时间没有可观察进展。请简要检查是否卡住或方向错误，然后继续完成原任务；不要重复已经完成的工作。"
	}
	return "监工检测到多次空转，已取消上一轮。请重新评估当前方案，选择一个可验证的下一步继续原任务，并优先运行必要的检查。"
}

func nextIdleDelay(snapshot supervisor.Snapshot) time.Duration {
	base := snapshot.Policy.IdleTimeout
	if base <= 0 {
		return 0
	}
	multiplier := 1 + snapshot.Budget.Nudges + snapshot.Budget.Retries
	return time.Duration(multiplier) * base
}
