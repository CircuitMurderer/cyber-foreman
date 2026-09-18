package verification

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	defaultCommandTimeout = 10 * time.Minute
	defaultOutputLimit    = 64 * 1024
)

type Command struct {
	Argv    []string      `json:"argv"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

type CommandResult struct {
	Argv      []string      `json:"argv"`
	ExitCode  int           `json:"exit_code"`
	Duration  time.Duration `json:"duration"`
	TimedOut  bool          `json:"timed_out"`
	Output    string        `json:"output,omitempty"`
	Truncated bool          `json:"truncated,omitempty"`
	Error     string        `json:"error,omitempty"`
}

type TestResult struct {
	Passed   bool            `json:"passed"`
	Commands []CommandResult `json:"commands"`
}

type CommandVerifier struct {
	OutputLimit     int
	Env             []string
	EnvKeys         []string
	SensitiveValues []string
}

func (v CommandVerifier) Run(ctx context.Context, cwd string, commands []Command) TestResult {
	result := TestResult{Passed: true, Commands: make([]CommandResult, 0, len(commands))}
	for _, command := range commands {
		commandResult := v.runOne(ctx, cwd, command)
		result.Commands = append(result.Commands, commandResult)
		if commandResult.ExitCode != 0 || commandResult.TimedOut || commandResult.Error != "" {
			result.Passed = false
			break
		}
	}
	return result
}

func (v CommandVerifier) runOne(parent context.Context, cwd string, command Command) CommandResult {
	result := CommandResult{Argv: append([]string(nil), command.Argv...), ExitCode: -1}
	if len(command.Argv) == 0 {
		result.Error = "verification command is empty"
		return result
	}
	timeout := command.Timeout
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command.Argv[0], command.Argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = append(v.allowedEnvironment(), v.Env...)
	limit := v.OutputLimit
	if limit <= 0 {
		limit = defaultOutputLimit
	}
	output := newTailBuffer(limit)
	cmd.Stdout = output
	cmd.Stderr = output
	started := time.Now()
	err := cmd.Run()
	result.Duration = time.Since(started)
	result.Output = redact(output.String(), v.Env, v.SensitiveValues)
	result.Truncated = output.Truncated()
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		result.Error = "verification command timed out"
		return result
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func (v CommandVerifier) allowedEnvironment() []string {
	keys := v.EnvKeys
	if len(keys) == 0 {
		keys = []string{"PATH", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "SYSTEMROOT"}
	}
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			environment = append(environment, key+"="+value)
		}
	}
	return environment
}

type tailBuffer struct {
	mu        sync.Mutex
	limit     int
	data      []byte
	truncated bool
}

func newTailBuffer(limit int) *tailBuffer { return &tailBuffer{limit: limit} }

func (b *tailBuffer) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	originalLength := len(payload)
	if originalLength >= b.limit {
		b.data = append(b.data[:0], payload[originalLength-b.limit:]...)
		b.truncated = true
		return originalLength, nil
	}
	if len(b.data)+originalLength > b.limit {
		overflow := len(b.data) + originalLength - b.limit
		b.data = append(b.data[:0], b.data[overflow:]...)
		b.truncated = true
	}
	b.data = append(b.data, payload...)
	return originalLength, nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(bytes.Clone(b.data)))
}

func (b *tailBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}
