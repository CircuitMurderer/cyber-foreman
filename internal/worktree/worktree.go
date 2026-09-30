package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	ErrNotRepository = errors.New("workspace is not inside a Git repository")
	ErrDirtySource   = errors.New("source workspace has uncommitted changes")
)

const maxPatchBytes = 1024 * 1024
const maxUntrackedFileBytes = 128 * 1024

type Prepared struct {
	SourceCWD    string
	SourceRoot   string
	Root         string
	CWD          string
	BaseRevision string
}

type FileChange struct {
	Status   string `json:"status"`
	Path     string `json:"path"`
	Redacted bool   `json:"redacted,omitempty"`
	original string
}

type Diff struct {
	BaseRevision string       `json:"base_revision"`
	Files        []FileChange `json:"files"`
	Patch        string       `json:"patch"`
	Truncated    bool         `json:"truncated"`
}

func Prepare(ctx context.Context, sourceCWD, taskID string) (Prepared, error) {
	if strings.TrimSpace(sourceCWD) == "" {
		return Prepared{}, errors.New("source workspace is required")
	}
	if taskID == "" || strings.ContainsAny(taskID, "/\\\x00") {
		return Prepared{}, errors.New("invalid task id for worktree")
	}
	sourceCWD, err := filepath.Abs(sourceCWD)
	if err != nil {
		return Prepared{}, fmt.Errorf("resolve source workspace: %w", err)
	}
	if sourceCWD, err = filepath.EvalSymlinks(sourceCWD); err != nil {
		return Prepared{}, fmt.Errorf("resolve source workspace links: %w", err)
	}
	root, err := gitText(ctx, sourceCWD, "rev-parse", "--show-toplevel")
	if err != nil {
		return Prepared{}, fmt.Errorf("%w: %v", ErrNotRepository, err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return Prepared{}, fmt.Errorf("resolve repository root: %w", err)
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return Prepared{}, fmt.Errorf("resolve repository root links: %w", err)
	}
	relativeCWD, err := filepath.Rel(root, sourceCWD)
	if err != nil || relativeCWD == ".." || strings.HasPrefix(relativeCWD, ".."+string(filepath.Separator)) {
		return Prepared{}, errors.New("workspace is outside its Git root")
	}
	status, err := gitText(ctx, root, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return Prepared{}, fmt.Errorf("inspect source workspace: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return Prepared{}, ErrDirtySource
	}
	base, err := gitText(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return Prepared{}, fmt.Errorf("resolve worktree base: %w", err)
	}
	commonDir, err := gitText(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return Prepared{}, fmt.Errorf("resolve Git common directory: %w", err)
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}
	commonDir, err = filepath.Abs(commonDir)
	if err != nil {
		return Prepared{}, fmt.Errorf("resolve Git common directory: %w", err)
	}
	worktreeRoot := filepath.Join(commonDir, "foreman-worktrees", taskID)
	if _, err := os.Stat(worktreeRoot); err == nil {
		return Prepared{}, fmt.Errorf("worktree path already exists: %s", worktreeRoot)
	} else if !os.IsNotExist(err) {
		return Prepared{}, err
	}
	if err := os.MkdirAll(filepath.Dir(worktreeRoot), 0o700); err != nil {
		return Prepared{}, fmt.Errorf("create worktree parent: %w", err)
	}
	if _, err := gitText(ctx, root, "worktree", "add", "--detach", worktreeRoot, base); err != nil {
		return Prepared{}, fmt.Errorf("create isolated worktree: %w", err)
	}
	worktreeCWD := filepath.Join(worktreeRoot, relativeCWD)
	info, err := os.Stat(worktreeCWD)
	if err != nil || !info.IsDir() {
		return Prepared{}, fmt.Errorf("worktree task directory is unavailable: %s", worktreeCWD)
	}
	return Prepared{
		SourceCWD: sourceCWD, SourceRoot: root, Root: worktreeRoot,
		CWD: worktreeCWD, BaseRevision: strings.TrimSpace(base),
	}, nil
}

func ReadDiff(ctx context.Context, root, baseRevision string) (Diff, error) {
	if strings.TrimSpace(root) == "" {
		return Diff{}, errors.New("worktree root is required")
	}
	if baseRevision == "" {
		var err error
		baseRevision, err = gitText(ctx, root, "rev-parse", "HEAD")
		if err != nil {
			return Diff{}, fmt.Errorf("resolve diff base: %w", err)
		}
	}
	statusRaw, err := gitBytes(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return Diff{}, fmt.Errorf("inspect worktree changes: %w", err)
	}
	files := parseStatus(statusRaw)
	for index := range files {
		files[index].Redacted = sensitivePath(files[index].Path) || sensitivePath(files[index].original)
	}
	redactSensitiveMoves(ctx, root, baseRevision, files)
	safeTracked := make([]string, 0, len(files))
	untracked := make([]string, 0, len(files))
	for index := range files {
		if files[index].Redacted {
			continue
		}
		if files[index].Status == "??" {
			untracked = append(untracked, files[index].Path)
		} else {
			safeTracked = append(safeTracked, files[index].Path)
		}
	}
	var patch cappedBuffer
	patch.max = maxPatchBytes
	if len(safeTracked) > 0 {
		args := []string{"diff", "--no-ext-diff", "--no-color", "--find-renames", baseRevision, "--"}
		args = append(args, safeTracked...)
		tracked, err := gitBytes(ctx, root, args...)
		if err != nil {
			return Diff{}, fmt.Errorf("read tracked diff: %w", err)
		}
		_, _ = patch.Write(tracked)
	}
	for _, path := range untracked {
		appendUntrackedPatch(&patch, root, path)
	}
	for _, file := range files {
		if !file.Redacted {
			continue
		}
		_, _ = fmt.Fprintf(&patch, "diff --git a/%s b/%s\n# Cyber Foreman redacted sensitive file content\n", file.Path, file.Path)
	}
	return Diff{
		BaseRevision: strings.TrimSpace(baseRevision), Files: files,
		Patch: patch.String(), Truncated: patch.truncated,
	}, nil
}

func redactSensitiveMoves(ctx context.Context, root, baseRevision string, files []FileChange) {
	sensitiveBlobs := make([][]byte, 0)
	for _, file := range files {
		if !file.Redacted || !strings.Contains(file.Status, "D") {
			continue
		}
		content, err := gitBytes(ctx, root, "cat-file", "blob", baseRevision+":"+file.Path)
		if err == nil && len(content) <= maxUntrackedFileBytes {
			sensitiveBlobs = append(sensitiveBlobs, content)
		}
	}
	if len(sensitiveBlobs) == 0 {
		return
	}
	for index := range files {
		file := &files[index]
		if file.Status != "??" || file.Redacted {
			continue
		}
		absolute := filepath.Join(root, filepath.FromSlash(file.Path))
		info, err := os.Lstat(absolute)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxUntrackedFileBytes {
			continue
		}
		content, err := os.ReadFile(absolute)
		if err != nil {
			continue
		}
		for _, sensitive := range sensitiveBlobs {
			if bytes.Equal(content, sensitive) {
				file.Redacted = true
				break
			}
		}
	}
}

func parseStatus(value []byte) []FileChange {
	parts := bytes.Split(value, []byte{0})
	files := make([]FileChange, 0, len(parts))
	for index := 0; index < len(parts); index++ {
		part := parts[index]
		if len(part) < 4 || part[2] != ' ' {
			continue
		}
		status := string(part[:2])
		path := filepath.ToSlash(string(part[3:]))
		original := ""
		if (part[0] == 'R' || part[0] == 'C' || part[1] == 'R' || part[1] == 'C') && index+1 < len(parts) {
			index++
			original = filepath.ToSlash(string(parts[index])) // Porcelain -z includes the original path second.
		}
		files = append(files, FileChange{Status: status, Path: path, original: original})
	}
	return files
}

func appendUntrackedPatch(patch *cappedBuffer, root, path string) {
	absolute := filepath.Join(root, filepath.FromSlash(path))
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	if info.Size() > maxUntrackedFileBytes {
		_, _ = fmt.Fprintf(patch, "diff --git a/%s b/%s\nnew file mode %o\n# Untracked file is too large to display\n", path, path, info.Mode().Perm())
		return
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		return
	}
	if bytes.IndexByte(content, 0) >= 0 {
		_, _ = fmt.Fprintf(patch, "diff --git a/%s b/%s\nnew file mode %o\nBinary file /dev/null and b/%s differ\n", path, path, info.Mode().Perm(), path)
		return
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	_, _ = fmt.Fprintf(patch, "diff --git a/%s b/%s\nnew file mode %o\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", path, path, info.Mode().Perm(), path, len(lines))
	for _, line := range lines {
		_, _ = patch.WriteString("+" + line + "\n")
	}
}

func sensitivePath(path string) bool {
	normalized := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(normalized)
	if base == ".api_key" || base == ".env" || strings.HasPrefix(base, ".env.") ||
		base == "id_rsa" || base == "id_ed25519" || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") {
		return true
	}
	return strings.Contains(normalized, "/.ssh/")
}

func gitText(ctx context.Context, dir string, args ...string) (string, error) {
	value, err := gitBytes(ctx, dir, args...)
	return strings.TrimSpace(string(value)), err
}

func gitBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout cappedBuffer
	stdout.max = maxPatchBytes + 1
	var stderr cappedBuffer
	stderr.max = 32 * 1024
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, errors.New(message)
	}
	return stdout.Bytes(), nil
}

type cappedBuffer struct {
	buffer    bytes.Buffer
	max       int
	truncated bool
}

func (b *cappedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.max - b.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
			b.truncated = true
		}
		_, _ = b.buffer.Write(value)
	} else if original > 0 {
		b.truncated = true
	}
	return original, nil
}

func (b *cappedBuffer) WriteString(value string) (int, error) { return b.Write([]byte(value)) }
func (b *cappedBuffer) String() string                        { return b.buffer.String() }
func (b *cappedBuffer) Bytes() []byte                         { return append([]byte(nil), b.buffer.Bytes()...) }
