package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cyber-foreman/internal/agent/acpagent"
	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/event"
	"cyber-foreman/internal/supervisor"
	"cyber-foreman/internal/verification"
)

func TestACPWireSupervisionRecoversCancelsRepairsAndVerifies(t *testing.T) {
	workspace := t.TempDir()
	adapter, err := acpagent.NewAdapter(acpagent.Config{
		Profile: acpagent.Profile{
			Name: "wire-acp", Command: os.Args[0],
			Args: []string{"-test.run=^TestSupervisedACPWireHelper$", "--"},
		},
		Env:              []string{"CYBER_FOREMAN_SUPERVISED_ACP_HELPER=1"},
		HandshakeTimeout: 2 * time.Second,
		GracePeriod:      100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := event.NewBus()
	events := bus.Subscribe(ctx, 512)
	service := NewService(ctx, adapter, bus)
	policy := supervisor.DefaultPolicy()
	policy.IdleTimeout = 30 * time.Millisecond
	policy.HardTimeout = 10 * time.Second
	policy.MaxNudges = 1
	policy.MaxRetries = 1
	policy.MaxTestRepairs = 1
	task, err := service.StartTask(StartTaskRequest{
		Prompt: "build the wire-level feature",
		CWD:    workspace, Supervision: &policy,
		Verification: VerificationRequest{Commands: []verification.Command{{
			Argv:    []string{"/bin/sh", "-c", `grep -qx FIXED result.txt || { echo "expected result.txt to contain FIXED"; exit 1; }`},
			Timeout: 2 * time.Second,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForTerminalTask(t, service, task.ID)
	if final.Status != domain.TaskCompleted {
		t.Fatalf("status = %q, error = %q", final.Status, final.Error)
	}
	result, err := os.ReadFile(filepath.Join(workspace, "result.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(result)) != "FIXED" {
		t.Fatalf("result.txt = %q", result)
	}

	logData, err := os.ReadFile(filepath.Join(workspace, ".wire-acp.log"))
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	for _, expected := range []string{
		"session=1 cancel", "session=1 prompt=2", "session=2 prompt=1", "session=2 prompt=2",
		"build the wire-level feature", "没有可观察进展", "expected result.txt to contain FIXED",
	} {
		if !strings.Contains(logText, expected) {
			t.Fatalf("wire log lacks %q:\n%s", expected, logText)
		}
	}
	if strings.Contains(logText, "partial-wire-output") {
		t.Fatalf("untrusted Agent output leaked into replay prompt:\n%s", logText)
	}

	actions := map[supervisor.Action]int{}
	verificationRuns := 0
	passedVerification := false
	for {
		select {
		case evt := <-events:
			if evt.TaskID != task.ID {
				continue
			}
			if evt.Type == domain.EventSupervisorDecision {
				if decision, ok := evt.Data.(supervisor.Decision); ok {
					actions[decision.Action]++
				}
			}
			if evt.Type == domain.EventVerificationFinish {
				verificationRuns++
				if data, ok := evt.Data.(map[string]any); ok {
					passed, _ := data["passed"].(bool)
					passedVerification = passedVerification || passed
				}
			}
		default:
			if actions[supervisor.ActionNudge] != 1 || actions[supervisor.ActionRetrySession] != 1 ||
				actions[supervisor.ActionRepair] != 1 {
				t.Fatalf("supervisor actions = %#v", actions)
			}
			if verificationRuns != 2 || !passedVerification {
				t.Fatalf("verification runs=%d passed=%v", verificationRuns, passedVerification)
			}
			return
		}
	}
}

// TestSupervisedACPWireHelper is executed in a child process by the test
// above. It speaks ACP over real stdin/stdout JSONL, deliberately disconnects
// its first process, and uses files in the temporary workspace to coordinate
// the replacement process.
func TestSupervisedACPWireHelper(t *testing.T) {
	if os.Getenv("CYBER_FOREMAN_SUPERVISED_ACP_HELPER") != "1" {
		return
	}
	if err := runSupervisedACPWireHelper(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(90)
	}
	os.Exit(0)
}

type supervisedACPMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func runSupervisedACPWireHelper() error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	encoder := json.NewEncoder(os.Stdout)
	attempt := 0
	promptIndex := 0
	sessionID := ""
	var pendingPromptID json.RawMessage
	for scanner.Scan() {
		var message supervisedACPMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return fmt.Errorf("decode ACP request: %w", err)
		}
		switch message.Method {
		case "initialize":
			writeSupervisedACPResponse(encoder, message.ID, map[string]any{
				"protocolVersion":   1,
				"agentCapabilities": map[string]any{},
				"agentInfo":         map[string]string{"name": "supervised-wire-agent", "version": "test"},
				"authMethods":       []any{},
			})
		case "session/new":
			var err error
			attempt, err = incrementWireAttempt()
			if err != nil {
				return err
			}
			sessionID = fmt.Sprintf("wire-session-%d", attempt)
			writeSupervisedACPResponse(encoder, message.ID, map[string]any{
				"sessionId": sessionID, "configOptions": []any{},
			})
		case "session/prompt":
			promptIndex++
			text, err := supervisedACPPromptText(message.Params)
			if err != nil {
				return err
			}
			if err := appendWireLog(fmt.Sprintf("session=%d prompt=%d %q\n", attempt, promptIndex, text)); err != nil {
				return err
			}
			if attempt == 1 && promptIndex == 1 {
				if err := writeSupervisedACPUpdate(encoder, sessionID, "partial-wire-output"); err != nil {
					return err
				}
				pendingPromptID = append(json.RawMessage(nil), message.ID...)
				continue
			}
			if attempt == 1 {
				// Drop the JSON-RPC response and transport during the follow-up.
				os.Exit(42)
			}
			if promptIndex == 1 {
				if err := os.WriteFile("result.txt", []byte("WRONG\n"), 0o600); err != nil {
					return err
				}
				if err := writeSupervisedACPUpdate(encoder, sessionID, "replacement session wrote initial result"); err != nil {
					return err
				}
			} else {
				if err := os.WriteFile("result.txt", []byte("FIXED\n"), 0o600); err != nil {
					return err
				}
				if err := writeSupervisedACPUpdate(encoder, sessionID, "repair applied"); err != nil {
					return err
				}
			}
			writeSupervisedACPResponse(encoder, message.ID, map[string]string{"stopReason": "end_turn"})
		case "session/cancel":
			if err := appendWireLog(fmt.Sprintf("session=%d cancel\n", attempt)); err != nil {
				return err
			}
			if len(pendingPromptID) > 0 {
				writeSupervisedACPResponse(encoder, pendingPromptID, map[string]string{"stopReason": "cancelled"})
				pendingPromptID = nil
			}
		default:
			if len(message.ID) > 0 {
				writeSupervisedACPResponse(encoder, message.ID, map[string]any{})
			}
		}
	}
	if err := scanner.Err(); err != nil && !strings.Contains(err.Error(), "file already closed") {
		return err
	}
	return nil
}

func supervisedACPPromptText(params json.RawMessage) (string, error) {
	var request struct {
		Prompt []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return "", fmt.Errorf("decode session/prompt: %w", err)
	}
	parts := make([]string, 0, len(request.Prompt))
	for _, item := range request.Prompt {
		if item.Type == "text" {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

func writeSupervisedACPResponse(encoder *json.Encoder, id json.RawMessage, result any) {
	_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}

func writeSupervisedACPUpdate(encoder *json.Encoder, sessionID, text string) error {
	return encoder.Encode(map[string]any{
		"jsonrpc": "2.0", "method": "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]string{"type": "text", "text": text},
			},
		},
	})
}

func incrementWireAttempt() (int, error) {
	const path = ".wire-acp-attempt"
	attempt := 0
	if data, err := os.ReadFile(path); err == nil {
		attempt, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	} else if !os.IsNotExist(err) {
		return 0, err
	}
	attempt++
	return attempt, os.WriteFile(path, []byte(strconv.Itoa(attempt)), 0o600)
}

func appendWireLog(value string) error {
	file, err := os.OpenFile(".wire-acp.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(value); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
