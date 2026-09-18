package agent

import (
	"context"
	"errors"

	"cyber-foreman/internal/domain"
)

var ErrUnsupported = errors.New("adapter capability is unsupported")

type Capabilities struct {
	Command          bool `json:"command"`
	StructuredEvents bool `json:"structured_events"`
	ResumeSession    bool `json:"resume_session"`
	Prompt           bool `json:"prompt"`
	MidTurnMessage   bool `json:"mid_turn_message"`
	CancelTurn       bool `json:"cancel_turn"`
	SessionConfig    bool `json:"session_config"`
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

type PromptRequest struct {
	Text string
}

type PromptResult struct {
	StopReason string
}

type ConfigOption struct {
	ID    string
	Value any
}

type Adapter interface {
	Name() string
	Capabilities() Capabilities
	Start(context.Context, StartRequest) (Session, error)
	Events(context.Context, string) (<-chan domain.Event, error)
	Prompt(context.Context, string, PromptRequest) (PromptResult, error)
	Cancel(context.Context, string) error
	SetConfigOption(context.Context, string, ConfigOption) error
	Stop(context.Context, string) error
}
