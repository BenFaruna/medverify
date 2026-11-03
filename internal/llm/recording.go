package llm

import (
	"context"
	"time"
)

// RecordingClient wraps a Client and writes one ledger row per call. It never
// records prompts, questions, sources, or answers.
type RecordingClient struct {
	Inner         Client
	Recorder      ChatRecorder
	ScanID        string
	Provider      string
	Model         string
	PromptVersion string
}

type scanIDKey struct{}

// WithScanID attaches the scan to a request context so one recording client can
// serve every conversation without being rebuilt per request.
func WithScanID(ctx context.Context, scanID string) context.Context {
	return context.WithValue(ctx, scanIDKey{}, scanID)
}

// ScanIDFromContext returns the scan attached to a context, if any.
func ScanIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(scanIDKey{}).(string)
	return value
}

// NewRecordingClient wraps inner.
func NewRecordingClient(inner Client, recorder ChatRecorder, scanID, provider, model string) *RecordingClient {
	return &RecordingClient{
		Inner: inner, Recorder: recorder, ScanID: scanID,
		Provider: provider, Model: model, PromptVersion: "unknown",
	}
}

// Chat implements Client.
func (r *RecordingClient) Chat(ctx context.Context, request Request) (Response, error) {
	started := time.Now()
	response, err := r.Inner.Chat(ctx, request)
	outcome := "success"
	errorCode := ""
	if err != nil {
		outcome = "error"
		errorCode = "provider_error"
		if IsTransient(err) {
			errorCode = "provider_unavailable"
		}
	}
	if r.Recorder != nil {
		provider := response.Provider
		if provider == "" {
			provider = r.Provider
		}
		model := response.Model
		if model == "" {
			model = r.Model
		}
		version := request.PromptVersion
		if version == "" {
			version = r.PromptVersion
		}
		scanID := r.ScanID
		if fromContext := ScanIDFromContext(ctx); fromContext != "" {
			scanID = fromContext
		}
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = r.Recorder.RecordChat(recordCtx, CallRecord{
			ScanID:        scanID,
			Provider:      provider,
			Model:         model,
			PromptVersion: version,
			Latency:       time.Since(started),
			Usage:         response.Usage,
			Outcome:       outcome,
			ErrorCode:     errorCode,
		})
	}
	return response, err
}

var _ Client = (*RecordingClient)(nil)
