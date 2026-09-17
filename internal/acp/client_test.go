package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestCallNotificationAndReverseRequest(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	client := NewClient(context.Background(), clientSide, clientSide, func(_ context.Context, method string, _ json.RawMessage) (any, *RPCError) {
		if method != "session/request_permission" {
			return nil, &RPCError{Code: -32601, Message: "unexpected reverse method " + method}
		}
		return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}, nil
	})

	serverDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(serverSide)
		requestLine, err := reader.ReadBytes('\n')
		if err != nil {
			serverDone <- err
			return
		}
		var request wireMessage
		if err := json.Unmarshal(requestLine, &request); err != nil {
			serverDone <- err
			return
		}
		messages := []any{
			map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "s1", "update": map[string]any{"sessionUpdate": "agent_message_chunk"}}},
			map[string]any{"jsonrpc": "2.0", "id": 99, "method": "session/request_permission", "params": map[string]any{}},
			map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(request.ID), "result": map[string]string{"stopReason": "end_turn"}},
		}
		encoder := json.NewEncoder(serverSide)
		for _, message := range messages {
			if err := encoder.Encode(message); err != nil {
				serverDone <- err
				return
			}
		}
		if _, err := reader.ReadBytes('\n'); err != nil {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()

	var result struct {
		StopReason string `json:"stopReason"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Call(ctx, "session/prompt", map[string]string{"sessionId": "s1"}, &result); err != nil {
		t.Fatal(err)
	}
	if result.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
	select {
	case notification := <-client.Notifications():
		if notification.Method != "session/update" {
			t.Fatalf("notification method = %q", notification.Method)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for notification")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestDisconnectFailsPendingCall(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	client := NewClient(context.Background(), clientSide, clientSide, nil)
	done := make(chan error, 1)
	go func() {
		var result map[string]any
		done <- client.Call(context.Background(), "initialize", map[string]any{}, &result)
	}()
	reader := bufio.NewReader(serverSide)
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	_ = serverSide.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected disconnect error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending call did not fail after disconnect")
	}
}
