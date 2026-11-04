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

// OpenAIProvider is the ledger name for the OpenAI-compatible wire format. It
// covers OpenAI itself and any server that speaks the same chat/completions
// API, including Gemini's compatibility endpoint, Azure OpenAI, OpenRouter,
// Ollama, and vLLM.
const OpenAIProvider = "openai"

// OpenAIOptions configures an OpenAI-compatible chat completions client.
//
// BaseURL names the API root, not the host: it must include the version path,
// for example "https://api.openai.com/v1". An empty value selects OpenAI.
type OpenAIOptions struct {
	APIKey      string
	BaseURL     string
	Model       string
	MaxTokens   int
	Timeout     time.Duration
	HTTPClient  *http.Client
	InputPrice  float64 // USD per million input tokens
	OutputPrice float64 // USD per million output tokens
}

// OpenAI calls an OpenAI-compatible chat completions endpoint. It never logs
// request or response bodies.
type OpenAI struct {
	apiKey      string
	baseURL     string
	model       string
	maxTokens   int
	client      *http.Client
	inputPrice  float64
	outputPrice float64
}

// NewOpenAI builds the client. The model must be pinned by the operator.
func NewOpenAI(opts OpenAIOptions) (*OpenAI, error) {
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, fmt.Errorf("llm: openai requires an API key")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, fmt.Errorf("llm: openai requires a pinned model identifier")
	}
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 1024
	}
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if opts.HTTPClient == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = 45 * time.Second
		}
		opts.HTTPClient = &http.Client{Timeout: timeout}
	}
	return &OpenAI{
		apiKey:      opts.APIKey,
		baseURL:     baseURL,
		model:       opts.Model,
		maxTokens:   opts.MaxTokens,
		client:      opts.HTTPClient,
		inputPrice:  opts.InputPrice,
		outputPrice: opts.OutputPrice,
	}, nil
}

type openAIChatMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIToolChoice struct {
	Type     string `json:"type"`
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

type openAIChatRequest struct {
	Model       string              `json:"model"`
	MaxTokens   int                 `json:"max_tokens"`
	Temperature float64             `json:"temperature"`
	Messages    []openAIChatMessage `json:"messages"`
	Tools       []openAITool        `json:"tools,omitempty"`
	ToolChoice  *openAIToolChoice   `json:"tool_choice,omitempty"`
}

type openAIChatResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Chat implements Client.
func (o *OpenAI) Chat(ctx context.Context, request Request) (Response, error) {
	if err := ValidateRequest(request); err != nil {
		return Response{}, err
	}

	payload := openAIChatRequest{
		Model:       o.model,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		Messages:    make([]openAIChatMessage, 0, len(request.Messages)+1),
	}
	if payload.MaxTokens <= 0 {
		payload.MaxTokens = o.maxTokens
	}
	if strings.TrimSpace(request.System) != "" {
		payload.Messages = append(payload.Messages, openAIChatMessage{
			Role: "system", Content: request.System,
		})
	}
	for _, message := range request.Messages {
		converted, err := openAIMessagesFrom(message)
		if err != nil {
			return Response{}, err
		}
		payload.Messages = append(payload.Messages, converted...)
	}
	for _, tool := range request.Tools {
		payload.Tools = append(payload.Tools, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Schema,
			},
		})
	}
	if request.ForceTool != "" {
		choice := &openAIToolChoice{Type: "function"}
		choice.Function.Name = request.ForceTool
		payload.ToolChoice = choice
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, fmt.Errorf("llm: encode openai request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("llm: build openai request: %w", err)
	}
	httpRequest.Header.Set("content-type", "application/json")
	httpRequest.Header.Set("authorization", "Bearer "+o.apiKey)

	response, err := o.client.Do(httpRequest)
	if err != nil {
		return Response{}, &TransientError{Err: fmt.Errorf("llm: openai request failed: %w", err)}
	}
	defer func() { _ = response.Body.Close() }()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return Response{}, &TransientError{Err: fmt.Errorf("llm: read openai response: %w", err)}
	}
	if response.StatusCode != http.StatusOK {
		// Never include the body: it can echo prompt or question text.
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			return Response{}, &TransientError{Err: fmt.Errorf("llm: openai returned status %d", response.StatusCode)}
		}
		return Response{}, fmt.Errorf("llm: openai returned status %d", response.StatusCode)
	}

	var decoded openAIChatResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return Response{}, fmt.Errorf("llm: decode openai response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return Response{}, fmt.Errorf("llm: openai response has no choices")
	}
	choice := decoded.Choices[0]

	result := Response{
		Provider:   OpenAIProvider,
		Model:      o.model,
		Text:       choice.Message.Content,
		StopReason: choice.FinishReason,
		Usage: Usage{
			InputTokens:  decoded.Usage.PromptTokens,
			OutputTokens: decoded.Usage.CompletionTokens,
		},
	}
	for _, call := range choice.Message.ToolCalls {
		input := json.RawMessage(strings.TrimSpace(call.Function.Arguments))
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID:    call.ID,
			Name:  call.Function.Name,
			Input: input,
		})
	}
	result.Usage.CostUSD = o.cost(result.Usage)
	return result, nil
}

func openAIMessagesFrom(message Message) ([]openAIChatMessage, error) {
	switch message.Role {
	case RoleUser:
		messages := make([]openAIChatMessage, 0, len(message.ToolResults)+1)
		for _, result := range message.ToolResults {
			if strings.TrimSpace(result.ToolCallID) == "" {
				return nil, fmt.Errorf("llm: tool result is missing its call id")
			}
			messages = append(messages, openAIChatMessage{
				Role:       "tool",
				ToolCallID: result.ToolCallID,
				Content:    result.Content,
			})
		}
		if strings.TrimSpace(message.Text) != "" {
			messages = append(messages, openAIChatMessage{Role: "user", Content: message.Text})
		}
		if len(messages) == 0 {
			return nil, fmt.Errorf("llm: user message has no content")
		}
		return messages, nil
	case RoleAssistant:
		converted := openAIChatMessage{Role: "assistant", Content: message.Text}
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" {
				return nil, fmt.Errorf("llm: assistant tool call is missing an id or name")
			}
			arguments := "{}"
			if len(call.Input) > 0 {
				arguments = string(call.Input)
			}
			converted.ToolCalls = append(converted.ToolCalls, openAIToolCall{
				ID:   call.ID,
				Type: "function",
				Function: openAIFunction{
					Name:      call.Name,
					Arguments: arguments,
				},
			})
		}
		if converted.Content == "" && len(converted.ToolCalls) == 0 {
			return nil, fmt.Errorf("llm: assistant message has no content")
		}
		return []openAIChatMessage{converted}, nil
	default:
		return nil, fmt.Errorf("llm: unsupported role %q", message.Role)
	}
}

func (o *OpenAI) cost(usage Usage) float64 {
	return float64(usage.InputTokens)/1_000_000*o.inputPrice +
		float64(usage.OutputTokens)/1_000_000*o.outputPrice
}

var _ Client = (*OpenAI)(nil)
