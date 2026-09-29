package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxBridgeBodyBytes = 16 * 1024 * 1024

type ProviderProtocol string

const (
	ProtocolChatCompletions ProviderProtocol = "chat-completions"
	ProtocolResponses       ProviderProtocol = "responses"
	ProtocolAnthropic       ProviderProtocol = "anthropic-messages"
)

// ProviderConfig routes Codex's Responses API traffic through a local bridge
// to a Responses, Chat Completions, or Anthropic Messages endpoint. Secrets
// stay in the named environment variable and are never serialized into
// Foreman config.
type ProviderConfig struct {
	BaseURL      string
	APIKeyEnv    string
	DefaultModel string
	Protocol     ProviderProtocol
}

func (c ProviderConfig) validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("Codex API base URL is required")
	}
	parsed, err := url.Parse(c.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid Codex API base URL %q", c.BaseURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("Codex API base URL must use http or https")
	}
	if strings.TrimSpace(c.DefaultModel) == "" {
		return errors.New("Codex API default model is required")
	}
	switch c.Protocol {
	case "", ProtocolChatCompletions, ProtocolResponses, ProtocolAnthropic:
	default:
		return fmt.Errorf("unsupported Codex API protocol %q", c.Protocol)
	}
	return nil
}

type responseBridge struct {
	config   ProviderConfig
	apiKey   string
	server   *http.Server
	listener net.Listener
	client   *http.Client
	close    sync.Once
	seq      atomic.Uint64
	callMu   sync.RWMutex
	calls    map[string]callState
}

func startResponseBridge(ctx context.Context, config ProviderConfig, apiKey string) (*responseBridge, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for Codex API bridge: %w", err)
	}
	bridge := &responseBridge{
		config: config, apiKey: apiKey, listener: listener,
		client: &http.Client{Transport: http.DefaultTransport},
		calls:  make(map[string]callState),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/responses", bridge.handleResponses)
	mux.HandleFunc("GET /v1/models", bridge.handleModels)
	bridge.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = bridge.server.Serve(listener) }()
	go func() {
		<-ctx.Done()
		bridge.Close()
	}()
	return bridge, nil
}

func (b *responseBridge) BaseURL() string {
	return "http://" + b.listener.Addr().String() + "/v1"
}

func (b *responseBridge) Close() {
	b.close.Do(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = b.server.Shutdown(shutdownCtx)
		_ = b.listener.Close()
	})
}

func (b *responseBridge) handleModels(w http.ResponseWriter, _ *http.Request) {
	// Codex expects its own model-catalog envelope here rather than the public
	// OpenAI /models shape. An empty catalog keeps explicitly configured models
	// on Codex's fallback metadata without emitting a failed-refresh error.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
}

type responsesRequest struct {
	Model      string            `json:"model"`
	Input      []json.RawMessage `json:"input"`
	Tools      []json.RawMessage `json:"tools"`
	ToolChoice json.RawMessage   `json:"tool_choice"`
	Reasoning  struct {
		Effort string `json:"effort"`
	} `json:"reasoning"`
}

func (b *responseBridge) handleResponses(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBridgeBodyBytes+1))
	if err != nil {
		writeBridgeError(w, http.StatusBadRequest, "read request: "+err.Error())
		return
	}
	if len(body) > maxBridgeBodyBytes {
		writeBridgeError(w, http.StatusRequestEntityTooLarge, "request exceeds bridge limit")
		return
	}
	if b.config.Protocol == ProtocolResponses {
		b.proxyResponses(w, r, body)
		return
	}
	var request responsesRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeBridgeError(w, http.StatusBadRequest, "decode Responses request: "+err.Error())
		return
	}
	if b.config.Protocol == ProtocolAnthropic {
		b.handleAnthropic(w, r, request)
		return
	}
	chatRequest, err := translateResponsesRequestWithSignatures(request, b.callMetadata)
	if err != nil {
		writeBridgeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload, err := json.Marshal(chatRequest)
	if err != nil {
		writeBridgeError(w, http.StatusInternalServerError, "encode upstream request")
		return
	}
	endpoint := strings.TrimRight(b.config.BaseURL, "/") + "/chat/completions"
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		writeBridgeError(w, http.StatusInternalServerError, "build upstream request")
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Accept", "text/event-stream")
	upstream.Header.Set("User-Agent", "cyber-foreman-codex-bridge/0.1.0")
	if b.apiKey != "" {
		upstream.Header.Set("Authorization", "Bearer "+b.apiKey)
	}
	response, err := b.client.Do(upstream)
	if err != nil {
		writeBridgeError(w, http.StatusBadGateway, "upstream request failed: "+err.Error())
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		writeBridgeError(w, response.StatusCode, "upstream returned "+response.Status+": "+strings.TrimSpace(string(message)))
		return
	}
	if err := b.translateChatStream(w, response.Body); err != nil {
		// Headers may already be committed. Closing without response.completed
		// makes Codex surface a retryable stream error instead of false success.
		return
	}
}

func (b *responseBridge) proxyResponses(w http.ResponseWriter, r *http.Request, body []byte) {
	endpoint := strings.TrimRight(b.config.BaseURL, "/") + "/responses"
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		writeBridgeError(w, http.StatusInternalServerError, "build upstream Responses request")
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Accept", r.Header.Get("Accept"))
	upstream.Header.Set("User-Agent", "cyber-foreman-codex-bridge/0.1.0")
	if b.apiKey != "" {
		upstream.Header.Set("Authorization", "Bearer "+b.apiKey)
	}
	response, err := b.client.Do(upstream)
	if err != nil {
		writeBridgeError(w, http.StatusBadGateway, "upstream request failed: "+err.Error())
		return
	}
	defer response.Body.Close()
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (b *responseBridge) handleAnthropic(w http.ResponseWriter, r *http.Request, request responsesRequest) {
	payloadValue, err := translateAnthropicRequest(request, b.callMetadata)
	if err != nil {
		writeBridgeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload, err := json.Marshal(payloadValue)
	if err != nil {
		writeBridgeError(w, http.StatusInternalServerError, "encode Anthropic request")
		return
	}
	baseURL := strings.TrimRight(b.config.BaseURL, "/")
	endpoint := baseURL + "/v1/messages"
	if strings.HasSuffix(baseURL, "/v1") {
		endpoint = baseURL + "/messages"
	}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		writeBridgeError(w, http.StatusInternalServerError, "build Anthropic request")
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Accept", "text/event-stream")
	upstream.Header.Set("anthropic-version", "2023-06-01")
	upstream.Header.Set("User-Agent", "cyber-foreman-codex-bridge/0.1.0")
	if b.apiKey != "" {
		upstream.Header.Set("x-api-key", b.apiKey)
	}
	response, err := b.client.Do(upstream)
	if err != nil {
		writeBridgeError(w, http.StatusBadGateway, "upstream request failed: "+err.Error())
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		writeBridgeError(w, response.StatusCode, "upstream returned "+response.Status+": "+strings.TrimSpace(string(message)))
		return
	}
	_ = b.translateAnthropicStream(w, response.Body)
}

func translateAnthropicRequest(request responsesRequest, metadata func(string) callState) (map[string]any, error) {
	if strings.TrimSpace(request.Model) == "" {
		return nil, errors.New("Responses request model is required")
	}
	messages, system, err := translateAnthropicInput(request.Input, metadata)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"model": request.Model, "messages": messages, "max_tokens": 16384, "stream": true,
	}
	if system != "" {
		result["system"] = system
	}
	tools := translateAnthropicTools(request.Tools)
	if len(tools) > 0 {
		result["tools"] = tools
		result["tool_choice"] = translateAnthropicToolChoice(request.ToolChoice)
	}
	return result, nil
}

func translateAnthropicInput(items []json.RawMessage, metadata func(string) callState) ([]map[string]any, string, error) {
	messages := make([]map[string]any, 0, len(items))
	systemParts := make([]string, 0)
	appendedThinking := make(map[string]bool)
	appendBlocks := func(role string, blocks []any) {
		if len(blocks) == 0 {
			return
		}
		if len(messages) > 0 && messages[len(messages)-1]["role"] == role {
			existing := messages[len(messages)-1]["content"].([]any)
			messages[len(messages)-1]["content"] = append(existing, blocks...)
			return
		}
		messages = append(messages, map[string]any{"role": role, "content": blocks})
	}

	for _, raw := range items {
		var item map[string]any
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, "", fmt.Errorf("decode Responses input item: %w", err)
		}
		typeName, _ := item["type"].(string)
		switch typeName {
		case "message", "":
			role, _ := item["role"].(string)
			text := anthropicTextContent(item["content"])
			if role == "developer" || role == "system" {
				if text != "" {
					systemParts = append(systemParts, text)
				}
				continue
			}
			if role == "user" || role == "assistant" {
				appendBlocks(role, []any{map[string]any{"type": "text", "text": text}})
			}
		case "function_call", "custom_tool_call":
			name, _ := item["name"].(string)
			arguments, _ := item["arguments"].(string)
			if arguments == "" {
				arguments, _ = item["input"].(string)
			}
			callID, _ := item["call_id"].(string)
			if callID == "" {
				callID, _ = item["id"].(string)
			}
			blocks := make([]any, 0, 2)
			if metadata != nil {
				state := metadata(callID)
				if state.thinking != "" && state.thinkingSignature != "" && !appendedThinking[state.thinkingSignature] {
					blocks = append(blocks, map[string]any{
						"type": "thinking", "thinking": state.thinking, "signature": state.thinkingSignature,
					})
					appendedThinking[state.thinkingSignature] = true
				}
			}
			var input any = map[string]any{}
			if strings.TrimSpace(arguments) != "" && json.Unmarshal([]byte(arguments), &input) != nil {
				input = map[string]any{"raw": arguments}
			}
			blocks = append(blocks, map[string]any{
				"type": "tool_use", "id": callID, "name": name, "input": input,
			})
			appendBlocks("assistant", blocks)
		case "function_call_output", "custom_tool_call_output":
			callID, _ := item["call_id"].(string)
			if callID == "" {
				callID, _ = item["id"].(string)
			}
			appendBlocks("user", []any{map[string]any{
				"type": "tool_result", "tool_use_id": callID, "content": stringifyOutput(item["output"]),
			}})
		}
	}
	if len(messages) == 0 {
		return nil, "", errors.New("Responses request contains no translatable messages")
	}
	return messages, strings.Join(systemParts, "\n\n"), nil
}

func anthropicTextContent(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	parts, ok := value.([]any)
	if !ok {
		return stringifyOutput(value)
	}
	var text strings.Builder
	for _, partValue := range parts {
		part, ok := partValue.(map[string]any)
		if !ok {
			continue
		}
		if value, ok := part["text"].(string); ok {
			text.WriteString(value)
		}
	}
	return text.String()
}

func translateAnthropicTools(rawTools []json.RawMessage) []map[string]any {
	tools := make([]map[string]any, 0, len(rawTools))
	for _, raw := range rawTools {
		var tool map[string]any
		if json.Unmarshal(raw, &tool) != nil || tool["type"] != "function" {
			continue
		}
		translated := map[string]any{"name": tool["name"]}
		if description, ok := tool["description"]; ok {
			translated["description"] = description
		}
		if parameters, ok := tool["parameters"]; ok {
			translated["input_schema"] = parameters
		} else {
			translated["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools = append(tools, translated)
	}
	return tools
}

func translateAnthropicToolChoice(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]string{"type": "auto"}
	}
	var choice any
	if json.Unmarshal(raw, &choice) != nil {
		return map[string]string{"type": "auto"}
	}
	switch value := choice.(type) {
	case string:
		switch value {
		case "none":
			return map[string]string{"type": "none"}
		case "required":
			return map[string]string{"type": "any"}
		default:
			return map[string]string{"type": "auto"}
		}
	case map[string]any:
		if value["type"] == "function" {
			name, _ := value["name"].(string)
			return map[string]string{"type": "tool", "name": name}
		}
	}
	return map[string]string{"type": "auto"}
}

func translateResponsesRequest(request responsesRequest) (map[string]any, error) {
	return translateResponsesRequestWithSignatures(request, nil)
}

func translateResponsesRequestWithSignatures(request responsesRequest, metadata func(string) callState) (map[string]any, error) {
	if strings.TrimSpace(request.Model) == "" {
		return nil, errors.New("Responses request model is required")
	}
	messages, err := translateInput(request.Input, metadata)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"model": request.Model, "messages": messages, "stream": true,
	}
	tools := translateTools(request.Tools)
	if len(tools) > 0 {
		result["tools"] = tools
		result["tool_choice"] = translateToolChoice(request.ToolChoice)
	}
	if request.Reasoning.Effort != "" {
		result["reasoning_effort"] = request.Reasoning.Effort
	}
	return result, nil
}

func translateInput(items []json.RawMessage, metadata func(string) callState) ([]map[string]any, error) {
	messages := make([]map[string]any, 0, len(items))
	pendingToolCalls := make([]any, 0)
	pendingReasoning := ""
	flushToolCalls := func() {
		if len(pendingToolCalls) == 0 {
			return
		}
		message := map[string]any{
			"role": "assistant", "content": nil, "tool_calls": pendingToolCalls,
		}
		if pendingReasoning != "" {
			message["reasoning_content"] = pendingReasoning
		}
		messages = append(messages, message)
		pendingToolCalls = nil
		pendingReasoning = ""
	}
	for _, raw := range items {
		var item map[string]any
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("decode Responses input item: %w", err)
		}
		typeName, _ := item["type"].(string)
		switch typeName {
		case "message", "":
			flushToolCalls()
			role, _ := item["role"].(string)
			if role == "developer" {
				role = "system"
			}
			if role == "" {
				continue
			}
			content := translateMessageContent(item["content"])
			messages = append(messages, map[string]any{"role": role, "content": content})
		case "function_call", "custom_tool_call":
			name, _ := item["name"].(string)
			arguments, _ := item["arguments"].(string)
			if arguments == "" {
				arguments, _ = item["input"].(string)
			}
			callID, _ := item["call_id"].(string)
			if callID == "" {
				callID, _ = item["id"].(string)
			}
			toolCall := map[string]any{
				"id": callID, "type": "function",
				"function": map[string]string{"name": name, "arguments": arguments},
			}
			if metadata != nil {
				state := metadata(callID)
				if state.thoughtSignature != "" {
					toolCall["extra_content"] = map[string]any{
						"google": map[string]string{"thought_signature": state.thoughtSignature},
					}
				}
				if pendingReasoning == "" {
					pendingReasoning = state.reasoningContent
				}
			}
			pendingToolCalls = append(pendingToolCalls, toolCall)
		case "function_call_output", "custom_tool_call_output":
			flushToolCalls()
			callID, _ := item["call_id"].(string)
			if callID == "" {
				callID, _ = item["id"].(string)
			}
			messages = append(messages, map[string]any{
				"role": "tool", "tool_call_id": callID, "content": stringifyOutput(item["output"]),
			})
		}
	}
	flushToolCalls()
	if len(messages) == 0 {
		return nil, errors.New("Responses request contains no translatable messages")
	}
	return messages, nil
}

func translateMessageContent(value any) any {
	if text, ok := value.(string); ok {
		return text
	}
	parts, ok := value.([]any)
	if !ok {
		return stringifyOutput(value)
	}
	translated := make([]map[string]any, 0, len(parts))
	for _, partValue := range parts {
		part, ok := partValue.(map[string]any)
		if !ok {
			continue
		}
		typeName, _ := part["type"].(string)
		switch typeName {
		case "input_text", "output_text", "text":
			if text, ok := part["text"].(string); ok {
				translated = append(translated, map[string]any{"type": "text", "text": text})
			}
		case "input_image":
			if imageURL, ok := part["image_url"].(string); ok {
				translated = append(translated, map[string]any{"type": "image_url", "image_url": map[string]string{"url": imageURL}})
			}
		}
	}
	if len(translated) == 1 && translated[0]["type"] == "text" {
		return translated[0]["text"]
	}
	return translated
}

func stringifyOutput(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(payload)
}

func translateTools(rawTools []json.RawMessage) []map[string]any {
	tools := make([]map[string]any, 0, len(rawTools))
	for _, raw := range rawTools {
		var tool map[string]any
		if json.Unmarshal(raw, &tool) != nil || tool["type"] != "function" {
			continue
		}
		function := map[string]any{"name": tool["name"]}
		if description, ok := tool["description"]; ok {
			function["description"] = description
		}
		if parameters, ok := tool["parameters"]; ok {
			function["parameters"] = parameters
		} else {
			function["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		if strict, ok := tool["strict"]; ok {
			function["strict"] = strict
		}
		tools = append(tools, map[string]any{"type": "function", "function": function})
	}
	return tools
}

func translateToolChoice(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return "auto"
	}
	var choice any
	if json.Unmarshal(raw, &choice) != nil {
		return "auto"
	}
	switch value := choice.(type) {
	case string:
		if value == "none" || value == "required" || value == "auto" {
			return value
		}
	case map[string]any:
		if value["type"] == "function" {
			return map[string]any{"type": "function", "function": map[string]any{"name": value["name"]}}
		}
	}
	return "auto"
}

type chatStreamChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
				ExtraContent struct {
					Google struct {
						ThoughtSignature string `json:"thought_signature"`
					} `json:"google"`
				} `json:"extra_content"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
}

type streamedToolCall struct {
	id                string
	name              string
	thoughtSignature  string
	reasoningContent  string
	thinking          string
	thinkingSignature string
	arguments         strings.Builder
}

type callState struct {
	thoughtSignature  string
	reasoningContent  string
	thinking          string
	thinkingSignature string
}

func (b *responseBridge) callMetadata(callID string) callState {
	b.callMu.RLock()
	defer b.callMu.RUnlock()
	return b.calls[callID]
}

func (b *responseBridge) rememberCall(callID string, state callState) {
	if callID == "" || state == (callState{}) {
		return
	}
	b.callMu.Lock()
	defer b.callMu.Unlock()
	if b.calls == nil {
		b.calls = make(map[string]callState)
	}
	b.calls[callID] = state
}

func (b *responseBridge) translateChatStream(w http.ResponseWriter, body io.Reader) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("streaming is unavailable")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	responseID := fmt.Sprintf("resp_foreman_%d_%d", time.Now().UnixNano(), b.seq.Add(1))
	messageID := "msg_" + responseID
	emitSSE(w, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": responseID, "status": "in_progress"}})
	messageStarted := false
	var textOutput strings.Builder
	var reasoningOutput strings.Builder
	toolCalls := make(map[int]*streamedToolCall)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxBridgeBodyBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode Chat Completions stream: %w", err)
		}
		for _, choice := range chunk.Choices {
			reasoningOutput.WriteString(choice.Delta.ReasoningContent)
			if choice.Delta.Content != "" {
				if !messageStarted {
					messageStarted = true
					emitSSE(w, "response.output_item.added", map[string]any{
						"type": "response.output_item.added", "output_index": 0,
						"item": map[string]any{"id": messageID, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}},
					})
					emitSSE(w, "response.content_part.added", map[string]any{
						"type": "response.content_part.added", "item_id": messageID, "output_index": 0, "content_index": 0,
						"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
					})
				}
				textOutput.WriteString(choice.Delta.Content)
				emitSSE(w, "response.output_text.delta", map[string]any{
					"type": "response.output_text.delta", "item_id": messageID,
					"output_index": 0, "content_index": 0, "delta": choice.Delta.Content,
				})
				flusher.Flush()
			}
			for _, delta := range choice.Delta.ToolCalls {
				call := toolCalls[delta.Index]
				if call == nil {
					call = &streamedToolCall{}
					toolCalls[delta.Index] = call
				}
				if delta.ID != "" {
					call.id = delta.ID
				}
				if delta.Function.Name != "" {
					call.name = delta.Function.Name
				}
				if delta.ExtraContent.Google.ThoughtSignature != "" {
					call.thoughtSignature = delta.ExtraContent.Google.ThoughtSignature
				}
				call.arguments.WriteString(delta.Function.Arguments)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Chat Completions stream: %w", err)
	}

	for _, call := range toolCalls {
		call.reasoningContent = reasoningOutput.String()
	}
	b.emitCompletedResponse(w, responseID, messageID, messageStarted, textOutput.String(), toolCalls)
	flusher.Flush()
	return nil
}

type anthropicStreamEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
}

func (b *responseBridge) translateAnthropicStream(w http.ResponseWriter, body io.Reader) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("streaming is unavailable")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	responseID := fmt.Sprintf("resp_foreman_%d_%d", time.Now().UnixNano(), b.seq.Add(1))
	messageID := "msg_" + responseID
	emitSSE(w, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": responseID, "status": "in_progress"}})
	messageStarted := false
	var textOutput strings.Builder
	var thinkingOutput strings.Builder
	thinkingSignature := ""
	toolCalls := make(map[int]*streamedToolCall)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxBridgeBodyBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("decode Anthropic stream: %w", err)
		}
		switch event.Type {
		case "content_block_start":
			switch event.ContentBlock.Type {
			case "thinking":
				thinkingOutput.WriteString(event.ContentBlock.Thinking)
				if event.ContentBlock.Signature != "" {
					thinkingSignature = event.ContentBlock.Signature
				}
			case "tool_use":
				toolCalls[event.Index] = &streamedToolCall{id: event.ContentBlock.ID, name: event.ContentBlock.Name}
			}
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				if !messageStarted {
					messageStarted = true
					emitMessageStarted(w, messageID)
				}
				textOutput.WriteString(event.Delta.Text)
				emitSSE(w, "response.output_text.delta", map[string]any{
					"type": "response.output_text.delta", "item_id": messageID,
					"output_index": 0, "content_index": 0, "delta": event.Delta.Text,
				})
				flusher.Flush()
			case "thinking_delta":
				thinkingOutput.WriteString(event.Delta.Thinking)
			case "signature_delta":
				thinkingSignature += event.Delta.Signature
			case "input_json_delta":
				call := toolCalls[event.Index]
				if call == nil {
					call = &streamedToolCall{}
					toolCalls[event.Index] = call
				}
				call.arguments.WriteString(event.Delta.PartialJSON)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Anthropic stream: %w", err)
	}
	for _, call := range toolCalls {
		call.thinking = thinkingOutput.String()
		call.thinkingSignature = thinkingSignature
	}
	b.emitCompletedResponse(w, responseID, messageID, messageStarted, textOutput.String(), toolCalls)
	flusher.Flush()
	return nil
}

func emitMessageStarted(w io.Writer, messageID string) {
	emitSSE(w, "response.output_item.added", map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{"id": messageID, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}},
	})
	emitSSE(w, "response.content_part.added", map[string]any{
		"type": "response.content_part.added", "item_id": messageID, "output_index": 0, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	})
}

func (b *responseBridge) emitCompletedResponse(w io.Writer, responseID, messageID string, messageStarted bool, text string, toolCalls map[int]*streamedToolCall) {
	output := make([]any, 0, 1+len(toolCalls))
	outputIndex := 0
	if messageStarted || len(toolCalls) == 0 {
		message := map[string]any{
			"id": messageID, "type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
		}
		emitSSE(w, "response.output_text.done", map[string]any{"type": "response.output_text.done", "item_id": messageID, "output_index": outputIndex, "content_index": 0, "text": text})
		emitSSE(w, "response.content_part.done", map[string]any{"type": "response.content_part.done", "item_id": messageID, "output_index": outputIndex, "content_index": 0, "part": message["content"].([]any)[0]})
		emitSSE(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": outputIndex, "item": message})
		output = append(output, message)
		outputIndex++
	}
	indices := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		call := toolCalls[index]
		if call == nil {
			continue
		}
		if call.id == "" {
			call.id = fmt.Sprintf("call_%s_%d", responseID, index)
		}
		b.rememberCall(call.id, callState{
			thoughtSignature:  call.thoughtSignature,
			reasoningContent:  call.reasoningContent,
			thinking:          call.thinking,
			thinkingSignature: call.thinkingSignature,
		})
		item := map[string]any{
			"id": "fc_" + call.id, "type": "function_call", "status": "completed",
			"call_id": call.id, "name": call.name, "arguments": call.arguments.String(),
		}
		emitSSE(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": outputIndex, "item": item})
		output = append(output, item)
		outputIndex++
	}
	completed := map[string]any{
		"id": responseID, "object": "response", "status": "completed", "output": output,
		"usage": map[string]any{
			"input_tokens": 0, "output_tokens": 0, "total_tokens": 0,
			"input_tokens_details":  map[string]int{"cached_tokens": 0},
			"output_tokens_details": map[string]int{"reasoning_tokens": 0},
		},
	}
	emitSSE(w, "response.completed", map[string]any{"type": "response.completed", "response": completed})
}

func emitSSE(w io.Writer, eventType string, event any) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, payload)
}

func writeBridgeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": "upstream_error", "message": message}})
}
