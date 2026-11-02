// Package vision defines the provider interface for reading text from pack
// photos, plus its implementations and fake. Phase 3 uses only the fake; phase
// 4 adds a real provider behind the same interface.
package vision

import (
	"context"
	"encoding/json"
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

// Result is a placeholder extraction result. Phase 4 replaces Fields with the
// typed label model; phase 3 only needs success or failure and confidence.
type Result struct {
	Provider   string
	Fields     json.RawMessage
	Confidence float64
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

// NewFake returns a fake that completes a scan with a placeholder result.
func NewFake() *Fake {
	return &Fake{Results: []Result{{Provider: "fake", Confidence: 1}}}
}

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
		return Result{Provider: "fake", Confidence: 1}, nil
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
