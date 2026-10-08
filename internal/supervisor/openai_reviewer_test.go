package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestOpenAIReviewerUsesChatCompletionsAndParsesJSON(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request path=%q auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != "deepseek-chat" || len(payload.Messages) != 2 || !strings.Contains(payload.Messages[1].Content, "untrusted_agent_response") {
			t.Fatalf("unexpected payload: %#v", payload)
		}
		body := "{\"choices\":[{\"message\":{\"content\":\"```json\\n{\\\"verdict\\\":\\\"redirect\\\",\\\"reason\\\":\\\"missing tests\\\",\\\"follow_up\\\":\\\"Run the required tests and report the result.\\\"}\\n```\"}}]}"
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	reviewer, err := NewOpenAIReviewer(OpenAIReviewerConfig{
		BaseURL: "https://example.test/v1", APIKey: "secret", Model: "deepseek-chat", Client: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	review, err := reviewer.Review(context.Background(), SemanticReviewRequest{
		TrustedInstructions: []string{"implement feature"}, AgentResponse: "I changed the code.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.Verdict != SemanticRedirect || review.FollowUp == "" || reviewer.Descriptor().Model != "deepseek-chat" {
		t.Fatalf("unexpected review: %#v", review)
	}
}

func TestParseSemanticReviewRejectsUnsafeOrMalformedOutput(t *testing.T) {
	for name, content := range map[string]string{
		"unknown verdict":  `{"verdict":"stop","reason":"x","follow_up":"x"}`,
		"missing followup": `{"verdict":"redirect","reason":"x","follow_up":""}`,
		"unknown field":    `{"verdict":"pass","reason":"x","follow_up":"","action":"stop"}`,
		"trailing":         `{"verdict":"pass","reason":"x","follow_up":""} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSemanticReview(content); err == nil {
				t.Fatal("invalid review was accepted")
			}
		})
	}
}

func TestOpenAIReviewerDoesNotExposeKeyInHTTPError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader("upstream unavailable")),
		}, nil
	})}
	reviewer, err := NewOpenAIReviewer(OpenAIReviewerConfig{BaseURL: "https://example.test", APIKey: "top-secret", Model: "model", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reviewer.Review(context.Background(), SemanticReviewRequest{AgentResponse: "done"})
	if err == nil || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestOpenAIReviewerSupportsUnauthenticatedLocalEndpoint(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if authorization := request.Header.Get("Authorization"); authorization != "" {
			t.Fatalf("unexpected authorization header %q", authorization)
		}
		body := `{"choices":[{"message":{"content":"{\"verdict\":\"pass\",\"reason\":\"aligned\",\"follow_up\":\"\"}"}}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	reviewer, err := NewOpenAIReviewer(OpenAIReviewerConfig{BaseURL: "http://qwen.internal/v1", Model: "qwen", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reviewer.Review(context.Background(), SemanticReviewRequest{AgentResponse: "done"})
	if err != nil || result.Verdict != SemanticPass {
		t.Fatalf("review=%#v err=%v", result, err)
	}
}

func TestOpenAIReviewerUsesBoundedForemanToolsBeforeRequestingFollowUp(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		var payload struct {
			Tools    []any            `json:"tools"`
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Tools) < 4 {
			t.Fatalf("tools=%d, want read tools plus terminal actions", len(payload.Tools))
		}
		var body string
		if requests == 1 {
			body = `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call-read","type":"function","function":{"name":"inspect_task_state","arguments":"{}"}}]}}]}`
		} else {
			if len(payload.Messages) < 4 || payload.Messages[len(payload.Messages)-1]["role"] != "tool" {
				t.Fatalf("tool result was not returned to model: %#v", payload.Messages)
			}
			body = `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call-fix","type":"function","function":{"name":"request_follow_up","arguments":"{\"reason\":\"missing test result\",\"instruction\":\"Run the configured test and report the result.\"}"}}]}}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	reviewer, err := NewOpenAIReviewer(OpenAIReviewerConfig{
		BaseURL: "https://example.test/v1", Model: "deepseek-chat", Client: client, ToolCalling: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	toolbox := &fakeSemanticToolbox{}
	review, err := reviewer.Review(context.Background(), SemanticReviewRequest{
		AgentResponse: "done", Toolbox: toolbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.Verdict != SemanticRedirect || review.ToolCall != "request_follow_up" || !strings.Contains(review.FollowUp, "configured test") {
		t.Fatalf("review=%#v", review)
	}
	if requests != 2 || toolbox.calls != 1 {
		t.Fatalf("requests=%d toolbox calls=%d", requests, toolbox.calls)
	}
}

func TestOpenAIReviewerRejectsAmbiguousTerminalToolCalls(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"choices":[{"message":{"tool_calls":[` +
			`{"id":"one","type":"function","function":{"name":"accept_turn","arguments":"{\"reason\":\"ok\"}"}},` +
			`{"id":"two","type":"function","function":{"name":"request_follow_up","arguments":"{\"reason\":\"bad\",\"instruction\":\"fix\"}"}}]}}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	reviewer, err := NewOpenAIReviewer(OpenAIReviewerConfig{
		BaseURL: "https://example.test/v1", Model: "model", Client: client, ToolCalling: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reviewer.Review(context.Background(), SemanticReviewRequest{AgentResponse: "done", Toolbox: &fakeSemanticToolbox{}})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("error=%v", err)
	}
}

func TestOpenAIReviewerAllowsExplicitOperatorAttentionTool(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, tool := range payload.Tools {
			found = found || tool.Function.Name == "request_operator_attention"
		}
		if !found {
			t.Fatal("operator attention tool was not advertised after explicit opt-in")
		}
		body := `{"choices":[{"message":{"tool_calls":[{"id":"attention","type":"function","function":{"name":"request_operator_attention","arguments":"{\"reason\":\"migration target is ambiguous\"}"}}]}}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	reviewer, err := NewOpenAIReviewer(OpenAIReviewerConfig{
		BaseURL: "https://example.test/v1", Model: "model", Client: client,
		ToolCalling: true, AllowAttention: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	review, err := reviewer.Review(context.Background(), SemanticReviewRequest{
		AgentResponse: "done", Toolbox: &fakeSemanticToolbox{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.Verdict != SemanticAttention || review.ToolCall != "request_operator_attention" {
		t.Fatalf("review=%#v", review)
	}
}

func TestOpenAIReviewerMidTurnDisablesAttentionAndMarksPhase(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Messages) != 2 || !strings.Contains(payload.Messages[1].Content, `"review_phase":"mid_turn"`) {
			t.Fatalf("mid-turn phase missing from request: %#v", payload.Messages)
		}
		for _, tool := range payload.Tools {
			if tool.Function.Name == "request_operator_attention" {
				t.Fatal("operator attention must not be advertised during a mid-turn review")
			}
		}
		body := `{"choices":[{"message":{"tool_calls":[{"id":"redirect","type":"function","function":{"name":"request_follow_up","arguments":"{\"reason\":\"agent is editing the wrong package\",\"instruction\":\"Stop editing package B and implement the requested change in package A.\"}"}}]}}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	reviewer, err := NewOpenAIReviewer(OpenAIReviewerConfig{
		BaseURL: "https://example.test/v1", Model: "model", Client: client,
		ToolCalling: true, AllowAttention: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	review, err := reviewer.Review(context.Background(), SemanticReviewRequest{
		Phase: SemanticReviewMidTurn, AgentResponse: "working in package B", Toolbox: &fakeSemanticToolbox{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.Verdict != SemanticRedirect || review.ToolCall != "request_follow_up" {
		t.Fatalf("review=%#v", review)
	}
}

type fakeSemanticToolbox struct {
	calls int
}

func (f *fakeSemanticToolbox) SupportedTools() []SemanticTool {
	return []SemanticTool{SemanticToolInspectTask}
}

func (f *fakeSemanticToolbox) ExecuteSemanticTool(_ context.Context, name SemanticTool, arguments json.RawMessage) (string, error) {
	f.calls++
	if name != SemanticToolInspectTask || string(arguments) != `{}` {
		return "", errors.New("unexpected tool request")
	}
	return `{"status":"verifying"}`, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
