package app

import (
	"context"
	"encoding/json"
	"fmt"
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
) (supervisor.SemanticReview, bool) {
	if reviewer == nil || strings.TrimSpace(response) == "" {
		return supervisor.SemanticReview{}, false
	}
	descriptor := reviewer.Descriptor()
	startedAt := time.Now().UTC()
	s.bus.Publish(domain.Event{
		TaskID: taskID, SessionID: sessionID, Type: domain.EventSemanticReviewStart, Timestamp: startedAt,
		Data: map[string]any{"provider": descriptor.Provider, "model": descriptor.Model},
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
	})
	data := map[string]any{
		"provider": descriptor.Provider, "model": descriptor.Model, "success": err == nil,
	}
	if err != nil {
		data["error"] = err.Error()
	} else {
		data["verdict"] = review.Verdict
		data["reason"] = review.Reason
		data["follow_up"] = review.FollowUp
	}
	s.bus.Publish(domain.Event{
		TaskID: taskID, SessionID: sessionID, Type: domain.EventSemanticReviewEnd,
		Timestamp: time.Now().UTC(), Data: data,
	})
	return review, err == nil
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
