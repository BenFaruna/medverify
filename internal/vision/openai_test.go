package vision

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newOpenAITestProvider(t *testing.T, handler http.Handler) *OpenAI {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	provider, err := NewOpenAI(OpenAIOptions{
		APIKey:    "test-key",
		BaseURL:   server.URL + "/v1",
		Model:     "test-vision-model",
		MaxTokens: 512,
	})
	require.NoError(t, err)
	return provider
}

func TestOpenAIExtractSendsForcedFunctionAndImage(t *testing.T) {
	var seen map[string]any
	provider := newOpenAITestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
							"name":      labelToolName,
							"arguments": `{"nrn":{"value":"01-2345","confidence":0.99,"source":1}}`,
						},
					}},
				},
			}},
			"usage": map[string]any{"prompt_tokens": 1200, "completion_tokens": 80},
		})
	}))

	result, err := provider.Extract(context.Background(), Request{
		ScanID:   "scan-1",
		Language: "en",
		Images:   []Image{{Position: 1, Data: []byte("jpeg-bytes")}},
	})
	require.NoError(t, err)
	assert.Equal(t, OpenAIProvider, result.Provider)
	assert.Equal(t, "test-vision-model", result.Model)
	assert.Equal(t, 1200, result.Usage.InputTokens)
	assert.Equal(t, 80, result.Usage.OutputTokens)
	require.NotNil(t, result.Label)
	require.NotNil(t, result.Label.NRN.Value)
	assert.Equal(t, "01-2345", *result.Label.NRN.Value)

	assert.Equal(t, "test-vision-model", seen["model"])
	assert.InDelta(t, 0.0, seen["temperature"], 1e-9)

	tools, ok := seen["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	assert.Equal(t, labelToolName, tools[0].(map[string]any)["function"].(map[string]any)["name"])

	choice := seen["tool_choice"].(map[string]any)
	assert.Equal(t, "function", choice["type"])
	assert.Equal(t, labelToolName, choice["function"].(map[string]any)["name"])

	messages := seen["messages"].([]any)
	require.Len(t, messages, 2)
	assert.Equal(t, "system", messages[0].(map[string]any)["role"])

	parts := messages[1].(map[string]any)["content"].([]any)
	require.Len(t, parts, 2)
	image := parts[0].(map[string]any)
	assert.Equal(t, "image_url", image["type"])
	assert.True(t, strings.HasPrefix(
		image["image_url"].(map[string]any)["url"].(string), "data:image/jpeg;base64,"))

	// The scan id and raw image bytes must never reach the provider as text.
	assert.NotContains(t, string(mustJSON(t, seen)), "scan-1")
	assert.NotContains(t, string(mustJSON(t, seen)), "jpeg-bytes")
}

func TestOpenAIExtractTreatsMissingToolCallAsUnreadable(t *testing.T) {
	provider := newOpenAITestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message":       map[string]any{"content": "I cannot read this."},
			}},
		})
	}))

	_, err := provider.Extract(context.Background(), Request{Images: []Image{{Data: []byte("x")}}})
	assert.ErrorIs(t, err, ErrUnreadable, "prose is unreadable, never a guessed label")
}

func TestOpenAIExtractTreatsContentFilterAsUnreadable(t *testing.T) {
	provider := newOpenAITestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "content_filter",
				"message":       map[string]any{},
			}},
		})
	}))

	_, err := provider.Extract(context.Background(), Request{Images: []Image{{Data: []byte("x")}}})
	assert.ErrorIs(t, err, ErrUnreadable)
}

func TestOpenAIExtractClassifiesHTTPFailures(t *testing.T) {
	cases := map[string]struct {
		status    int
		transient bool
	}{
		"rate limited": {status: http.StatusTooManyRequests, transient: true},
		"server error": {status: http.StatusBadGateway, transient: true},
		"unauthorized": {status: http.StatusUnauthorized, transient: false},
		"bad request":  {status: http.StatusBadRequest, transient: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			provider := newOpenAITestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"message":"request contained secret prompt text"}}`))
			}))

			_, err := provider.Extract(context.Background(), Request{Images: []Image{{Data: []byte("x")}}})
			require.Error(t, err)
			assert.Equal(t, tc.transient, IsTransient(err))
			assert.NotContains(t, err.Error(), "secret prompt text",
				"provider bodies can echo private text and must not be surfaced")
		})
	}
}

func TestNewOpenAIRequiresKeyAndModel(t *testing.T) {
	_, err := NewOpenAI(OpenAIOptions{Model: "m"})
	assert.Error(t, err)

	_, err = NewOpenAI(OpenAIOptions{APIKey: "k"})
	assert.Error(t, err)
}

func TestNewOpenAIDefaultsToThePublicEndpoint(t *testing.T) {
	provider, err := NewOpenAI(OpenAIOptions{APIKey: "k", Model: "m"})
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com/v1", provider.baseURL)
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}
