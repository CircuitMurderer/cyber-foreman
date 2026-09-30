package agent

import (
	"context"
	"errors"
	"strings"

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

type Metadata struct {
	Selectable       bool   `json:"selectable"`
	Driver           string `json:"driver,omitempty"`
	ProviderFormat   string `json:"provider_format,omitempty"`
	DefaultModel     string `json:"default_model,omitempty"`
	DefaultWorkspace string `json:"default_workspace,omitempty"`
}

type MetadataProvider interface {
	Metadata() Metadata
}

type ProbeProvider interface {
	Probe(context.Context) Status
}

func StatusOf(adapter Adapter) Status {
	if provider, ok := adapter.(StatusProvider); ok {
		return provider.Status()
	}
	return Status{Installed: true, Healthy: true}
}

func MetadataOf(adapter Adapter) Metadata {
	if provider, ok := adapter.(MetadataProvider); ok {
		return provider.Metadata()
	}
	return Metadata{}
}

func Probe(ctx context.Context, adapter Adapter) Status {
	if provider, ok := adapter.(ProbeProvider); ok {
		return provider.Probe(ctx)
	}
	return StatusOf(adapter)
}

func WithMetadata(adapter Adapter, metadata Metadata) Adapter {
	metadata.Driver = strings.TrimSpace(metadata.Driver)
	metadata.ProviderFormat = strings.TrimSpace(metadata.ProviderFormat)
	metadata.DefaultModel = strings.TrimSpace(metadata.DefaultModel)
	metadata.DefaultWorkspace = strings.TrimSpace(metadata.DefaultWorkspace)
	return &configuredAdapter{Adapter: adapter, metadata: metadata}
}

type configuredAdapter struct {
	Adapter
	metadata Metadata
}

func (a *configuredAdapter) Metadata() Metadata { return a.metadata }
func (a *configuredAdapter) Status() Status     { return StatusOf(a.Adapter) }
func (a *configuredAdapter) Probe(ctx context.Context) Status {
	return Probe(ctx, a.Adapter)
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
