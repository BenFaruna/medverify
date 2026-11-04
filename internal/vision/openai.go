package vision

import (
	"bytes"
	"context"
	"encoding/base64"
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

// OpenAIOptions configures an OpenAI-compatible chat completions provider.
//
// BaseURL names the API root, not the host: it must include the version path,
// for example "https://api.openai.com/v1". An empty value selects OpenAI.
type OpenAIOptions struct {
	APIKey      string
	BaseURL     string
	Model       string
	MaxTokens   int
	HTTPClient  *http.Client
	Prompt      Prompt
	InputPrice  float64 // USD per million input tokens
	OutputPrice float64 // USD per million output tokens
}

// OpenAI calls an OpenAI-compatible chat completions endpoint with a forced
// function call, so the model can only answer with the label schema. It never
// logs image bytes or model text.
type OpenAI struct {
	apiKey      string
	baseURL     string
	model       string
	maxTokens   int
	client      *http.Client
	prompt      Prompt
	inputPrice  float64
	outputPrice float64
}

// NewOpenAI builds the provider. APIKey, Model, and a prompt are required.
func NewOpenAI(opts OpenAIOptions) (*OpenAI, error) {
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, fmt.Errorf("vision: openai requires an API key")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, fmt.Errorf("vision: openai requires a pinned model identifier")
	}
	prompt := opts.Prompt
	if prompt.Version == "" {
		loaded, err := LoadPrompt(PromptVersion)
		if err != nil {
			return nil, err
		}
		prompt = loaded
	}
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 4096
	}
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 90 * time.Second}
	}
	return &OpenAI{
		apiKey:      opts.APIKey,
		baseURL:     baseURL,
		model:       opts.Model,
		maxTokens:   opts.MaxTokens,
		client:      opts.HTTPClient,
		prompt:      prompt,
		inputPrice:  opts.InputPrice,
		outputPrice: opts.OutputPrice,
	}, nil
}

type openAIImage struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type openAIPart struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	ImageURL *openAIImage `json:"image_url,omitempty"`
}

type openAIMessage struct {
	Role    string       `json:"role"`
	Content []openAIPart `json:"content"`
}

type openAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIToolChoice struct {
	Type     string             `json:"type"`
	Function openAIFunctionName `json:"function"`
}

type openAIFunctionName struct {
	Name string `json:"name"`
}

type openAIRequest struct {
	Model       string           `json:"model"`
	MaxTokens   int              `json:"max_tokens"`
	Temperature float64          `json:"temperature"`
	Messages    []openAIMessage  `json:"messages"`
	Tools       []openAITool     `json:"tools"`
	ToolChoice  openAIToolChoice `json:"tool_choice"`
}

type openAIResponse struct {
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
	Error *struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Extract implements Provider.
func (o *OpenAI) Extract(ctx context.Context, request Request) (Result, error) {
	if len(request.Images) == 0 {
		return Result{}, fmt.Errorf("vision: openai requires at least one image")
	}

	parts := make([]openAIPart, 0, len(request.Images)+1)
	for _, image := range request.Images {
		parts = append(parts, openAIPart{
			Type: "image_url",
			ImageURL: &openAIImage{
				URL:    "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(image.Data),
				Detail: "high",
			},
		})
	}
	parts = append(parts, openAIPart{Type: "text", Text: userPrompt(request.Language)})

	payload := openAIRequest{
		Model:       o.model,
		MaxTokens:   o.maxTokens,
		Temperature: 0,
		Messages: []openAIMessage{
			{Role: "system", Content: []openAIPart{{Type: "text", Text: o.prompt.System}}},
			{Role: "user", Content: parts},
		},
		Tools: []openAITool{{
			Type: "function",
			Function: openAIFunction{
				Name:        labelToolName,
				Description: "Record the medicine pack label exactly as printed.",
				Parameters:  o.prompt.Schema,
			},
		}},
		ToolChoice: openAIToolChoice{
			Type:     "function",
			Function: openAIFunctionName{Name: labelToolName},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, fmt.Errorf("vision: encode openai request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("vision: build openai request: %w", err)
	}
	httpRequest.Header.Set("content-type", "application/json")
	httpRequest.Header.Set("authorization", "Bearer "+o.apiKey)

	response, err := o.client.Do(httpRequest)
	if err != nil {
		return Result{}, transientf("vision: openai request failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return Result{}, transientf("vision: read openai response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		// Never include the body: it can echo request content.
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			return Result{}, transientf("vision: openai returned status %d", response.StatusCode)
		}
		return Result{}, fmt.Errorf("vision: openai returned status %d", response.StatusCode)
	}

	label, usage, err := parseOpenAIResponse(responseBody)
	if err != nil {
		return Result{}, err
	}
	usage.CostUSD = o.cost(usage)

	return Result{
		Provider:      OpenAIProvider,
		Model:         o.model,
		PromptVersion: o.prompt.Version,
		Label:         &label,
		Confidence:    ValidateLabel(label).OverallConfidence(),
		Usage:         usage,
	}, nil
}

func (o *OpenAI) cost(usage Usage) float64 {
	return float64(usage.InputTokens)/1_000_000*o.inputPrice +
		float64(usage.OutputTokens)/1_000_000*o.outputPrice
}

func parseOpenAIResponse(body []byte) (RawLabel, Usage, error) {
	var response openAIResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return RawLabel{}, Usage{}, fmt.Errorf("vision: decode openai response: %w", err)
	}
	usage := Usage{
		InputTokens:  response.Usage.PromptTokens,
		OutputTokens: response.Usage.CompletionTokens,
	}
	if len(response.Choices) == 0 {
		return RawLabel{}, usage, ErrUnreadable
	}
	choice := response.Choices[0]
	if choice.FinishReason == "content_filter" {
		return RawLabel{}, usage, ErrUnreadable
	}
	for _, call := range choice.Message.ToolCalls {
		if call.Function.Name != labelToolName || strings.TrimSpace(call.Function.Arguments) == "" {
			continue
		}
		var label RawLabel
		if err := json.Unmarshal([]byte(call.Function.Arguments), &label); err != nil {
			return RawLabel{}, usage, fmt.Errorf("vision: decode label arguments: %w", err)
		}
		return label, usage, nil
	}
	// No label tool call means the model did not produce readable label data.
	// That is unreadable, not a system error.
	return RawLabel{}, usage, ErrUnreadable
}

var _ Provider = (*OpenAI)(nil)
