package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/supervisor"
)

const maxObservedAgentResponseRunes = 16 * 1024

func (s *Service) reviewAgentTurn(
	ctx context.Context,
	taskID, sessionID string,
	reviewer supervisor.SemanticReviewer,
	instructions []domain.ConversationMessageData,
	currentInstruction, response string,
	report verificationReport,
	snapshot supervisor.Snapshot,
) (supervisor.SemanticReview, bool) {
	if reviewer == nil || strings.TrimSpace(response) == "" {
		return supervisor.SemanticReview{}, false
	}
	descriptor := reviewer.Descriptor()
	startedAt := time.Now().UTC()
	s.bus.Publish(domain.Event{
		TaskID: taskID, SessionID: sessionID, Type: domain.EventSemanticReviewStart, Timestamp: startedAt,
		Data: map[string]any{
			"provider": descriptor.Provider, "model": descriptor.Model,
			"tool_calling": descriptor.ToolCalling, "allow_workspace_diff": descriptor.AllowWorkspaceDiff,
			"allow_operator_attention": descriptor.AllowAttention,
		},
	})
	trusted := make([]string, 0, len(instructions))
	for _, instruction := range instructions {
		if instruction.Source == "operator" {
			trusted = append(trusted, instruction.Text)
		}
	}
	review, err := reviewer.Review(ctx, supervisor.SemanticReviewRequest{
		TaskID: taskID, TrustedInstructions: trusted, CurrentInstruction: currentInstruction,
		AgentResponse: response, VerificationSummary: semanticVerificationSummary(report),
		Toolbox: &semanticToolbox{
			service: s, taskID: taskID, snapshot: snapshot, report: report,
			allowWorkspaceDiff: descriptor.AllowWorkspaceDiff,
		},
	})
	if err == nil && review.Verdict == supervisor.SemanticAttention && !descriptor.AllowAttention {
		err = errors.New("semantic reviewer requested disabled operator attention")
		review = supervisor.SemanticReview{}
	}
	data := map[string]any{
		"provider": descriptor.Provider, "model": descriptor.Model, "success": err == nil,
	}
	if err != nil {
		data["error"] = err.Error()
	} else {
		data["verdict"] = review.Verdict
		data["reason"] = review.Reason
		data["follow_up"] = review.FollowUp
		if review.ToolCall != "" {
			data["tool_call"] = review.ToolCall
		}
	}
	s.bus.Publish(domain.Event{
		TaskID: taskID, SessionID: sessionID, Type: domain.EventSemanticReviewEnd,
		Timestamp: time.Now().UTC(), Data: data,
	})
	return review, err == nil
}

type semanticToolbox struct {
	service            *Service
	taskID             string
	snapshot           supervisor.Snapshot
	report             verificationReport
	allowWorkspaceDiff bool
}

func (t *semanticToolbox) SupportedTools() []supervisor.SemanticTool {
	tools := []supervisor.SemanticTool{
		supervisor.SemanticToolInspectTask,
		supervisor.SemanticToolInspectRecentActivity,
		supervisor.SemanticToolInspectAgentActivity,
	}
	if t.allowWorkspaceDiff {
		if task, err := t.service.GetTask(t.taskID); err == nil && task.WorktreeRoot != "" {
			tools = append(tools, supervisor.SemanticToolInspectWorkspaceDiff)
		}
	}
	return tools
}

func (t *semanticToolbox) ExecuteSemanticTool(ctx context.Context, name supervisor.SemanticTool, arguments json.RawMessage) (string, error) {
	var result string
	var err error
	switch name {
	case supervisor.SemanticToolInspectTask:
		err = decodeSemanticToolArguments(arguments, &struct{}{})
		if err == nil {
			result, err = t.inspectTask()
		}
	case supervisor.SemanticToolInspectRecentActivity:
		var args struct {
			Limit int `json:"limit,omitempty"`
		}
		err = decodeSemanticToolArguments(arguments, &args)
		if err == nil {
			if args.Limit == 0 {
				args.Limit = 10
			}
			if args.Limit < 1 || args.Limit > 20 {
				err = errors.New("limit must be between 1 and 20")
			} else {
				result, err = t.inspectRecentActivity(args.Limit)
			}
		}
	case supervisor.SemanticToolInspectAgentActivity:
		var args struct {
			Limit int `json:"limit,omitempty"`
		}
		err = decodeSemanticToolArguments(arguments, &args)
		if err == nil {
			if args.Limit == 0 {
				args.Limit = 10
			}
			if args.Limit < 1 || args.Limit > 20 {
				err = errors.New("limit must be between 1 and 20")
			} else {
				result, err = t.inspectAgentActivity(args.Limit)
			}
		}
	case supervisor.SemanticToolInspectWorkspaceDiff:
		err = decodeSemanticToolArguments(arguments, &struct{}{})
		if err == nil {
			if !t.allowWorkspaceDiff {
				err = errors.New("workspace diff access is disabled")
			} else {
				result, err = t.inspectWorkspaceDiff(ctx)
			}
		}
	default:
		err = errors.New("semantic tool is not allowed")
	}
	t.service.bus.Publish(domain.Event{
		TaskID: t.taskID, Type: domain.EventSemanticToolCall, Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"tool": name, "success": err == nil, "result_bytes": len(result),
			"error": semanticToolError(err),
		},
	})
	return result, err
}

func (t *semanticToolbox) inspectTask() (string, error) {
	task, err := t.service.GetTask(t.taskID)
	if err != nil {
		return "", err
	}
	return marshalSemanticToolResult(map[string]any{
		"task_id": task.ID, "kind": task.Kind, "adapter": task.Adapter, "status": task.Status,
		"supervision_budget_used": t.snapshot.Budget,
		"supervision_budget_limits": map[string]int{
			"nudges":               t.snapshot.Policy.MaxNudges,
			"retries":              t.snapshot.Policy.MaxRetries,
			"test_repairs":         t.snapshot.Policy.MaxTestRepairs,
			"semantic_redirects":   t.snapshot.Policy.MaxSemanticRedirects,
			"semantic_escalations": t.snapshot.Policy.MaxSemanticEscalations,
		},
		"deterministic_verification": semanticVerificationSummary(t.report),
	})
}

func (t *semanticToolbox) inspectAgentActivity(limit int) (string, error) {
	// Scan the full default replay window so a long stream of text chunks does
	// not hide earlier tool activity from the bounded result.
	events := t.service.bus.RecentTaskEvents(t.taskID, 4096)
	result := make([]map[string]any, 0, limit)
	for index := len(events) - 1; index >= 0 && len(result) < limit; index-- {
		if activity, ok := safeAgentActivity(events[index]); ok {
			result = append(result, activity)
		}
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return marshalSemanticToolResult(map[string]any{"agent_activity": result})
}

func safeAgentActivity(event domain.Event) (map[string]any, bool) {
	if event.Type == domain.EventAgentPermission {
		payload, err := json.Marshal(event.Data)
		if err != nil {
			return nil, false
		}
		var permission domain.AgentPermissionData
		if json.Unmarshal(payload, &permission) != nil {
			return nil, false
		}
		return map[string]any{
			"sequence": event.Sequence, "occurred_at": event.Timestamp,
			"activity": "permission_requested", "option_count": len(permission.Options),
		}, true
	}
	if event.Type != domain.EventAgentSessionUpdate {
		return nil, false
	}
	payload, err := json.Marshal(event.Data)
	if err != nil {
		return nil, false
	}
	var envelope domain.AgentSessionUpdateData
	if json.Unmarshal(payload, &envelope) != nil {
		return nil, false
	}
	var update struct {
		SessionUpdate string `json:"sessionUpdate"`
		Kind          string `json:"kind"`
		Status        string `json:"status"`
		Raw           struct {
			Item struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"item"`
		} `json:"raw"`
	}
	if json.Unmarshal(envelope.Update, &update) != nil ||
		(update.SessionUpdate != "tool_call" && update.SessionUpdate != "tool_call_update") {
		return nil, false
	}
	if update.Kind == "" {
		update.Kind = update.Raw.Item.Type
	}
	if update.Status == "" {
		update.Status = update.Raw.Item.Status
	}
	result := map[string]any{
		"sequence": event.Sequence, "occurred_at": event.Timestamp,
		"activity": update.SessionUpdate,
	}
	if update.Kind != "" {
		result["kind"] = boundSemanticMetadata(update.Kind)
	}
	if update.Status != "" {
		result["status"] = boundSemanticMetadata(update.Status)
	}
	return result, true
}

func boundSemanticMetadata(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 128 {
		runes = runes[:128]
	}
	return string(runes)
}

func (t *semanticToolbox) inspectRecentActivity(limit int) (string, error) {
	events := t.service.bus.RecentTaskEvents(t.taskID, limit)
	result := make([]map[string]any, 0, len(events))
	for _, event := range events {
		item := map[string]any{
			"sequence": event.Sequence, "type": event.Type, "occurred_at": event.Timestamp,
		}
		if summary := semanticEventSummary(event); summary != nil {
			item["summary"] = summary
		}
		result = append(result, item)
	}
	return marshalSemanticToolResult(map[string]any{"events": result})
}

func (t *semanticToolbox) inspectWorkspaceDiff(ctx context.Context) (string, error) {
	diff, err := t.service.TaskDiff(ctx, t.taskID)
	if err != nil {
		return "", err
	}
	const maxPatchRunes = 12 * 1024
	patch := []rune(diff.Patch)
	if len(patch) > maxPatchRunes {
		patch = append(patch[:maxPatchRunes], []rune("\n(diff truncated for semantic review)")...)
		diff.Truncated = true
	}
	return marshalSemanticToolResult(map[string]any{
		"files": diff.Files, "patch": string(patch), "truncated": diff.Truncated,
	})
}

func semanticEventSummary(event domain.Event) any {
	payload, err := json.Marshal(event.Data)
	if err != nil {
		return nil
	}
	switch event.Type {
	case domain.EventTaskState:
		var value struct {
			From domain.TaskStatus `json:"from"`
			To   domain.TaskStatus `json:"to"`
		}
		if json.Unmarshal(payload, &value) == nil {
			return map[string]any{"from": value.From, "to": value.To}
		}
	case domain.EventSupervisorDecision:
		var value struct {
			Action supervisor.Action `json:"action"`
			RuleID string            `json:"rule_id"`
		}
		if json.Unmarshal(payload, &value) == nil {
			return map[string]any{"rule_id": value.RuleID, "action": value.Action}
		}
	case domain.EventVerificationFinish:
		var value struct {
			Verifier string `json:"verifier"`
			Passed   bool   `json:"passed"`
		}
		if json.Unmarshal(payload, &value) == nil {
			return map[string]any{"verifier": value.Verifier, "passed": value.Passed}
		}
	case domain.EventSemanticReviewEnd:
		var value struct {
			Verdict supervisor.SemanticVerdict `json:"verdict"`
			Success bool                       `json:"success"`
		}
		if json.Unmarshal(payload, &value) == nil {
			return map[string]any{"success": value.Success, "verdict": value.Verdict}
		}
	}
	return nil
}

func decodeSemanticToolArguments(raw json.RawMessage, target any) error {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("tool arguments must contain one JSON object")
	}
	return nil
}

func marshalSemanticToolResult(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func semanticToolError(err error) string {
	if err == nil {
		return ""
	}
	message := []rune(err.Error())
	if len(message) > 256 {
		message = message[:256]
	}
	return string(message)
}

func agentResponseChunk(event domain.Event) string {
	if event.Type == domain.EventAgentOutput {
		if output, ok := event.Data.(domain.AgentOutputData); ok {
			return output.Line
		}
		return ""
	}
	if event.Type != domain.EventAgentSessionUpdate {
		return ""
	}
	data, ok := event.Data.(domain.AgentSessionUpdateData)
	if !ok {
		return ""
	}
	var update struct {
		SessionUpdate string `json:"sessionUpdate"`
		Content       struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data.Update, &update); err != nil || update.SessionUpdate != "agent_message_chunk" {
		return ""
	}
	return update.Content.Text
}

func appendBoundedResponse(builder *strings.Builder, runeCount *int, chunk string) {
	remaining := maxObservedAgentResponseRunes - *runeCount
	if remaining <= 0 || chunk == "" {
		return
	}
	runes := []rune(chunk)
	if len(runes) > remaining {
		runes = runes[:remaining]
	}
	builder.WriteString(string(runes))
	*runeCount += len(runes)
}

func semanticVerificationSummary(report verificationReport) string {
	if !report.Required {
		return "No deterministic verifier was configured."
	}
	parts := make([]string, 0, 2)
	if report.Workspace != nil {
		parts = append(parts, fmt.Sprintf("workspace verification passed=%t", report.Workspace.Passed && report.WorkspaceError == ""))
	}
	if report.Test != nil {
		parts = append(parts, fmt.Sprintf("configured command verification passed=%t", report.Test.Passed))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("deterministic verification passed=%t", report.Passed)
	}
	return strings.Join(parts, "; ")
}

func semanticRedirectPrompt(review supervisor.SemanticReview) string {
	return "辅助监工在确定性门禁通过后发现当前回复可能偏离任务。请根据以下具体问题继续修正；不要撤销已经正确完成的工作，完成后照常结束本轮。\n\n" +
		"<review_reason>\n" + review.Reason + "\n</review_reason>\n\n" +
		"<requested_follow_up>\n" + review.FollowUp + "\n</requested_follow_up>"
}
