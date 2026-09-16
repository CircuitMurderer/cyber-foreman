package agent

import (
	"context"

	"cyber-foreman/internal/domain"
)

type Capabilities struct {
	StructuredEvents bool `json:"structured_events"`
	ResumeSession    bool `json:"resume_session"`
	MidTurnMessage   bool `json:"mid_turn_message"`
	CancelTurn       bool `json:"cancel_turn"`
	ToolEvents       bool `json:"tool_events"`
	PermissionEvents bool `json:"permission_events"`
}

type StartRequest struct {
	TaskID  string
	Command []string
	CWD     string
	Env     []string
}

type Session struct {
	ID string
}

type Adapter interface {
	Name() string
	Capabilities() Capabilities
	Start(context.Context, StartRequest) (Session, error)
	Events(context.Context, string) (<-chan domain.Event, error)
	Stop(context.Context, string) error
}
