package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	processadapter "cyber-foreman/internal/agent/process"
	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/event"
	"cyber-foreman/internal/verification"
)

func TestServiceRunsConfiguredCompletionGate(t *testing.T) {
	tests := []struct {
		name       string
		verifyMode string
		want       domain.TaskStatus
	}{
		{name: "pass", verifyMode: "ok", want: domain.TaskCompleted},
		{name: "fail", verifyMode: "fail", want: domain.TaskAttention},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			service := NewService(ctx, processadapter.NewAdapter(), event.NewBus())
			task, err := service.StartTask(StartTaskRequest{
				Command: helperProcessCommand(t, "ok"), CWD: t.TempDir(),
				Verification: VerificationRequest{Commands: []verification.Command{{
					Argv: helperProcessCommand(t, test.verifyMode), Timeout: 2 * time.Second,
				}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			final := waitForTerminalTask(t, service, task.ID)
			if final.Status != test.want {
				t.Fatalf("status = %q, want %q; error = %q", final.Status, test.want, final.Error)
			}
			if test.want == domain.TaskAttention && final.Error == "" {
				t.Fatal("attention-required task is missing its reason")
			}
		})
	}
}

func TestServiceWorkspaceGateRejectsSensitiveChange(t *testing.T) {
	repo := appTestRepository(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := NewService(ctx, processadapter.NewAdapter(), event.NewBus())
	task, err := service.StartTask(StartTaskRequest{
		Command: helperProcessCommand(t, "write-sensitive"), CWD: repo,
		Verification: VerificationRequest{Workspace: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForTerminalTask(t, service, task.ID)
	if final.Status != domain.TaskAttention {
		t.Fatalf("status = %q, want attention_required", final.Status)
	}
	if _, err := os.Stat(filepath.Join(repo, ".api_key")); err != nil {
		t.Fatalf("workspace verifier modified the sensitive file: %v", err)
	}
}

func TestServiceHelperProcess(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "ok":
		os.Exit(0)
	case "fail":
		os.Exit(7)
	case "write-sensitive":
		if err := os.WriteFile(".api_key", []byte("test-secret"), 0o600); err != nil {
			os.Exit(8)
		}
		os.Exit(0)
	default:
		os.Exit(9)
	}
}

func helperProcessCommand(t *testing.T, mode string) []string {
	t.Helper()
	binary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return []string{binary, "-test.run=TestServiceHelperProcess", "--", mode}
}

func waitForTerminalTask(t *testing.T, service *Service, taskID string) domain.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := service.GetTask(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status.Terminal() {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach a terminal status", taskID)
	return domain.Task{}
}

func appTestRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runAppGit(t, repo, "init", "-q")
	runAppGit(t, repo, "config", "user.name", "Cyber Foreman Test")
	runAppGit(t, repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runAppGit(t, repo, "add", "tracked.txt")
	runAppGit(t, repo, "commit", "-q", "-m", "base")
	return repo
}

func runAppGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
