package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ClaudeOptions configures the Anthropic Messages API client.
type ClaudeOptions struct {
	APIKey      string
	BaseURL     string
	Model       string
	MaxTokens   int
	Timeout     time.Duration
	HTTPClient  *http.Client
	InputPrice  float64 // USD per million input tokens
	OutputPrice float64 // USD per million output tokens
}

// Claude calls the Messages API. It never logs request or response bodies.
type Claude struct {
	apiKey      string
	baseURL     string
	model       string
	maxTokens   int
	client      *http.Client
	inputPrice  float64
	outputPrice float64
}

// NewClaude builds the client. The model must be pinned by the operator; a
// floating alias is not rejected here because the provider decides aliases, but
// configuration validation requires an explicit value.
func NewClaude(opts ClaudeOptions) (*Claude, error) {
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, fmt.Errorf("llm: claude requires an API key")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, fmt.Errorf("llm: claude requires a pinned model identifier")
	}
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 1024
	}
	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.anthropic.com"
	}
	if opts.HTTPClient == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = 45 * time.Second
		}
		opts.HTTPClient = &http.Client{Timeout: timeout}
	}
	return &Claude{
		apiKey:      opts.APIKey,
		baseURL:     strings.TrimRight(opts.BaseURL, "/"),
		model:       opts.Model,
		maxTokens:   opts.MaxTokens,
		client:      opts.HTTPClient,
		inputPrice:  opts.InputPrice,
		outputPrice: opts.OutputPrice,
	}, nil
}

type claudeRequest struct {
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature float64         `json:"temperature"`
	System      []claudeSystem  `json:"system,omitempty"`
	Messages    []claudeMessage `json:"messages"`
	Tools       []claudeTool    `json:"tools,omitempty"`
	ToolChoice  *claudeChoice   `json:"tool_choice,omitempty"`
}

type claudeSystem struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

type claudeMessage struct {
	Role    string        `json:"role"`
	Content []claudeBlock `json:"content"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type claudeTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type claudeChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type claudeResponse struct {
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Chat implements Client.
func (c *Claude) Chat(ctx context.Context, request Request) (Response, error) {
	if err := ValidateRequest(request); err != nil {
		return Response{}, err
	}

	payload := claudeRequest{
		Model:       c.model,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		Messages:    make([]claudeMessage, 0, len(request.Messages)),
	}
	if payload.MaxTokens <= 0 {
		payload.MaxTokens = c.maxTokens
	}
	if strings.TrimSpace(request.System) != "" {
		// The system prompt is fixed and versioned, so it is the right thing
		// to cache at the provider.
		payload.System = []claudeSystem{{
			Type:         "text",
			Text:         request.System,
			CacheControl: &cacheControl{Type: "ephemeral"},
		}}
	}
	for _, message := range request.Messages {
		converted, err := claudeMessageFrom(message)
		if err != nil {
			return Response{}, err
		}
		payload.Messages = append(payload.Messages, converted)
	}
	for _, tool := range request.Tools {
		payload.Tools = append(payload.Tools, claudeTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.Schema,
		})
	}
	if request.ForceTool != "" {
		payload.ToolChoice = &claudeChoice{Type: "tool", Name: request.ForceTool}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, fmt.Errorf("llm: encode claude request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("llm: build claude request: %w", err)
	}
	httpRequest.Header.Set("content-type", "application/json")
	httpRequest.Header.Set("anthropic-version", "2023-06-01")
	httpRequest.Header.Set("x-api-key", c.apiKey)

	response, err := c.client.Do(httpRequest)
	if err != nil {
		return Response{}, &TransientError{Err: fmt.Errorf("llm: claude request failed: %w", err)}
	}
	defer func() { _ = response.Body.Close() }()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return Response{}, &TransientError{Err: fmt.Errorf("llm: read claude response: %w", err)}
	}
	if response.StatusCode != http.StatusOK {
		// Never include the body: it can echo prompt or question text.
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			return Response{}, &TransientError{Err: fmt.Errorf("llm: claude returned status %d", response.StatusCode)}
		}
		return Response{}, fmt.Errorf("llm: claude returned status %d", response.StatusCode)
	}

	var decoded claudeResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return Response{}, fmt.Errorf("llm: decode claude response: %w", err)
	}
	result := Response{
		Provider:   "claude",
		Model:      c.model,
		StopReason: decoded.StopReason,
		Usage: Usage{
			InputTokens:  decoded.Usage.InputTokens,
			OutputTokens: decoded.Usage.OutputTokens,
		},
	}
	for _, block := range decoded.Content {
		switch block.Type {
		case "text":
			result.Text += block.Text
		case "tool_use":
			if len(block.Input) == 0 {
				block.Input = json.RawMessage("{}")
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:    block.ID,
				Name:  block.Name,
				Input: block.Input,
			})
		}
	}
	result.Usage.CostUSD = c.cost(result.Usage)
	return result, nil
}

func claudeMessageFrom(message Message) (claudeMessage, error) {
	switch message.Role {
	case RoleUser:
		blocks := make([]claudeBlock, 0, len(message.ToolResults)+1)
		for _, result := range message.ToolResults {
			if strings.TrimSpace(result.ToolCallID) == "" {
				return claudeMessage{}, fmt.Errorf("llm: tool result is missing its call id")
			}
			blocks = append(blocks, claudeBlock{
				Type:      "tool_result",
				ToolUseID: result.ToolCallID,
				Content:   result.Content,
				IsError:   result.IsError,
			})
		}
		if strings.TrimSpace(message.Text) != "" {
			blocks = append(blocks, claudeBlock{Type: "text", Text: message.Text})
		}
		if len(blocks) == 0 {
			return claudeMessage{}, fmt.Errorf("llm: user message has no content")
		}
		return claudeMessage{Role: "user", Content: blocks}, nil
	case RoleAssistant:
		blocks := make([]claudeBlock, 0, len(message.ToolCalls)+1)
		if strings.TrimSpace(message.Text) != "" {
			blocks = append(blocks, claudeBlock{Type: "text", Text: message.Text})
		}
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" {
				return claudeMessage{}, fmt.Errorf("llm: assistant tool call is missing an id or name")
			}
			input := call.Input
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			blocks = append(blocks, claudeBlock{
				Type: "tool_use", ID: call.ID, Name: call.Name, Input: input,
			})
		}
		if len(blocks) == 0 {
			return claudeMessage{}, fmt.Errorf("llm: assistant message has no content")
		}
		return claudeMessage{Role: "assistant", Content: blocks}, nil
	default:
		return claudeMessage{}, fmt.Errorf("llm: unsupported role %q", message.Role)
	}
}

func (c *Claude) cost(usage Usage) float64 {
	return float64(usage.InputTokens)/1_000_000*c.inputPrice +
		float64(usage.OutputTokens)/1_000_000*c.outputPrice
}

var _ Client = (*Claude)(nil)
