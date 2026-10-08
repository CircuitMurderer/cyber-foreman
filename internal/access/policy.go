package access

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cyber-foreman/internal/app"
)

type Config struct {
	WorkspaceRoots    []string
	RestrictWorkspace bool
	CommandAllowlist  [][]string
	RestrictCommands  bool
}

type Policy struct {
	workspaceRoots    []string
	restrictWorkspace bool
	commandAllowlist  [][]string
	restrictCommands  bool
}

func NewPolicy(config Config) (*Policy, error) {
	policy := &Policy{
		restrictWorkspace: config.RestrictWorkspace,
		restrictCommands:  config.RestrictCommands,
	}
	for index, root := range config.WorkspaceRoots {
		resolved, err := canonicalDirectory(root)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace root %d: %w", index, err)
		}
		policy.workspaceRoots = append(policy.workspaceRoots, resolved)
	}
	for _, prefix := range config.CommandAllowlist {
		policy.commandAllowlist = append(policy.commandAllowlist, append([]string(nil), prefix...))
	}
	return policy, nil
}

func (p *Policy) AuthorizeTask(request app.TaskAuthorization) error {
	if p == nil {
		return nil
	}
	if p.restrictWorkspace {
		workspace, err := canonicalDirectory(request.Workspace)
		if err != nil {
			return fmt.Errorf("%w: resolve workspace: %v", app.ErrTaskForbidden, err)
		}
		if !withinAnyRoot(workspace, p.workspaceRoots) {
			return fmt.Errorf("%w: workspace %q is outside the configured roots", app.ErrTaskForbidden, request.Workspace)
		}
	}
	if len(request.Command) > 0 && p.restrictCommands && !p.commandAllowed(request.Command) {
		return fmt.Errorf("%w: command %q is not allowlisted", app.ErrTaskForbidden, request.Command[0])
	}
	for index, command := range request.VerificationCommands {
		if p.restrictCommands && !p.commandAllowed(command) {
			name := ""
			if len(command) > 0 {
				name = command[0]
			}
			return fmt.Errorf("%w: verification command %d (%q) is not allowlisted", app.ErrTaskForbidden, index, name)
		}
	}
	return nil
}

func (p *Policy) commandAllowed(command []string) bool {
	for _, prefix := range p.commandAllowlist {
		if len(command) < len(prefix) {
			continue
		}
		matched := true
		for index := range prefix {
			if command[index] != prefix[index] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return filepath.Clean(resolved), nil
}

func withinAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
