// Package llm defines the language model interface used by the grounded
// assistant, its fake for deterministic tests, and a real provider client.
//
// The interface is deliberately small. Callers pass plain messages plus JSON
// tool schemas; the provider only chooses text or a tool call. Tools are
// executed by the assistant package, so model output can never reach a database
// or another product directly.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Role is one message author.
type Role string

// Supported roles. A "tool" result is sent back as a user message by the
// provider adapter; callers use ToolResult for clarity.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Tool describes one read-only function the model may call.
type Tool struct {
	Name        string
	Description string
	// Schema is a JSON Schema object. It is embedded in the provider request,
	// never filled with user or leaflet text.
	Schema json.RawMessage
}

// ToolCall is a model request to run one tool. ID is provider-assigned and is
// echoed back with the result.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult is the outcome of one tool call, returned to the model as data.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error,omitempty"`
}

// Message is one turn. Assistant messages may carry text and tool calls; user
// messages may carry text and tool results. Tool results are data, never
// instructions.
type Message struct {
	Role        Role
	Text        string
	ToolCalls   []ToolCall
	ToolResults []ToolResult
}

// Request is one model call. ForceTool makes the provider answer with a tool
// call, which is how the router and verifier get structured JSON.
type Request struct {
	System      string
	Messages    []Message
	Tools       []Tool
	ForceTool   string
	MaxTokens   int
	Temperature float64
	// PromptVersion is stored with the call ledger by the assistant. The
	// provider client does not interpret it.
	PromptVersion string
}

// Usage is provider-reported resource use in estimated USD.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

// Response is one model answer.
type Response struct {
	Provider   string
	Model      string
	Text       string
	ToolCalls  []ToolCall
	StopReason string
	Usage      Usage
}

// Client is the model seam. Implementations must not log prompt text, user
// questions, leaflet passages, or tool results.
type Client interface {
	Chat(ctx context.Context, request Request) (Response, error)
}

// Embedder is the embedding half of the phase 09 interface. Phase 08 owns the
// concrete implementations; keeping the method shape here lets the assistant
// depend on one interface without coupling to a provider.
type Embedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	Model() string
	Dim() int
}

// ErrTransient marks a provider failure that may succeed on retry: a timeout,
// a rate limit, or a 5xx. The HTTP layer must never turn it into a partial
// answer.
var ErrTransient = errors.New("llm provider error is retryable")

// TransientError wraps a retryable provider error.
type TransientError struct{ Err error }

func (e *TransientError) Error() string { return e.Err.Error() }
func (e *TransientError) Unwrap() error { return e.Err }
func (e *TransientError) Is(target error) bool {
	return target == ErrTransient || errors.Is(e.Err, target)
}

// IsTransient reports whether err may be retried.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrTransient) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var transient *TransientError
	return errors.As(err, &transient)
}

// ValidateTool checks a tool definition before it reaches a provider.
func ValidateTool(tool Tool) error {
	if strings.TrimSpace(tool.Name) == "" {
		return fmt.Errorf("llm: tool name is required")
	}
	if strings.TrimSpace(tool.Description) == "" {
		return fmt.Errorf("llm: tool %s needs a description", tool.Name)
	}
	if len(tool.Schema) == 0 || !json.Valid(tool.Schema) {
		return fmt.Errorf("llm: tool %s has an invalid JSON schema", tool.Name)
	}
	return nil
}

// CallRecord is one chat call for the cost ledger. It deliberately has no
// fields for prompts or answers.
type CallRecord struct {
	ScanID        string
	Provider      string
	Model         string
	PromptVersion string
	Latency       time.Duration
	Usage         Usage
	Outcome       string
	ErrorCode     string
}

// ChatRecorder persists a chat call. Implementations must redact and never
// store prompt or answer text. It is named separately from the existing cost
// Recorder in store.go.
type ChatRecorder interface {
	RecordChat(ctx context.Context, call CallRecord) error
}

// ValidateRequest rejects an unusable request early.
func ValidateRequest(request Request) error {
	if len(request.Messages) == 0 {
		return fmt.Errorf("llm: at least one message is required")
	}
	for _, tool := range request.Tools {
		if err := ValidateTool(tool); err != nil {
			return err
		}
	}
	if request.ForceTool != "" {
		found := false
		for _, tool := range request.Tools {
			if tool.Name == request.ForceTool {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("llm: forced tool %q is not in the request tool list", request.ForceTool)
		}
	}
	return nil
}
