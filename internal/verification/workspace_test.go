package verification

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestWorkspaceVerifierTracksTaskDeltaFromDirtyBaseline(t *testing.T) {
	repo := newTestRepository(t)
	tracked := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("user change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	verifier := WorkspaceVerifier{}
	baseline, err := verifier.Capture(context.Background(), repo, WorkspacePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := verifier.Verify(context.Background(), baseline, WorkspacePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if !unchanged.Passed || len(unchanged.ChangedFiles) != 0 {
		t.Fatalf("preexisting dirty file was attributed to task: %#v", unchanged)
	}
	if err := os.WriteFile(tracked, []byte("agent change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := verifier.Verify(context.Background(), baseline, WorkspacePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if !changed.Passed || !slices.Contains(changed.ChangedFiles, "tracked.txt") {
		t.Fatalf("task change not detected: %#v", changed)
	}
}

func TestWorkspaceVerifierRejectsSensitivePathAndHeadChange(t *testing.T) {
	repo := newTestRepository(t)
	verifier := WorkspaceVerifier{}
	baseline, err := verifier.Capture(context.Background(), repo, WorkspacePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".api_key"), []byte("do-not-log-this"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := verifier.Verify(context.Background(), baseline, WorkspacePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || !result.SecurityViolation || !slices.Contains(result.ChangedFiles, ".api_key") {
		t.Fatalf("sensitive path change was accepted: %#v", result)
	}
	for _, violation := range result.Violations {
		if violation == "do-not-log-this" {
			t.Fatal("sensitive contents leaked into result")
		}
	}

	if err := os.Remove(filepath.Join(repo, ".api_key")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "second.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "second.txt")
	runGit(t, repo, "commit", "-m", "second")
	headResult, err := verifier.Verify(context.Background(), baseline, WorkspacePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if headResult.Passed || !headResult.HeadChanged || !headResult.SecurityViolation {
		t.Fatalf("HEAD change was accepted: %#v", headResult)
	}
}

func TestWorkspaceVerifierRejectsDiffCheckFailure(t *testing.T) {
	repo := newTestRepository(t)
	verifier := WorkspaceVerifier{}
	baseline, err := verifier.Capture(context.Background(), repo, WorkspacePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("trailing space \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "tracked.txt")
	result, err := verifier.Verify(context.Background(), baseline, WorkspacePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || result.DiffCheckPassed {
		t.Fatalf("git diff --check failure was accepted: %#v", result)
	}
}

func TestWorkspaceVerifierDoesNotAttributePreexistingDiffIssue(t *testing.T) {
	repo := newTestRepository(t)
	tracked := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("preexisting trailing space \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	verifier := WorkspaceVerifier{}
	baseline, err := verifier.Capture(context.Background(), repo, WorkspacePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := verifier.Verify(context.Background(), baseline, WorkspacePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if !unchanged.Passed {
		t.Fatalf("preexisting diff issue failed verification: %#v", unchanged)
	}
	if err := os.WriteFile(tracked, []byte("different trailing space  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := verifier.Verify(context.Background(), baseline, WorkspacePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Passed || changed.DiffCheckPassed {
		t.Fatalf("new diff issue was accepted: %#v", changed)
	}
}

func newTestRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.name", "Cyber Foreman Test")
	runGit(t, repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-q", "-m", "base")
	return repo
}

func runGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
