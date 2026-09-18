package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultMaxFiles     = 20_000
	defaultMaxFileSize  = int64(64 * 1024 * 1024)
	defaultMaxTotalSize = int64(512 * 1024 * 1024)
)

type WorkspacePolicy struct {
	IgnoredPaths   []string `json:"ignored_paths,omitempty"`
	SensitivePaths []string `json:"sensitive_paths,omitempty"`
	MaxFiles       int      `json:"max_files,omitempty"`
	MaxFileSize    int64    `json:"max_file_size,omitempty"`
	MaxTotalSize   int64    `json:"max_total_size,omitempty"`
	RequireChanges bool     `json:"require_changes,omitempty"`
}

type FileState struct {
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
	Kind   string `json:"kind"`
}

type WorkspaceBaseline struct {
	Root            string               `json:"root"`
	Head            string               `json:"head"`
	Files           map[string]FileState `json:"files"`
	DiffCheckDigest string               `json:"diff_check_digest"`
}

type WorkspaceResult struct {
	Passed            bool     `json:"passed"`
	SecurityViolation bool     `json:"security_violation"`
	HeadChanged       bool     `json:"head_changed"`
	DiffCheckPassed   bool     `json:"diff_check_passed"`
	ChangedFiles      []string `json:"changed_files,omitempty"`
	Violations        []string `json:"violations,omitempty"`
	DiffOutput        string   `json:"diff_output,omitempty"`
	DiffTruncated     bool     `json:"diff_truncated,omitempty"`
}

type WorkspaceVerifier struct {
	GitBinary   string
	OutputLimit int
}

func (v WorkspaceVerifier) Capture(ctx context.Context, cwd string, policy WorkspacePolicy) (WorkspaceBaseline, error) {
	rootOutput, _, err := v.git(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return WorkspaceBaseline{}, fmt.Errorf("resolve git root: %w", err)
	}
	root, err := filepath.Abs(strings.TrimSpace(rootOutput))
	if err != nil {
		return WorkspaceBaseline{}, fmt.Errorf("resolve absolute git root: %w", err)
	}
	head, _, err := v.git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return WorkspaceBaseline{}, fmt.Errorf("read git HEAD: %w", err)
	}
	files, err := captureFiles(root, normalizeWorkspacePolicy(policy))
	if err != nil {
		return WorkspaceBaseline{}, err
	}
	diffState, err := v.diffCheck(ctx, root)
	if err != nil {
		return WorkspaceBaseline{}, fmt.Errorf("capture git diff check baseline: %w", err)
	}
	return WorkspaceBaseline{
		Root: root, Head: strings.TrimSpace(head), Files: files, DiffCheckDigest: diffState.digest,
	}, nil
}

func (v WorkspaceVerifier) Verify(ctx context.Context, baseline WorkspaceBaseline, policy WorkspacePolicy) (WorkspaceResult, error) {
	if baseline.Root == "" || baseline.Head == "" || baseline.Files == nil || baseline.DiffCheckDigest == "" {
		return WorkspaceResult{}, errors.New("workspace baseline is incomplete")
	}
	current, err := v.Capture(ctx, baseline.Root, policy)
	if err != nil {
		return WorkspaceResult{}, err
	}
	result := WorkspaceResult{DiffCheckPassed: true}
	result.ChangedFiles = compareFiles(baseline.Files, current.Files)
	if current.Head != baseline.Head {
		result.HeadChanged = true
		result.SecurityViolation = true
		result.Violations = append(result.Violations, "git HEAD changed during task")
	}
	policy = normalizeWorkspacePolicy(policy)
	for _, changed := range result.ChangedFiles {
		if matchesAny(changed, policy.SensitivePaths) {
			result.SecurityViolation = true
			result.Violations = append(result.Violations, "sensitive path changed: "+changed)
		}
	}
	diffState, err := v.diffCheck(ctx, baseline.Root)
	if err != nil {
		return WorkspaceResult{}, err
	}
	if diffState.failed && diffState.digest != baseline.DiffCheckDigest {
		result.DiffCheckPassed = false
		result.DiffOutput = redact(diffState.output, nil, nil)
		result.DiffTruncated = diffState.truncated
		result.Violations = append(result.Violations, "git diff --check failed")
	}
	if policy.RequireChanges && len(result.ChangedFiles) == 0 {
		result.Violations = append(result.Violations, "task produced no workspace changes")
	}
	result.Passed = !result.SecurityViolation && result.DiffCheckPassed &&
		(!policy.RequireChanges || len(result.ChangedFiles) > 0)
	sort.Strings(result.Violations)
	return result, nil
}

type workspaceDiffState struct {
	digest    string
	output    string
	failed    bool
	truncated bool
}

func (v WorkspaceVerifier) diffCheck(ctx context.Context, root string) (workspaceDiffState, error) {
	worktreeOutput, worktreeTruncated, worktreeErr := v.git(ctx, root, "diff", "--check", "--no-ext-diff")
	stagedOutput, stagedTruncated, stagedErr := v.git(ctx, root, "diff", "--cached", "--check", "--no-ext-diff")
	if !isDiffCheckResult(worktreeErr) {
		return workspaceDiffState{}, fmt.Errorf("worktree diff check: %w", worktreeErr)
	}
	if !isDiffCheckResult(stagedErr) {
		return workspaceDiffState{}, fmt.Errorf("staged diff check: %w", stagedErr)
	}
	if worktreeTruncated || stagedTruncated {
		return workspaceDiffState{}, errors.New("git diff check output exceeded limit")
	}
	output := strings.TrimSpace(worktreeOutput + "\n" + stagedOutput)
	digest := sha256.Sum256([]byte(output))
	return workspaceDiffState{
		digest: hex.EncodeToString(digest[:]), output: output,
		failed: worktreeErr != nil || stagedErr != nil,
	}, nil
}

func isDiffCheckResult(err error) bool {
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 2
}

func (v WorkspaceVerifier) git(parent context.Context, cwd string, args ...string) (string, bool, error) {
	binary := v.GitBinary
	if binary == "" {
		binary = "git"
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmdArgs := append([]string{"-C", cwd}, args...)
	cmd := exec.CommandContext(ctx, binary, cmdArgs...)
	cmd.Env = (CommandVerifier{}).allowedEnvironment()
	limit := v.OutputLimit
	if limit <= 0 {
		limit = defaultOutputLimit
	}
	output := newTailBuffer(limit)
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return output.String(), output.Truncated(), errors.New("git command timed out")
	}
	return output.String(), output.Truncated(), err
}

func captureFiles(root string, policy WorkspacePolicy) (map[string]FileState, error) {
	files := make(map[string]FileState)
	totalSize := int64(0)
	err := filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if matchesAny(rel, policy.IgnoredPaths) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(filePath)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if len(files) >= policy.MaxFiles {
			return fmt.Errorf("workspace exceeds maximum file count %d", policy.MaxFiles)
		}
		if info.Size() > policy.MaxFileSize {
			return fmt.Errorf("workspace file %s exceeds maximum size", rel)
		}
		totalSize += info.Size()
		if totalSize > policy.MaxTotalSize {
			return fmt.Errorf("workspace exceeds maximum scanned size %d", policy.MaxTotalSize)
		}
		state, err := hashFileState(filePath, info)
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", rel, err)
		}
		files[rel] = state
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func hashFileState(filePath string, info os.FileInfo) (FileState, error) {
	hash := sha256.New()
	kind := "file"
	if info.Mode()&os.ModeSymlink != 0 {
		kind = "symlink"
		target, err := os.Readlink(filePath)
		if err != nil {
			return FileState{}, err
		}
		_, _ = io.WriteString(hash, target)
	} else if info.Mode().IsRegular() {
		file, err := os.Open(filePath)
		if err != nil {
			return FileState{}, err
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return FileState{}, copyErr
		}
		if closeErr != nil {
			return FileState{}, closeErr
		}
	} else {
		kind = "special"
		_, _ = io.WriteString(hash, info.Mode().String())
	}
	return FileState{
		Mode: uint32(info.Mode()), Size: info.Size(), Digest: hex.EncodeToString(hash.Sum(nil)), Kind: kind,
	}, nil
}

func compareFiles(before, after map[string]FileState) []string {
	changedSet := make(map[string]bool)
	for name, state := range before {
		if current, ok := after[name]; !ok || current != state {
			changedSet[name] = true
		}
	}
	for name, state := range after {
		if previous, ok := before[name]; !ok || previous != state {
			changedSet[name] = true
		}
	}
	changed := make([]string, 0, len(changedSet))
	for name := range changedSet {
		changed = append(changed, name)
	}
	sort.Strings(changed)
	return changed
}

func normalizeWorkspacePolicy(policy WorkspacePolicy) WorkspacePolicy {
	if len(policy.IgnoredPaths) == 0 {
		policy.IgnoredPaths = []string{".git", ".git/**", ".tools", ".tools/**", ".cache", ".cache/**", "bin", "bin/**", "data", "data/**"}
	}
	if len(policy.SensitivePaths) == 0 {
		policy.SensitivePaths = []string{".api_key", ".env", ".env.*", "*.pem", "*.key", "id_rsa", "id_ed25519", "**/id_rsa", "**/id_ed25519"}
	}
	if policy.MaxFiles <= 0 {
		policy.MaxFiles = defaultMaxFiles
	}
	if policy.MaxFileSize <= 0 {
		policy.MaxFileSize = defaultMaxFileSize
	}
	if policy.MaxTotalSize <= 0 {
		policy.MaxTotalSize = defaultMaxTotalSize
	}
	return policy
}

func matchesAny(name string, patterns []string) bool {
	name = filepath.ToSlash(name)
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(pattern)
		if matched, err := path.Match(pattern, name); err == nil && matched {
			return true
		}
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			if name == prefix || strings.HasPrefix(name, prefix+"/") {
				return true
			}
		}
	}
	return false
}
