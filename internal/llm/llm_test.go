package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFakeRepeatsLastResponseAndRecordsRequests(t *testing.T) {
	fake := NewFake(
		FakeResponse{Response: Response{Text: "first"}},
		FakeResponse{Response: Response{Text: "second"}},
	)
	for i := 0; i < 3; i++ {
		if _, err := fake.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "q"}}}); err != nil {
			t.Fatalf("chat %d: %v", i, err)
		}
	}
	if got := fake.CallsCount(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
	if fake.Requests()[0].Messages[0].Text != "q" {
		t.Fatalf("request was not recorded")
	}
}

func TestValidateRequestRejectsForcedToolNotListed(t *testing.T) {
	err := ValidateRequest(Request{
		Messages:  []Message{{Role: RoleUser, Text: "q"}},
		ForceTool: "missing",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestClaudeBuildsCachedSystemAndForcedTool(t *testing.T) {
	var seen map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &seen); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Fatalf("missing api key header")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{
				"type": "tool_use", "id": "tool-1", "name": "answer", "input": map[string]any{"intent": "status"},
			}},
			"stop_reason": "tool_use",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	defer server.Close()

	client, err := NewClaude(ClaudeOptions{
		APIKey: "test-key", BaseURL: server.URL, Model: "pinned-model",
		InputPrice: 3, OutputPrice: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	schema := json.RawMessage(`{"type":"object","properties":{"intent":{"type":"string"}}}`)
	response, err := client.Chat(context.Background(), Request{
		System:    "fixed system prompt",
		Messages:  []Message{{Role: RoleUser, Text: "is it registered?"}},
		Tools:     []Tool{{Name: "answer", Description: "answer", Schema: schema}},
		ForceTool: "answer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "answer" {
		t.Fatalf("tool calls = %#v", response.ToolCalls)
	}
	if response.Usage.CostUSD <= 0 {
		t.Fatalf("cost was not estimated: %#v", response.Usage)
	}

	system, ok := seen["system"].([]any)
	if !ok || len(system) != 1 {
		t.Fatalf("system = %#v", seen["system"])
	}
	first := system[0].(map[string]any)
	if first["cache_control"] == nil {
		t.Fatalf("system prompt was not marked cacheable: %#v", first)
	}
	choice := seen["tool_choice"].(map[string]any)
	if choice["type"] != "tool" || choice["name"] != "answer" {
		t.Fatalf("tool_choice = %#v", choice)
	}
}

func TestClaudeSendsToolResultsAsUserBlocks(t *testing.T) {
	var seen map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "answer"}},
		})
	}))
	defer server.Close()

	client, err := NewClaude(ClaudeOptions{APIKey: "k", BaseURL: server.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Chat(context.Background(), Request{Messages: []Message{
		{Role: RoleUser, Text: "q"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "t", Input: json.RawMessage(`{}`)}}},
		{Role: RoleUser, ToolResults: []ToolResult{{ToolCallID: "call-1", Content: `{"ok":true}`}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	messages := seen["messages"].([]any)
	last := messages[len(messages)-1].(map[string]any)
	content := last["content"].([]any)
	block := content[0].(map[string]any)
	if block["type"] != "tool_result" || block["tool_use_id"] != "call-1" {
		t.Fatalf("tool result block = %#v", block)
	}
}

func TestClaudeMarksRetryableStatusesTransientAndNeverEchoesBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("secret prompt text"))
	}))
	defer server.Close()

	client, err := NewClaude(ClaudeOptions{APIKey: "k", BaseURL: server.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "q"}}})
	if !IsTransient(err) {
		t.Fatalf("expected a transient error, got %v", err)
	}
	if strings.Contains(err.Error(), "secret prompt") {
		t.Fatalf("error leaked the response body: %v", err)
	}
}

func TestClaudePermanentStatusIsNotTransient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	client, err := NewClaude(ClaudeOptions{APIKey: "k", BaseURL: server.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "q"}}})
	if err == nil || IsTransient(err) || errors.Is(err, ErrTransient) {
		t.Fatalf("expected a permanent error, got %v", err)
	}
}
