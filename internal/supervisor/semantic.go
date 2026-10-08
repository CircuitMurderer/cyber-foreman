package supervisor

import (
	"context"
	"encoding/json"
)

type SemanticVerdict string

const (
	SemanticPass      SemanticVerdict = "pass"
	SemanticRedirect  SemanticVerdict = "redirect"
	SemanticUncertain SemanticVerdict = "uncertain"
)

type SemanticReviewRequest struct {
	TaskID              string
	TrustedInstructions []string
	CurrentInstruction  string
	AgentResponse       string
	VerificationSummary string
	Toolbox             SemanticToolbox
}

type SemanticReview struct {
	Verdict  SemanticVerdict `json:"verdict"`
	Reason   string          `json:"reason"`
	FollowUp string          `json:"follow_up,omitempty"`
	ToolCall string          `json:"-"`
}

type SemanticReviewer interface {
	Review(context.Context, SemanticReviewRequest) (SemanticReview, error)
	Descriptor() SemanticReviewerDescriptor
}

type SemanticReviewerDescriptor struct {
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	ToolCalling        bool   `json:"tool_calling,omitempty"`
	AllowWorkspaceDiff bool   `json:"allow_workspace_diff,omitempty"`
}

type SemanticTool string

const (
	SemanticToolInspectTask           SemanticTool = "inspect_task_state"
	SemanticToolInspectRecentActivity SemanticTool = "inspect_recent_activity"
	SemanticToolInspectWorkspaceDiff  SemanticTool = "inspect_workspace_diff"
)

// SemanticToolbox is the narrow, read-only boundary exposed to a semantic
// reviewer. Corrective actions are returned as a SemanticReview and remain
// subject to the normal Decision -> Executor -> budget path.
type SemanticToolbox interface {
	SupportedTools() []SemanticTool
	ExecuteSemanticTool(context.Context, SemanticTool, json.RawMessage) (string, error)
}
