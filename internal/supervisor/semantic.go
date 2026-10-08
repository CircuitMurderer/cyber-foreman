package supervisor

import (
	"context"
	"encoding/json"
	"time"
)

type SemanticVerdict string
type SemanticReviewPhase string

const (
	SemanticPass      SemanticVerdict = "pass"
	SemanticRedirect  SemanticVerdict = "redirect"
	SemanticUncertain SemanticVerdict = "uncertain"
	SemanticAttention SemanticVerdict = "attention"

	SemanticReviewFinal   SemanticReviewPhase = "final"
	SemanticReviewMidTurn SemanticReviewPhase = "mid_turn"
)

type SemanticReviewRequest struct {
	TaskID              string
	TrustedInstructions []string
	CurrentInstruction  string
	AgentResponse       string
	VerificationSummary string
	Toolbox             SemanticToolbox
	Phase               SemanticReviewPhase
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
	Provider           string                `json:"provider"`
	Model              string                `json:"model"`
	ToolCalling        bool                  `json:"tool_calling,omitempty"`
	AllowWorkspaceDiff bool                  `json:"allow_workspace_diff,omitempty"`
	AllowAttention     bool                  `json:"allow_operator_attention,omitempty"`
	MidTurn            SemanticMidTurnPolicy `json:"mid_turn,omitempty"`
}

type SemanticMidTurnPolicy struct {
	Enabled        bool          `json:"enabled"`
	Interval       time.Duration `json:"interval"`
	MinOutputRunes int           `json:"min_output_runes"`
	MaxReviews     int           `json:"max_reviews"`
}

type SemanticTool string

const (
	SemanticToolInspectTask           SemanticTool = "inspect_task_state"
	SemanticToolInspectRecentActivity SemanticTool = "inspect_recent_activity"
	SemanticToolInspectAgentActivity  SemanticTool = "inspect_agent_activity"
	SemanticToolInspectWorkspaceDiff  SemanticTool = "inspect_workspace_diff"
)

// SemanticToolbox is the narrow, read-only boundary exposed to a semantic
// reviewer. Corrective actions are returned as a SemanticReview and remain
// subject to the normal Decision -> Executor -> budget path.
type SemanticToolbox interface {
	SupportedTools() []SemanticTool
	ExecuteSemanticTool(context.Context, SemanticTool, json.RawMessage) (string, error)
}
