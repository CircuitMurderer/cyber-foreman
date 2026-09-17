package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cyber-foreman/internal/agent"
	"cyber-foreman/internal/domain"
)

func TestAdapterLifecycle(t *testing.T) {
	adapter, err := NewAdapter(Config{
		Binary:           os.Args[0],
		Args:             []string{"-test.run=TestOpenCodeHelperProcess", "--"},
		Env:              []string{"CYBER_FOREMAN_OPENCODE_HELPER=1"},
		HandshakeTimeout: 3 * time.Second,
		GracePeriod:      time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := adapter.Start(ctx, agent.StartRequest{TaskID: "task-test", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	events, err := adapter.Events(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}

	result, err := adapter.Prompt(ctx, session.ID, agent.PromptRequest{Text: "say hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
	if err := adapter.SetConfigOption(ctx, session.ID, agent.ConfigOption{ID: "model", Value: "google/gemini-test"}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Cancel(ctx, session.ID); err != nil {
		t.Fatal(err)
	}

	wantedUpdates := map[string]bool{"agent_message_chunk": false, "cancel_seen": false}
	foundPermission := false
	deadline := time.After(3 * time.Second)
	for !wantedUpdates["agent_message_chunk"] || !wantedUpdates["cancel_seen"] || !foundPermission {
		select {
		case event := <-events:
			if event.Type == domain.EventAgentPermission {
				foundPermission = true
				continue
			}
			if event.Type != domain.EventAgentSessionUpdate {
				continue
			}
			data, ok := event.Data.(domain.AgentSessionUpdateData)
			if !ok {
				t.Fatalf("unexpected update data %T", event.Data)
			}
			var update struct {
				Kind string `json:"sessionUpdate"`
			}
			if err := json.Unmarshal(data.Update, &update); err != nil {
				t.Fatal(err)
			}
			if _, ok := wantedUpdates[update.Kind]; ok {
				wantedUpdates[update.Kind] = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for updates: %#v", wantedUpdates)
		}
	}
	if err := adapter.Stop(ctx, session.ID); err != nil {
		t.Fatal(err)
	}

	foundDisconnect := false
	for event := range events {
		if event.Type == domain.EventAgentDisconnected {
			foundDisconnect = true
		}
	}
	if !foundDisconnect {
		t.Fatal("missing agent.disconnected event")
	}
}

func TestOpenCodeHelperProcess(t *testing.T) {
	if os.Getenv("CYBER_FOREMAN_OPENCODE_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	step := 0
	for scanner.Scan() {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		switch message.Method {
		case "initialize":
			if step != 0 {
				os.Exit(3)
			}
			step++
			writeHelperResponse(encoder, message.ID, map[string]any{
				"protocolVersion":   1,
				"agentCapabilities": map[string]any{},
				"agentInfo":         map[string]string{"name": "fake-opencode", "version": "test"},
			})
		case "session/new":
			if step != 1 {
				os.Exit(4)
			}
			step++
			writeHelperResponse(encoder, message.ID, map[string]any{
				"sessionId": "session-test", "configOptions": []any{},
			})
		case "session/prompt":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0", "id": 700, "method": "session/request_permission",
				"params": map[string]any{
					"sessionId": "session-test",
					"toolCall":  map[string]any{"toolCallId": "tool-test", "status": "pending"},
					"options": []map[string]string{
						{"optionId": "allow", "name": "Allow once", "kind": "allow_once"},
						{"optionId": "reject", "name": "Reject once", "kind": "reject_once"},
					},
				},
			})
			if !scanner.Scan() {
				os.Exit(6)
			}
			var permissionResponse struct {
				Result struct {
					Outcome struct {
						Outcome  string `json:"outcome"`
						OptionID string `json:"optionId"`
					} `json:"outcome"`
				} `json:"result"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &permissionResponse); err != nil || permissionResponse.Result.Outcome.OptionID != "reject" {
				os.Exit(7)
			}
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{
					"sessionId": "session-test",
					"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "hello"}},
				},
			})
			writeHelperResponse(encoder, message.ID, map[string]string{"stopReason": "end_turn"})
		case "session/set_config_option":
			writeHelperResponse(encoder, message.ID, map[string]any{"configOptions": []any{}})
		case "session/cancel":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{
					"sessionId": "session-test",
					"update":    map[string]string{"sessionUpdate": "cancel_seen"},
				},
			})
		default:
			if len(message.ID) > 0 {
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0", "id": json.RawMessage(message.ID),
					"error": map[string]any{"code": -32601, "message": "unknown method " + message.Method},
				})
			}
		}
	}
	if err := scanner.Err(); err != nil && !strings.Contains(err.Error(), "file already closed") {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(5)
	}
	os.Exit(0)
}

func TestRealOpenCodeHandshake(t *testing.T) {
	binary := os.Getenv("OPENCODE_BIN")
	if binary == "" {
		t.Skip("set OPENCODE_BIN to run the real OpenCode handshake test")
	}
	adapter, err := NewAdapter(Config{Binary: binary, HandshakeTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	session, err := adapter.Start(ctx, agent.StartRequest{TaskID: "real-handshake", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if session.ID == "" {
		t.Fatal("empty session id")
	}
	if err := adapter.Stop(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
}

func writeHelperResponse(encoder *json.Encoder, id json.RawMessage, result any) {
	_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}
