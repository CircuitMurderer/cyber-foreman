package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareAndReadDiff(t *testing.T) {
	repository := initRepository(t)
	source := filepath.Join(repository, "nested")
	prepared, err := Prepare(context.Background(), source, "task-test")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SourceCWD != source || prepared.SourceRoot != repository || prepared.CWD != filepath.Join(prepared.Root, "nested") || prepared.BaseRevision == "" {
		t.Fatalf("unexpected prepared worktree: %#v", prepared)
	}
	if err := os.WriteFile(filepath.Join(prepared.CWD, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Root, "new.txt"), []byte("new content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Root, ".env"), []byte("SECRET=hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff, err := ReadDiff(context.Background(), prepared.Root, prepared.BaseRevision)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 3 || !strings.Contains(diff.Patch, "changed") || !strings.Contains(diff.Patch, "new content") {
		t.Fatalf("unexpected diff: %#v\n%s", diff.Files, diff.Patch)
	}
	if strings.Contains(diff.Patch, "SECRET=hidden") || !strings.Contains(diff.Patch, "redacted sensitive") {
		t.Fatalf("sensitive content was not redacted:\n%s", diff.Patch)
	}
	original, err := os.ReadFile(filepath.Join(source, "tracked.txt"))
	if err != nil || string(original) != "original\n" {
		t.Fatalf("source workspace changed: %q err=%v", original, err)
	}
}

func TestPrepareRejectsDirtySource(t *testing.T) {
	repository := initRepository(t)
	if err := os.WriteFile(filepath.Join(repository, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), repository, "task-dirty"); err != ErrDirtySource {
		t.Fatalf("error = %v, want ErrDirtySource", err)
	}
}

func TestReadDiffRedactsRenameFromSensitivePath(t *testing.T) {
	repository := initRepository(t)
	prepared, err := Prepare(context.Background(), repository, "task-sensitive-rename")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(prepared.Root, ".env"), filepath.Join(prepared.Root, "public.txt")); err != nil {
		t.Fatal(err)
	}
	diff, err := ReadDiff(context.Background(), prepared.Root, prepared.BaseRevision)
	if err != nil {
		t.Fatal(err)
	}
	allRedacted := len(diff.Files) > 0
	for _, file := range diff.Files {
		allRedacted = allRedacted && file.Redacted
	}
	if !allRedacted || strings.Contains(diff.Patch, "committed-secret") {
		t.Fatalf("sensitive rename leaked content: %#v\n%s", diff.Files, diff.Patch)
	}
}

func initRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "tracked.txt"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=committed-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"},
		{"add", "."}, {"commit", "-qm", "initial"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
