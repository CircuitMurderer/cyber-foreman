package supervisor

import "context"

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
}

type SemanticReview struct {
	Verdict  SemanticVerdict `json:"verdict"`
	Reason   string          `json:"reason"`
	FollowUp string          `json:"follow_up,omitempty"`
}

type SemanticReviewer interface {
	Review(context.Context, SemanticReviewRequest) (SemanticReview, error)
	Descriptor() SemanticReviewerDescriptor
}

type SemanticReviewerDescriptor struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}
