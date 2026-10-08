package supervisor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
