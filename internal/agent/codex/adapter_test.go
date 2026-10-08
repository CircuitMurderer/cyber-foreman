package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cyber-foreman/internal/acp"
	"cyber-foreman/internal/agent"
	"cyber-foreman/internal/domain"
)

func TestAdapterPromptContinueAndCancel(t *testing.T) {
	adapter, err := NewAdapter(Config{
		Binary: os.Args[0],
		Args:   []string{"-test.run=TestCodexHelperProcess", "--", "app-server"},
		Env:    []string{"GO_WANT_CODEX_HELPER=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := adapter.Start(ctx, agent.StartRequest{TaskID: "task-codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	events, err := adapter.Events(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.SetConfigOption(ctx, session.ID, agent.ConfigOption{ID: "model", Value: "test-model"}); err != nil {
		t.Fatal(err)
	}
	first, err := adapter.Prompt(ctx, session.ID, agent.PromptRequest{Text: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if first.StopReason != "end_turn" {
		t.Fatalf("first stop reason = %q", first.StopReason)
	}
	second, err := adapter.Prompt(ctx, session.ID, agent.PromptRequest{Text: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if second.StopReason != "end_turn" {
		t.Fatalf("second stop reason = %q", second.StopReason)
	}

	promptDone := make(chan struct {
		result agent.PromptResult
		err    error
	}, 1)
	go func() {
		result, promptErr := adapter.Prompt(ctx, session.ID, agent.PromptRequest{Text: "wait"})
		promptDone <- struct {
			result agent.PromptResult
			err    error
		}{result: result, err: promptErr}
	}()
	waitForText(t, ctx, events, "waiting")
	if err := adapter.Cancel(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-promptDone:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if outcome.result.StopReason != "cancelled" {
			t.Fatalf("cancel stop reason = %q", outcome.result.StopReason)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for interrupted turn")
	}
	if err := adapter.Stop(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPermissionRequestsWaitForStructuredDecision(t *testing.T) {
	s := &session{
		taskID: "task", id: "thread", events: make(chan domain.Event, 1), done: make(chan struct{}),
		permissions: make(map[string]*codexPermission),
	}
	adapter := &Adapter{sessions: map[string]*session{"thread": s}}
	type outcome struct {
		result any
		err    *acp.RPCError
	}
	done := make(chan outcome, 1)
	go func() {
		result, rpcErr := s.handleRequest(context.Background(), "item/commandExecution/requestApproval", json.RawMessage(`{}`))
		done <- outcome{result: result, err: rpcErr}
	}()
	event := <-s.events
	permission, ok := event.Data.(domain.AgentPermissionData)
	if event.Type != domain.EventAgentPermission || !ok || permission.RequestID == "" || len(permission.Options) != 3 {
		t.Fatalf("permission event=%#v", event)
	}
	if err := adapter.ResolvePermission(context.Background(), "thread", permission.RequestID, "accept"); err != nil {
		t.Fatal(err)
	}
	resolved := <-done
	if resolved.err != nil {
		t.Fatal(resolved.err)
	}
	decision, ok := resolved.result.(map[string]string)
	if !ok || decision["decision"] != "accept" {
		t.Fatalf("decision = %#v", resolved.result)
	}
}

func TestProviderKeyIsRemovedFromCodexChildEnvironment(t *testing.T) {
	const keyName = "FOREMAN_TEST_CODEX_KEY"
	t.Setenv(keyName, "parent-secret")
	adapter, err := NewAdapter(Config{
		Binary: os.Args[0],
		Env:    []string{keyName + "=config-secret", "KEEP_CONFIG=yes"},
		Provider: &ProviderConfig{
			BaseURL: "http://127.0.0.1:9000", APIKeyEnv: keyName,
			DefaultModel: "test-model", Protocol: ProtocolResponses,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	environment := adapter.childEnv([]string{keyName + "=request-secret", "KEEP_REQUEST=yes"})
	for _, entry := range environment {
		if strings.HasPrefix(entry, keyName+"=") {
			t.Fatalf("provider key leaked to child environment: %q", entry)
		}
	}
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "KEEP_CONFIG=yes") || !strings.Contains(joined, "KEEP_REQUEST=yes") {
		t.Fatalf("unrelated environment was removed: %s", joined)
	}
}

func TestProviderArgsDisableExternalControlPlaneTraffic(t *testing.T) {
	arguments := strings.Join(providerArgs("http://127.0.0.1:1234/v1"), "\n")
	for _, expected := range []string{
		`model_provider="cyber_foreman"`,
		`model_providers.cyber_foreman.base_url="http://127.0.0.1:1234/v1"`,
		"analytics.enabled=false",
		"features.remote_plugin=false",
		"features.plugins=false",
	} {
		if !strings.Contains(arguments, expected) {
			t.Fatalf("provider arguments missing %q: %s", expected, arguments)
		}
	}
}

func waitForText(t *testing.T, ctx context.Context, events <-chan domain.Event, want string) {
	t.Helper()
	for {
		select {
		case event := <-events:
			if event.Type != domain.EventAgentSessionUpdate {
				continue
			}
			data, ok := event.Data.(domain.AgentSessionUpdateData)
			if ok && strings.Contains(string(data.Update), want) {
				return
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

func TestCodexHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CODEX_HELPER") != "1" {
		return
	}
	args := os.Args
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(args) || args[separator+1] != "app-server" {
		fmt.Fprintln(os.Stderr, "expected app-server argument")
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	turnNumber := 0
	activeTurn := ""
	for scanner.Scan() {
		var message struct {
			JSONRPC json.RawMessage `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			os.Exit(3)
		}
		if len(message.JSONRPC) != 0 {
			os.Exit(4)
		}
		switch message.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"id": message.ID, "result": map[string]string{"codexHome": "/tmp/codex", "platformFamily": "unix", "platformOs": "test", "userAgent": "codex-test"}})
		case "initialized":
		case "thread/start":
			var params struct {
				ApprovalPolicy    string `json:"approvalPolicy"`
				ApprovalsReviewer string `json:"approvalsReviewer"`
			}
			if json.Unmarshal(message.Params, &params) != nil || params.ApprovalPolicy != "on-request" || params.ApprovalsReviewer != "user" {
				os.Exit(12)
			}
			_ = encoder.Encode(map[string]any{"id": message.ID, "result": map[string]any{"thread": map[string]string{"id": "thread-1"}, "model": "test-model", "modelProvider": "test", "cwd": "/tmp", "approvalPolicy": "on-request", "sandbox": map[string]any{}, "approvalsReviewer": "user"}})
		case "turn/start":
			turnNumber++
			activeTurn = fmt.Sprintf("turn-%d", turnNumber)
			var params struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			_ = json.Unmarshal(message.Params, &params)
			_ = encoder.Encode(map[string]any{"id": message.ID, "result": map[string]any{"turn": map[string]any{"id": activeTurn, "status": "inProgress", "items": []any{}, "error": nil}}})
			text := fmt.Sprintf("hello-%d", turnNumber)
			if len(params.Input) > 0 && params.Input[0].Text == "wait" {
				text = "waiting"
			}
			_ = encoder.Encode(map[string]any{"method": "item/agentMessage/delta", "params": map[string]string{"threadId": "thread-1", "turnId": activeTurn, "itemId": "message-1", "delta": text}})
			_ = encoder.Encode(map[string]any{"method": "item/started", "params": map[string]any{"threadId": "thread-1", "turnId": activeTurn, "item": map[string]string{"id": "tool-1", "type": "commandExecution"}}})
			_ = encoder.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-1", "turnId": activeTurn, "item": map[string]string{"id": "tool-1", "type": "commandExecution"}}})
			if text != "waiting" {
				_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": activeTurn, "status": "completed", "items": []any{}, "error": nil}}})
				activeTurn = ""
			}
		case "turn/interrupt":
			turnID := activeTurn
			_ = encoder.Encode(map[string]any{"id": message.ID, "result": map[string]any{}})
			_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": turnID, "status": "interrupted", "items": []any{}, "error": nil}}})
			activeTurn = ""
		default:
			if len(message.ID) > 0 {
				_ = encoder.Encode(map[string]any{"id": message.ID, "error": &acp.RPCError{Code: -32601, Message: "unsupported"}})
			}
		}
	}
	os.Exit(0)
}
