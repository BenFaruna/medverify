package vision

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PromptVersion is stored with every extraction so a result can be traced to
// the exact instructions that produced it.
const PromptVersion = "v1"

//go:embed prompts/v1/system.md prompts/v1/schema.json
var promptFiles embed.FS

// Prompt is one versioned instruction set and its tool schema.
type Prompt struct {
	Version string
	System  string
	Schema  json.RawMessage
}

// LoadPrompt returns the embedded prompt for a version. Only "v1" exists now;
// an unknown version is an error rather than a silent fallback.
func LoadPrompt(version string) (Prompt, error) {
	if version == "" {
		version = PromptVersion
	}
	if version != PromptVersion {
		return Prompt{}, fmt.Errorf("vision: unknown prompt version %q", version)
	}
	system, err := promptFiles.ReadFile("prompts/" + version + "/system.md")
	if err != nil {
		return Prompt{}, fmt.Errorf("vision: read prompt: %w", err)
	}
	schema, err := promptFiles.ReadFile("prompts/" + version + "/schema.json")
	if err != nil {
		return Prompt{}, fmt.Errorf("vision: read prompt schema: %w", err)
	}
	if !json.Valid(schema) {
		return Prompt{}, fmt.Errorf("vision: prompt schema is not valid JSON")
	}
	return Prompt{Version: version, System: string(system), Schema: schema}, nil
}

// ClaudeOptions configures the Anthropic Messages API provider.
type ClaudeOptions struct {
	APIKey      string
	BaseURL     string
	Model       string
	MaxTokens   int
	HTTPClient  *http.Client
	Prompt      Prompt
	InputPrice  float64 // USD per million input tokens
	OutputPrice float64 // USD per million output tokens
}

// Claude calls the Anthropic Messages API with a forced tool call so the model
// can only answer with the label schema. It never logs image bytes or text.
type Claude struct {
	apiKey      string
	baseURL     string
	model       string
	maxTokens   int
	client      *http.Client
	prompt      Prompt
	inputPrice  float64
	outputPrice float64
}

// NewClaude builds the provider. APIKey, Model, and a prompt are required.
func NewClaude(opts ClaudeOptions) (*Claude, error) {
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, fmt.Errorf("vision: claude requires an API key")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, fmt.Errorf("vision: claude requires a pinned model identifier")
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
	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.anthropic.com"
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 90 * time.Second}
	}
	return &Claude{
		apiKey:      opts.APIKey,
		baseURL:     strings.TrimRight(opts.BaseURL, "/"),
		model:       opts.Model,
		maxTokens:   opts.MaxTokens,
		client:      opts.HTTPClient,
		prompt:      prompt,
		inputPrice:  opts.InputPrice,
		outputPrice: opts.OutputPrice,
	}, nil
}

type claudeRequest struct {
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature float64         `json:"temperature"`
	System      string          `json:"system"`
	Messages    []claudeMessage `json:"messages"`
	Tools       []claudeTool    `json:"tools"`
	ToolChoice  claudeChoice    `json:"tool_choice"`
}

type claudeMessage struct {
	Role    string        `json:"role"`
	Content []claudeBlock `json:"content"`
}

type claudeBlock struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *claudeImage `json:"source,omitempty"`
}

type claudeImage struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type claudeTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type claudeChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type claudeResponse struct {
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

const labelToolName = "record_label"

// Extract implements Provider.
func (c *Claude) Extract(ctx context.Context, request Request) (Result, error) {
	if len(request.Images) == 0 {
		return Result{}, fmt.Errorf("vision: claude requires at least one image")
	}

	blocks := make([]claudeBlock, 0, len(request.Images)+1)
	for _, image := range request.Images {
		blocks = append(blocks, claudeBlock{
			Type: "image",
			Source: &claudeImage{
				Type:      "base64",
				MediaType: "image/jpeg",
				Data:      base64.StdEncoding.EncodeToString(image.Data),
			},
		})
	}
	blocks = append(blocks, claudeBlock{
		Type: "text",
		Text: userPrompt(request.Language),
	})

	payload := claudeRequest{
		Model:       c.model,
		MaxTokens:   c.maxTokens,
		Temperature: 0,
		System:      c.prompt.System,
		Messages:    []claudeMessage{{Role: "user", Content: blocks}},
		Tools: []claudeTool{{
			Name:        labelToolName,
			Description: "Record the medicine pack label exactly as printed.",
			InputSchema: c.prompt.Schema,
		}},
		ToolChoice: claudeChoice{Type: "tool", Name: labelToolName},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, fmt.Errorf("vision: encode claude request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("vision: build claude request: %w", err)
	}
	httpRequest.Header.Set("content-type", "application/json")
	httpRequest.Header.Set("anthropic-version", "2023-06-01")
	httpRequest.Header.Set("x-api-key", c.apiKey)

	response, err := c.client.Do(httpRequest)
	if err != nil {
		return Result{}, transientf("vision: claude request failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return Result{}, transientf("vision: read claude response: %v", err)
	}

	if response.StatusCode != http.StatusOK {
		// Never include the body: it can echo request content.
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			return Result{}, transientf("vision: claude returned status %d", response.StatusCode)
		}
		return Result{}, fmt.Errorf("vision: claude returned status %d", response.StatusCode)
	}

	label, usage, err := parseClaudeResponse(responseBody)
	if err != nil {
		return Result{}, err
	}
	usage.CostUSD = c.cost(usage)

	return Result{
		Provider:      "claude",
		Model:         c.model,
		PromptVersion: c.prompt.Version,
		Label:         &label,
		Confidence:    ValidateLabel(label).OverallConfidence(),
		Usage:         usage,
	}, nil
}

func userPrompt(language string) string {
	if strings.TrimSpace(language) == "" {
		return "Read this pack and record the label. Use source 1."
	}
	return "Read this pack and record the label. The user requested language " +
		language + "; copy printed text as-is. Use source 1."
}

func (c *Claude) cost(usage Usage) float64 {
	return float64(usage.InputTokens)/1_000_000*c.inputPrice +
		float64(usage.OutputTokens)/1_000_000*c.outputPrice
}

func parseClaudeResponse(body []byte) (RawLabel, Usage, error) {
	var response claudeResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return RawLabel{}, Usage{}, fmt.Errorf("vision: decode claude response: %w", err)
	}
	usage := Usage{
		InputTokens:  response.Usage.InputTokens,
		OutputTokens: response.Usage.OutputTokens,
	}
	if response.StopReason == "refusal" {
		return RawLabel{}, usage, ErrUnreadable
	}
	for _, block := range response.Content {
		if block.Type != "tool_use" || len(block.Input) == 0 {
			continue
		}
		var label RawLabel
		if err := json.Unmarshal(block.Input, &label); err != nil {
			return RawLabel{}, usage, fmt.Errorf("vision: decode label tool input: %w", err)
		}
		return label, usage, nil
	}
	// A refusal, a prose answer, or an empty content list means the model did
	// not produce readable label data. That is unreadable, not a system error.
	return RawLabel{}, usage, ErrUnreadable
}
