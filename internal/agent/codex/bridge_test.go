package codex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTranslateResponsesRequest(t *testing.T) {
	request := responsesRequest{
		Model: "gemini-test",
		Input: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"developer","content":[{"type":"input_text","text":"rules"}]}`),
			json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_text","text":"run pwd"}]}`),
			json.RawMessage(`{"type":"function_call","call_id":"call-1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"}`),
			json.RawMessage(`{"type":"function_call","call_id":"call-2","name":"exec_command","arguments":"{\"cmd\":\"git status\"}"}`),
			json.RawMessage(`{"type":"function_call_output","call_id":"call-1","output":"/workspace"}`),
			json.RawMessage(`{"type":"function_call_output","call_id":"call-2","output":"clean"}`),
		},
		Tools: []json.RawMessage{
			json.RawMessage(`{"type":"function","name":"exec_command","description":"run a command","parameters":{"type":"object"}}`),
			json.RawMessage(`{"type":"web_search"}`),
		},
		ToolChoice: json.RawMessage(`"auto"`),
	}
	translated, err := translateResponsesRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	messages := translated["messages"].([]map[string]any)
	wantRoles := []string{"system", "user", "assistant", "tool", "tool"}
	if len(messages) != len(wantRoles) {
		t.Fatalf("messages = %#v", messages)
	}
	for index, want := range wantRoles {
		if messages[index]["role"] != want {
			t.Fatalf("message %d role = %#v", index, messages[index]["role"])
		}
	}
	toolCalls := messages[2]["tool_calls"].([]any)
	if len(toolCalls) != 2 {
		t.Fatalf("parallel tool calls were not grouped: %#v", messages[2])
	}
	tools := translated["tools"].([]map[string]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	function := tools[0]["function"].(map[string]any)
	if function["name"] != "exec_command" {
		t.Fatalf("function = %#v", function)
	}
}

func TestBridgeTranslatesChatStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("authorization header was not forwarded")
		}
		body, _ := io.ReadAll(r.Body)
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if request["model"] != "gemini-test" || request["stream"] != true {
			t.Errorf("request = %#v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chat-1\",\"choices\":[{\"delta\":{\"content\":\"HELLO\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chat-1\",\"choices\":[{\"delta\":{\"content\":\"_WORLD\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	bridge := &responseBridge{
		config: ProviderConfig{BaseURL: upstream.URL, DefaultModel: "gemini-test"},
		apiKey: "test-secret", client: upstream.Client(),
	}
	payload := `{"model":"gemini-test","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"stream":true}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload)).WithContext(context.Background())
	bridge.handleResponses(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{"response.output_item.added", "response.output_text.delta", "HELLO", "_WORLD", "response.output_item.done", "response.completed"} {
		if !strings.Contains(body, want) {
			t.Fatalf("bridge response missing %q: %s", want, body)
		}
	}
}

func TestBridgeModelsUsesCodexCatalogEnvelope(t *testing.T) {
	bridge := &responseBridge{}
	recorder := httptest.NewRecorder()
	bridge.handleModels(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var catalog struct {
		Models []any `json:"models"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 0 {
		t.Fatalf("models = %#v", catalog.Models)
	}
}

func TestBridgeTranslatesToolCall(t *testing.T) {
	bridge := &responseBridge{}
	chatStream := strings.NewReader(`data: {"choices":[{"delta":{"reasoning_content":"reasoning-"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"reasoning_content":"state","tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":"},"extra_content":{"google":{"thought_signature":"signed-thought"}}}]}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"pwd\"}"}}]}}]}` + "\n\n" +
		"data: [DONE]\n\n")
	recorder := httptest.NewRecorder()
	if err := bridge.translateChatStream(recorder, chatStream); err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	for _, want := range []string{`"type":"function_call"`, `"call_id":"call-1"`, `"name":"exec_command"`, `{\"cmd\":\"pwd\"}`} {
		if !strings.Contains(body, want) {
			t.Fatalf("tool response missing %q: %s", want, body)
		}
	}
	translated, err := translateResponsesRequestWithSignatures(responsesRequest{
		Model: "gemini-test",
		Input: []json.RawMessage{
			json.RawMessage(`{"type":"function_call","call_id":"call-1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"}`),
			json.RawMessage(`{"type":"function_call_output","call_id":"call-1","output":"/workspace"}`),
		},
	}, bridge.callMetadata)
	if err != nil {
		t.Fatal(err)
	}
	messages := translated["messages"].([]map[string]any)
	toolCalls := messages[0]["tool_calls"].([]any)
	toolCall := toolCalls[0].(map[string]any)
	if messages[0]["reasoning_content"] != "reasoning-state" {
		t.Fatalf("reasoning content was not preserved: %#v", messages[0])
	}
	extraContent := toolCall["extra_content"].(map[string]any)
	google := extraContent["google"].(map[string]string)
	if google["thought_signature"] != "signed-thought" {
		t.Fatalf("thought signature was not preserved: %#v", toolCall)
	}
}

func TestBridgeProxiesResponsesAPI(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer response-secret" {
			t.Errorf("authorization header was not forwarded")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: passthrough\n\n")
	}))
	defer upstream.Close()
	bridge := &responseBridge{
		config: ProviderConfig{BaseURL: upstream.URL, DefaultModel: "deepseek", Protocol: ProtocolResponses},
		apiKey: "response-secret", client: upstream.Client(),
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"deepseek","input":"hello"}`))
	bridge.handleResponses(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "data: passthrough\n\n" {
		t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
}

func TestBridgeTranslatesAnthropicToolCall(t *testing.T) {
	bridge := &responseBridge{}
	stream := strings.NewReader(
		`data: {"type":"message_start"}` + "\n\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}` + "\n\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"need pwd"}}` + "\n\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed-reasoning"}}` + "\n\n" +
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call-a","name":"exec_command","input":{}}}` + "\n\n" +
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"pwd\"}"}}` + "\n\n" +
			`data: {"type":"message_stop"}` + "\n\n",
	)
	recorder := httptest.NewRecorder()
	if err := bridge.translateAnthropicStream(recorder, stream); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorder.Body.String(), `"call_id":"call-a"`) {
		t.Fatalf("tool call was not translated: %s", recorder.Body.String())
	}
	request := responsesRequest{
		Model: "deepseek",
		Input: []json.RawMessage{
			json.RawMessage(`{"type":"function_call","call_id":"call-a","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"}`),
			json.RawMessage(`{"type":"function_call_output","call_id":"call-a","output":"/workspace"}`),
		},
	}
	translated, err := translateAnthropicRequest(request, bridge.callMetadata)
	if err != nil {
		t.Fatal(err)
	}
	messages := translated["messages"].([]map[string]any)
	assistantBlocks := messages[0]["content"].([]any)
	thinking := assistantBlocks[0].(map[string]any)
	if thinking["thinking"] != "need pwd" || thinking["signature"] != "signed-reasoning" {
		t.Fatalf("Anthropic thinking state was not preserved: %#v", assistantBlocks)
	}
	toolUse := assistantBlocks[1].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["id"] != "call-a" {
		t.Fatalf("Anthropic tool use = %#v", toolUse)
	}
}

func TestTranslateAnthropicParallelToolCallsKeepsOneThinkingBlock(t *testing.T) {
	state := callState{thinking: "choose tools", thinkingSignature: "signed-once"}
	request := responsesRequest{
		Model: "deepseek",
		Input: []json.RawMessage{
			json.RawMessage(`{"type":"function_call","call_id":"call-a","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"}`),
			json.RawMessage(`{"type":"function_call","call_id":"call-b","name":"exec_command","arguments":"{\"cmd\":\"git status\"}"}`),
		},
	}
	translated, err := translateAnthropicRequest(request, func(string) callState { return state })
	if err != nil {
		t.Fatal(err)
	}
	messages := translated["messages"].([]map[string]any)
	blocks := messages[0]["content"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("expected one thinking block and two tool calls, got %#v", blocks)
	}
	if blocks[0].(map[string]any)["type"] != "thinking" || blocks[1].(map[string]any)["type"] != "tool_use" || blocks[2].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("unexpected Anthropic blocks: %#v", blocks)
	}
}

func TestProviderConfigValidation(t *testing.T) {
	for name, config := range map[string]ProviderConfig{
		"missing URL":    {DefaultModel: "model"},
		"invalid scheme": {BaseURL: "file:///tmp/api", DefaultModel: "model"},
		"missing model":  {BaseURL: "http://127.0.0.1:9000"},
		"bad protocol":   {BaseURL: "http://127.0.0.1:9000", DefaultModel: "model", Protocol: "google-native"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := config.validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
