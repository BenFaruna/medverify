// Package vision defines the provider interface for reading text from pack
// photos, plus its implementations and fake. Phase 3 uses only the fake; phase
// 4 adds a real provider behind the same interface.
package vision

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrUnreadable means the provider could not read the image confidently. The
// scan becomes unreadable and the user is asked for a better photo; it is not
// a system failure and must not be guessed around.
var ErrUnreadable = errors.New("image is unreadable")

// Image is one prepared image handed to a provider. Data is the clean JPEG
// produced by the scan package, never the original upload.
type Image struct {
	Position int
	Data     []byte
}

// Request is one extraction request.
type Request struct {
	ScanID   string
	Language string
	Images   []Image
}

// Usage is the provider's reported resource use. Costs are estimates in USD.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

// CallRecord is one provider call for the cost and latency ledger. It never
// carries prompt text or image bytes.
type CallRecord struct {
	ScanID        string
	Provider      string
	Model         string
	PromptVersion string
	Kind          string
	Latency       time.Duration
	Usage         Usage
	Outcome       string
	ErrorCode     string
}

// Recorder persists a provider call. Implementations must not store prompts or
// responses.
type Recorder interface {
	Record(ctx context.Context, call CallRecord) error
}

// Result is one provider extraction. Raw label values are untrusted until
// ValidateLabel runs. Confidence is the coarse gate from phase 03; per-field
// confidence lives on the label.
type Result struct {
	Provider      string
	Model         string
	PromptVersion string
	Label         *RawLabel
	Confidence    float64
	Usage         Usage
}

// Provider reads label text from pack photos.
type Provider interface {
	Extract(ctx context.Context, request Request) (Result, error)
}

// Fake is a scriptable in-memory provider for tests and for phase 3's local
// pipeline. It never calls a network.
type Fake struct {
	mu sync.Mutex

	// Results are returned in order. The last one repeats after the list ends.
	Results []Result
	// Errors are returned in order. A non-nil error takes precedence over the
	// result at the same call index.
	Errors []error
	// Calls counts invocations, for assertions.
	Calls int
	// Requests records every request, for assertions.
	Requests []Request
	// Delay slows each call, for tests that need to observe an in-flight scan.
	Delay time.Duration
}

// NewFake returns a fake that completes a scan with a small, deterministic
// label. Tests override Results when they need other values.
func NewFake() *Fake {
	label := DefaultFakeLabel()
	return &Fake{Results: []Result{{
		Provider:      "fake",
		Model:         "fake-v1",
		PromptVersion: "fake",
		Confidence:    1,
		Label:         &label,
	}}}
}

// DefaultFakeLabel is a complete, plausible label used by the fake provider and
// by pipeline tests.
func DefaultFakeLabel() RawLabel {
	return RawLabel{
		NRN:          RawField{Value: ptrString("01-2345"), Confidence: 0.98, Source: 1},
		ProductName:  RawField{Value: ptrString("Paracetamol Tablets"), Confidence: 0.97, Source: 1},
		Manufacturer: RawField{Value: ptrString("Example Pharma Ltd"), Confidence: 0.94, Source: 1},
		BatchNo:      RawField{Value: ptrString("BATCH-001"), Confidence: 0.93, Source: 1},
		MfgDate:      RawField{Value: ptrString("Jan 2026"), Confidence: 0.92, Source: 1},
		ExpiryDate:   RawField{Value: ptrString("Jan 2028"), Confidence: 0.92, Source: 1},
		Ingredients: []RawIngredient{
			{Name: "Paracetamol", Strength: "500 mg", Confidence: 0.96, Source: 1},
		},
	}
}

func ptrString(value string) *string { return &value }

// Extract implements Provider.
func (f *Fake) Extract(ctx context.Context, request Request) (Result, error) {
	f.mu.Lock()
	delay := f.Delay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	index := f.Calls
	f.Calls++
	f.Requests = append(f.Requests, Request{
		ScanID:   request.ScanID,
		Language: request.Language,
		Images:   append([]Image(nil), request.Images...),
	})

	if index < len(f.Errors) && f.Errors[index] != nil {
		return Result{}, f.Errors[index]
	}
	if len(f.Results) == 0 {
		label := DefaultFakeLabel()
		return Result{Provider: "fake", Model: "fake-v1", PromptVersion: "fake", Confidence: 1, Label: &label}, nil
	}
	if index < len(f.Results) {
		return f.Results[index], nil
	}
	return f.Results[len(f.Results)-1], nil
}

// CallsCount returns the number of invocations. Useful when the fake is shared
// across goroutines.
func (f *Fake) CallsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Calls
}
