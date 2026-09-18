package api

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"cyber-foreman/internal/app"
	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/supervisor"
	"cyber-foreman/internal/verification"
)

type createTaskRequest struct {
	Kind         string              `json:"kind,omitempty"`
	Adapter      string              `json:"adapter"`
	Workspace    string              `json:"workspace,omitempty"`
	Input        taskInput           `json:"input"`
	Model        string              `json:"model,omitempty"`
	Supervision  *supervisionPolicy  `json:"supervision,omitempty"`
	Verification verificationRequest `json:"verification,omitempty"`
}

type taskInput struct {
	Prompt  string   `json:"prompt,omitempty"`
	Command []string `json:"command,omitempty"`
}

type supervisionPolicy struct {
	IdleTimeout    string `json:"idle_timeout,omitempty"`
	HardTimeout    string `json:"hard_timeout,omitempty"`
	MaxNudges      *int   `json:"max_nudges,omitempty"`
	MaxRetries     *int   `json:"max_retries,omitempty"`
	MaxTestRepairs *int   `json:"max_test_repairs,omitempty"`
}

type verificationRequest struct {
	Workspace       bool            `json:"workspace,omitempty"`
	Commands        []command       `json:"commands,omitempty"`
	WorkspacePolicy workspacePolicy `json:"workspace_policy,omitempty"`
}

type command struct {
	Argv    []string `json:"argv"`
	Timeout string   `json:"timeout,omitempty"`
}

type workspacePolicy struct {
	IgnoredPaths   []string `json:"ignored_paths,omitempty"`
	SensitivePaths []string `json:"sensitive_paths,omitempty"`
	MaxFiles       int      `json:"max_files,omitempty"`
	MaxFileSize    int64    `json:"max_file_size,omitempty"`
	MaxTotalSize   int64    `json:"max_total_size,omitempty"`
	RequireChanges bool     `json:"require_changes,omitempty"`
}

type taskActionRequest struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

type taskResponse struct {
	ID        string            `json:"id"`
	Kind      domain.TaskKind   `json:"kind"`
	Adapter   string            `json:"adapter"`
	Workspace string            `json:"workspace,omitempty"`
	Status    domain.TaskStatus `json:"status"`
	ExitCode  *int              `json:"exit_code,omitempty"`
	Error     string            `json:"error,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Links     taskLinks         `json:"links"`
}

type taskLinks struct {
	Self    string `json:"self"`
	Events  string `json:"events"`
	Actions string `json:"actions"`
}

func newTaskResponse(task domain.Task) taskResponse {
	base := "/api/v1/tasks/" + task.ID
	return taskResponse{
		ID: task.ID, Kind: task.Kind, Adapter: task.Adapter, Workspace: task.CWD,
		Status: task.Status, ExitCode: task.ExitCode, Error: task.Error,
		CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		Links: taskLinks{Self: base, Events: base + "/events", Actions: base + "/actions"},
	}
}

func (r createTaskRequest) appRequest() (app.StartTaskRequest, error) {
	if strings.TrimSpace(r.Adapter) == "" {
		return app.StartTaskRequest{}, errors.New("adapter is required")
	}
	prompt := strings.TrimSpace(r.Input.Prompt)
	if (len(r.Input.Command) == 0) == (prompt == "") {
		return app.StartTaskRequest{}, errors.New("exactly one of input.prompt or input.command is required")
	}
	inferredKind := string(domain.TaskKindCommand)
	if prompt != "" {
		inferredKind = string(domain.TaskKindAgent)
	}
	if r.Kind != "" && r.Kind != inferredKind {
		return app.StartTaskRequest{}, fmt.Errorf("kind %q does not match task input", r.Kind)
	}
	policy, err := r.Supervision.appPolicy()
	if err != nil {
		return app.StartTaskRequest{}, err
	}
	commands := make([]verification.Command, len(r.Verification.Commands))
	for i, item := range r.Verification.Commands {
		if len(item.Argv) == 0 {
			return app.StartTaskRequest{}, fmt.Errorf("verification.commands[%d].argv is required", i)
		}
		timeout, err := parseOptionalDuration(item.Timeout, fmt.Sprintf("verification.commands[%d].timeout", i))
		if err != nil {
			return app.StartTaskRequest{}, err
		}
		commands[i] = verification.Command{Argv: append([]string(nil), item.Argv...), Timeout: timeout}
	}
	return app.StartTaskRequest{
		Adapter: r.Adapter, Prompt: prompt, Command: append([]string(nil), r.Input.Command...),
		Model: r.Model, CWD: r.Workspace, Supervision: policy,
		Verification: app.VerificationRequest{
			Workspace: r.Verification.Workspace, Commands: commands,
			WorkspacePolicy: verification.WorkspacePolicy{
				IgnoredPaths:   append([]string(nil), r.Verification.WorkspacePolicy.IgnoredPaths...),
				SensitivePaths: append([]string(nil), r.Verification.WorkspacePolicy.SensitivePaths...),
				MaxFiles:       r.Verification.WorkspacePolicy.MaxFiles,
				MaxFileSize:    r.Verification.WorkspacePolicy.MaxFileSize,
				MaxTotalSize:   r.Verification.WorkspacePolicy.MaxTotalSize,
				RequireChanges: r.Verification.WorkspacePolicy.RequireChanges,
			},
		},
	}, nil
}

func (p *supervisionPolicy) appPolicy() (*supervisor.Policy, error) {
	if p == nil {
		return nil, nil
	}
	result := supervisor.DefaultPolicy()
	var err error
	if p.IdleTimeout != "" {
		result.IdleTimeout, err = parseDuration(p.IdleTimeout, "supervision.idle_timeout")
		if err != nil {
			return nil, err
		}
	}
	if p.HardTimeout != "" {
		result.HardTimeout, err = parseDuration(p.HardTimeout, "supervision.hard_timeout")
		if err != nil {
			return nil, err
		}
	}
	for name, value := range map[string]*int{
		"max_nudges": p.MaxNudges, "max_retries": p.MaxRetries, "max_test_repairs": p.MaxTestRepairs,
	} {
		if value != nil && *value < 0 {
			return nil, fmt.Errorf("supervision.%s must not be negative", name)
		}
	}
	if p.MaxNudges != nil {
		result.MaxNudges = *p.MaxNudges
	}
	if p.MaxRetries != nil {
		result.MaxRetries = *p.MaxRetries
	}
	if p.MaxTestRepairs != nil {
		result.MaxTestRepairs = *p.MaxTestRepairs
	}
	return &result, nil
}

func parseOptionalDuration(value, field string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	return parseDuration(value, field)
}

func parseDuration(value, field string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration such as 90s or 10m", field)
	}
	return duration, nil
}
