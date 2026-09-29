package acpagent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyber-foreman/internal/agent"
)

func TestResolvesCommandFromPATHAndReportsMissingCommand(t *testing.T) {
	t.Setenv("PATH", filepath.Dir(os.Args[0]))
	adapter, err := NewAdapter(Config{Profile: Profile{Name: "path-agent", Command: filepath.Base(os.Args[0]), Args: []string{"unused"}}})
	if err != nil {
		t.Fatal(err)
	}
	if status := adapter.Status(); !status.Installed || !status.Healthy || !filepath.IsAbs(status.Command) {
		t.Fatalf("unexpected PATH status: %#v", status)
	}

	missing, err := NewAdapter(Config{Profile: Profile{Name: "missing", Command: "definitely-not-a-real-agent-command"}})
	if err != nil {
		t.Fatal(err)
	}
	if status := missing.Status(); status.Installed || status.Healthy || status.Error == "" {
		t.Fatalf("unexpected missing status: %#v", status)
	}
}

func TestProbeAndAuthenticatedLifecycle(t *testing.T) {
	profile := Profile{
		Name: "grok", Command: os.Args[0],
		Args:        []string{"-test.run=TestACPAgentHelperProcess", "--", "--no-auto-update", "agent", "stdio"},
		AuthMethods: []string{"xai.api_key", "cached_token"},
	}
	adapter, err := NewAdapter(Config{
		Profile: profile, Env: []string{"CYBER_FOREMAN_ACP_HELPER=1"},
		HandshakeTimeout: 3 * time.Second, GracePeriod: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	probeCtx, probeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	status := adapter.Probe(probeCtx)
	probeCancel()
	if !status.Installed || !status.Healthy || status.ProtocolVersion != 1 {
		t.Fatalf("unexpected probe status: %#v", status)
	}
	if status.AgentInfo == nil || status.AgentInfo.Name != "fake-grok" {
		t.Fatalf("unexpected agent info: %#v", status.AgentInfo)
	}
	if !adapter.Capabilities().ResumeSession {
		t.Fatal("loadSession capability was not negotiated")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := adapter.Start(ctx, agent.StartRequest{TaskID: "task-grok", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != "grok-session" {
		t.Fatalf("session id = %q", session.ID)
	}
	if err := adapter.Stop(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
}

func TestOptionalAuthenticationFallsBackToProviderEnvironment(t *testing.T) {
	profile := Profile{
		Name: "grok-byok", Command: os.Args[0],
		Args:         []string{"-test.run=TestACPAgentHelperProcess", "--", "--no-auto-update", "agent", "stdio"},
		AuthMethods:  []string{"xai.api_key", "cached_token"},
		AuthOptional: true,
	}
	adapter, err := NewAdapter(Config{
		Profile:          profile,
		Env:              []string{"CYBER_FOREMAN_ACP_HELPER=1", "CYBER_FOREMAN_ACP_HELPER_AUTH_OPTIONAL=1"},
		HandshakeTimeout: 3 * time.Second, GracePeriod: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := adapter.Start(ctx, agent.StartRequest{TaskID: "task-grok-byok", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != "grok-session" {
		t.Fatalf("session id = %q", session.ID)
	}
	if status := adapter.Status(); !status.Healthy || !strings.Contains(status.Error, "authentication skipped") {
		t.Fatalf("unexpected optional authentication status: %#v", status)
	}
	if err := adapter.Stop(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
}

func TestACPAgentHelperProcess(t *testing.T) {
	if os.Getenv("CYBER_FOREMAN_ACP_HELPER") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	wantArgs := []string{"--no-auto-update", "agent", "stdio"}
	if separator < 0 || !reflect.DeepEqual(os.Args[separator+1:], wantArgs) {
		fmt.Fprintf(os.Stderr, "unexpected helper args: %#v", os.Args)
		os.Exit(20)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	authenticated := false
	xaiAttempted := false
	authOptional := os.Getenv("CYBER_FOREMAN_ACP_HELPER_AUTH_OPTIONAL") == "1"
	for scanner.Scan() {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			os.Exit(21)
		}
		switch message.Method {
		case "initialize":
			writeACPHelperResponse(encoder, message.ID, map[string]any{
				"protocolVersion":   1,
				"agentCapabilities": map[string]any{"loadSession": true},
				"agentInfo":         map[string]string{"name": "fake-grok", "title": "Fake Grok", "version": "test"},
				"authMethods": []map[string]string{
					{"id": "xai.api_key", "name": "API key"},
					{"id": "cached_token", "name": "Cached login"},
				},
			})
		case "authenticate":
			var params struct {
				MethodID string `json:"methodId"`
			}
			if json.Unmarshal(message.Params, &params) != nil {
				os.Exit(22)
			}
			if authOptional {
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0", "id": json.RawMessage(message.ID),
					"error": map[string]any{"code": -32000, "message": "xAI credentials unavailable"},
				})
				continue
			}
			if params.MethodID == "xai.api_key" {
				xaiAttempted = true
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0", "id": json.RawMessage(message.ID),
					"error": map[string]any{"code": -32000, "message": "API key unavailable"},
				})
				continue
			}
			if params.MethodID != "cached_token" || !xaiAttempted {
				os.Exit(22)
			}
			authenticated = true
			writeACPHelperResponse(encoder, message.ID, map[string]any{})
		case "session/new":
			if !authenticated && !authOptional {
				os.Exit(23)
			}
			writeACPHelperResponse(encoder, message.ID, map[string]any{"sessionId": "grok-session", "configOptions": []any{}})
		case "session/cancel":
			// Notification sent during graceful shutdown.
		default:
			if len(message.ID) > 0 {
				writeACPHelperResponse(encoder, message.ID, map[string]any{})
			}
		}
	}
	if err := scanner.Err(); err != nil && !strings.Contains(err.Error(), "file already closed") {
		os.Exit(24)
	}
	os.Exit(0)
}

func writeACPHelperResponse(encoder *json.Encoder, id json.RawMessage, result any) {
	_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}
