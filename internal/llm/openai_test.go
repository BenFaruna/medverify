package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newOpenAIClient(t *testing.T, handler http.Handler) *OpenAI {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := NewOpenAI(OpenAIOptions{
		APIKey:    "test-key",
		BaseURL:   server.URL + "/v1",
		Model:     "test-chat-model",
		MaxTokens: 256,
	})
	require.NoError(t, err)
	return client
}

func TestOpenAIChatSendsSystemToolsAndForcedChoice(t *testing.T) {
	var seen map[string]any
	client := newOpenAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("authorization"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &seen))

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "tool_calls",
				"message": map[string]any{
					"tool_calls": []map[string]any{{
						"id":   "call-1",
						"type": "function",
						"function": map[string]any{
							"name":      "route_question",
							"arguments": `{"intent":"status"}`,
						},
					}},
				},
			}},
			"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20},
		})
	}))

	response, err := client.Chat(context.Background(), Request{
		System:    "fixed system prompt",
		Messages:  []Message{{Role: RoleUser, Text: "is this registered?"}},
		Tools:     []Tool{{Name: "route_question", Description: "classify", Schema: json.RawMessage(`{"type":"object"}`)}},
		ForceTool: "route_question",
	})
	require.NoError(t, err)
	assert.Equal(t, OpenAIProvider, response.Provider)
	assert.Equal(t, "test-chat-model", response.Model)
	assert.Equal(t, 100, response.Usage.InputTokens)
	assert.Equal(t, 20, response.Usage.OutputTokens)
	require.Len(t, response.ToolCalls, 1)
	assert.Equal(t, "route_question", response.ToolCalls[0].Name)
	assert.JSONEq(t, `{"intent":"status"}`, string(response.ToolCalls[0].Input))

	messages := seen["messages"].([]any)
	require.Len(t, messages, 2)
	assert.Equal(t, "system", messages[0].(map[string]any)["role"])
	assert.Equal(t, "fixed system prompt", messages[0].(map[string]any)["content"])

	tools := seen["tools"].([]any)
	require.Len(t, tools, 1)
	assert.Equal(t, "route_question", tools[0].(map[string]any)["function"].(map[string]any)["name"])

	choice := seen["tool_choice"].(map[string]any)
	assert.Equal(t, "route_question", choice["function"].(map[string]any)["name"])
}

func TestOpenAIChatMapsToolResultsToToolMessages(t *testing.T) {
	var seen map[string]any
	client := newOpenAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &seen))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message":       map[string]any{"content": "final answer"},
			}},
		})
	}))

	response, err := client.Chat(context.Background(), Request{
		Messages: []Message{
			{Role: RoleUser, Text: "question"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{
				ID: "call-9", Name: "lookup", Input: json.RawMessage(`{"nrn":"01-2345"}`),
			}}},
			{Role: RoleUser, ToolResults: []ToolResult{{
				ToolCallID: "call-9", Content: `{"found":true}`,
			}}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "final answer", response.Text)

	messages := seen["messages"].([]any)
	require.Len(t, messages, 3)

	assistant := messages[1].(map[string]any)
	assert.Equal(t, "assistant", assistant["role"])
	calls := assistant["tool_calls"].([]any)
	require.Len(t, calls, 1)
	function := calls[0].(map[string]any)["function"].(map[string]any)
	assert.Equal(t, "lookup", function["name"])
	assert.JSONEq(t, `{"nrn":"01-2345"}`, function["arguments"].(string))

	tool := messages[2].(map[string]any)
	assert.Equal(t, "tool", tool["role"])
	assert.Equal(t, "call-9", tool["tool_call_id"])
	assert.Equal(t, `{"found":true}`, tool["content"])
}

func TestOpenAIChatClassifiesHTTPFailuresWithoutLeakingBodies(t *testing.T) {
	cases := map[string]struct {
		status    int
		transient bool
	}{
		"rate limited": {status: http.StatusTooManyRequests, transient: true},
		"server error": {status: http.StatusInternalServerError, transient: true},
		"unauthorized": {status: http.StatusUnauthorized, transient: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client := newOpenAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"message":"echoed user question"}}`))
			}))

			_, err := client.Chat(context.Background(), Request{
				Messages: []Message{{Role: RoleUser, Text: "private question"}},
			})
			require.Error(t, err)
			assert.Equal(t, tc.transient, IsTransient(err))
			assert.NotContains(t, err.Error(), "echoed user question")
		})
	}
}

func TestOpenAIChatRejectsAnEmptyChoiceList(t *testing.T) {
	client := newOpenAIClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{}})
	}))

	_, err := client.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "q"}},
	})
	assert.Error(t, err)
}

func TestNewOpenAIRequiresKeyAndModel(t *testing.T) {
	_, err := NewOpenAI(OpenAIOptions{Model: "m"})
	assert.Error(t, err)

	_, err = NewOpenAI(OpenAIOptions{APIKey: "k"})
	assert.Error(t, err)
}

func TestNewOpenAIDefaultsToThePublicEndpoint(t *testing.T) {
	client, err := NewOpenAI(OpenAIOptions{APIKey: "k", Model: "m"})
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com/v1", client.baseURL)
}
