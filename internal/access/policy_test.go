package access

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"cyber-foreman/internal/app"
)

func TestPolicyAllowsWorkspaceAndCommandPrefixes(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	policy, err := NewPolicy(Config{
		WorkspaceRoots: []string{root}, RestrictWorkspace: true,
		CommandAllowlist: [][]string{{"go", "test"}, {"./scripts/test"}}, RestrictCommands: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = policy.AuthorizeTask(app.TaskAuthorization{
		Workspace: workspace, Command: []string{"go", "test", "./..."},
		VerificationCommands: [][]string{{"./scripts/test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPolicyRejectsWorkspaceEscapeAndCommands(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	policy, err := NewPolicy(Config{
		WorkspaceRoots: []string{root}, RestrictWorkspace: true,
		CommandAllowlist: [][]string{{"go", "test"}}, RestrictCommands: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, request := range map[string]app.TaskAuthorization{
		"workspace":    {Workspace: outside},
		"command":      {Workspace: root, Command: []string{"go", "env"}},
		"verification": {Workspace: root, VerificationCommands: [][]string{{"sh", "-c", "anything"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := policy.AuthorizeTask(request); !errors.Is(err, app.ErrTaskForbidden) {
				t.Fatalf("error=%v, want ErrTaskForbidden", err)
			}
		})
	}
}

func TestExplicitEmptyRestrictionsDeny(t *testing.T) {
	policy, err := NewPolicy(Config{RestrictWorkspace: true, RestrictCommands: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.AuthorizeTask(app.TaskAuthorization{Workspace: t.TempDir()}); !errors.Is(err, app.ErrTaskForbidden) {
		t.Fatalf("workspace error=%v", err)
	}
	policy, err = NewPolicy(Config{RestrictCommands: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.AuthorizeTask(app.TaskAuthorization{Command: []string{"go", "test"}}); !errors.Is(err, app.ErrTaskForbidden) {
		t.Fatalf("command error=%v", err)
	}
}

func TestPolicyRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	policy, err := NewPolicy(Config{WorkspaceRoots: []string{root}, RestrictWorkspace: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.AuthorizeTask(app.TaskAuthorization{Workspace: link}); !errors.Is(err, app.ErrTaskForbidden) {
		t.Fatalf("error=%v, want symlink escape rejection", err)
	}
}
