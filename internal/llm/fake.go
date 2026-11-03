package llm

import (
	"context"
	"sync"
	"time"
)

// FakeResponse is one scripted answer. Err takes precedence over Response.
type FakeResponse struct {
	Response Response
	Err      error
}

// Fake is a deterministic, scripted client for unit tests and local
// development without a provider key. It never calls a network. The last
// scripted response repeats, so tests only describe the interesting prefix.
type Fake struct {
	mu sync.Mutex

	Script   []FakeResponse
	Calls    []Request
	Delay    time.Duration
	Canceled bool
}

// NewFake builds a fake from a list of responses.
func NewFake(responses ...FakeResponse) *Fake {
	return &Fake{Script: responses}
}

// NewFakeText builds a fake that answers with fixed text.
func NewFakeText(text string) *Fake {
	return NewFake(FakeResponse{Response: Response{
		Provider: "fake", Model: "fake-chat-v1", Text: text,
	}})
}

// NewFakeTools builds a fake that answers with a list of tool calls.
func NewFakeTools(calls ...ToolCall) *Fake {
	return NewFake(FakeResponse{Response: Response{
		Provider: "fake", Model: "fake-chat-v1", ToolCalls: calls,
	}})
}

// Chat implements Client.
func (f *Fake) Chat(ctx context.Context, request Request) (Response, error) {
	f.mu.Lock()
	delay := f.Delay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, request)
	if len(f.Script) == 0 {
		return Response{Provider: "fake", Model: "fake-chat-v1"}, nil
	}
	index := len(f.Calls) - 1
	if index >= len(f.Script) {
		index = len(f.Script) - 1
	}
	entry := f.Script[index]
	if entry.Err != nil {
		return Response{}, entry.Err
	}
	response := entry.Response
	if response.Provider == "" {
		response.Provider = "fake"
	}
	if response.Model == "" {
		response.Model = "fake-chat-v1"
	}
	return response, nil
}

// CallsCount returns how many requests the fake has seen.
func (f *Fake) CallsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}

// Requests returns a copy of every request, for assertions.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Request, len(f.Calls))
	copy(out, f.Calls)
	return out
}

// compile-time check.
var _ Client = (*Fake)(nil)
