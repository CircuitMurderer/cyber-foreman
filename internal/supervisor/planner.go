package supervisor

import "context"

type SupervisionPlanRequest struct {
	TaskPrompt        string `json:"task_prompt"`
	SupervisionIntent string `json:"supervision_intent,omitempty"`
	Adapter           string `json:"adapter,omitempty"`
}

type SupervisionPlan struct {
	Summary      string                  `json:"summary"`
	Rules        SupervisionPlanRules    `json:"rules"`
	Verification SupervisionPlanVerify   `json:"verification"`
	Semantic     SupervisionPlanSemantic `json:"semantic"`
	Warnings     []string                `json:"warnings,omitempty"`
}

type SupervisionPlanRules struct {
	IdleTimeout    string `json:"idle_timeout"`
	HardTimeout    string `json:"hard_timeout"`
	WaitingTimeout string `json:"waiting_timeout"`
	MaxNudges      int    `json:"max_nudges"`
	MaxRetries     int    `json:"max_retries"`
	MaxTestRepairs int    `json:"max_test_repairs"`
}

type SupervisionPlanVerify struct {
	Workspace      bool                    `json:"workspace"`
	RequireChanges bool                    `json:"require_changes"`
	Command        *SupervisionPlanCommand `json:"command,omitempty"`
}

type SupervisionPlanCommand struct {
	Argv    []string `json:"argv"`
	Timeout string   `json:"timeout"`
	Purpose string   `json:"purpose"`
}

type SupervisionPlanSemantic struct {
	FinalReview    bool     `json:"final_review"`
	Criteria       []string `json:"criteria"`
	MaxRedirects   int      `json:"max_redirects"`
	MaxEscalations int      `json:"max_escalations"`
}

type SupervisionPlanner interface {
	PlanSupervision(context.Context, SupervisionPlanRequest) (SupervisionPlan, error)
}
