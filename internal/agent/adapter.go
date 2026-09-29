package agent

import (
	"context"
	"errors"

	"cyber-foreman/internal/domain"
)

var ErrUnsupported = errors.New("adapter capability is unsupported")

type ImplementationInfo struct {
	Name    string `json:"name,omitempty"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

type Status struct {
	Installed       bool                `json:"installed"`
	Healthy         bool                `json:"healthy"`
	Command         string              `json:"command,omitempty"`
	Version         string              `json:"version,omitempty"`
	ProtocolVersion int                 `json:"protocol_version,omitempty"`
	AgentInfo       *ImplementationInfo `json:"agent_info,omitempty"`
	Error           string              `json:"error,omitempty"`
}

// StatusProvider is optional. Adapters that do not implement it are local
// built-ins and are treated as installed and healthy by the registry.
type StatusProvider interface {
	Status() Status
}

func StatusOf(adapter Adapter) Status {
	if provider, ok := adapter.(StatusProvider); ok {
		return provider.Status()
	}
	return Status{Installed: true, Healthy: true}
}

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
