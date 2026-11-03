// Package llm owns provider call records for cost and latency visibility.
// Prompt text, images, and responses are never stored here.
package llm

import (
	"context"
	"fmt"
	"math"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
	"github.com/benfaruna/medverify/internal/vision"
)

// Recorder writes the llm_calls ledger.
type Recorder struct {
	queries *dbgen.Queries
}

// NewRecorder builds a recorder around an existing pool.
func NewRecorder(pool *pgxpool.Pool) *Recorder {
	return &Recorder{queries: dbgen.New(pool)}
}

// Record implements vision.Recorder.
func (r *Recorder) Record(ctx context.Context, call vision.CallRecord) error {
	if call.Provider == "" {
		return fmt.Errorf("llm: call record requires a provider")
	}

	var scanID pgtype.UUID
	if call.ScanID != "" {
		id, err := uuid.Parse(call.ScanID)
		if err != nil {
			return fmt.Errorf("llm: invalid scan id in call record: %w", err)
		}
		scanID = pgtype.UUID{Bytes: id, Valid: true}
	}

	kind := call.Kind
	if kind == "" {
		kind = "vision"
	}
	model := call.Model
	if model == "" {
		model = "unknown"
	}
	promptVersion := call.PromptVersion
	if promptVersion == "" {
		promptVersion = "unknown"
	}

	latency := call.Latency.Milliseconds()
	if latency < 0 {
		latency = 0
	}
	if latency > math.MaxInt32 {
		latency = math.MaxInt32
	}

	var cost pgtype.Numeric
	if call.Usage.CostUSD > 0 {
		// Store micro-dollars to match numeric(12,6) without a float scan.
		cost = pgtype.Numeric{
			Int:   big.NewInt(int64(math.Round(call.Usage.CostUSD * 1_000_000))),
			Exp:   -6,
			Valid: true,
		}
	}

	_, err := r.queries.InsertLLMCall(ctx, dbgen.InsertLLMCallParams{
		ScanID:        scanID,
		Provider:      call.Provider,
		Model:         model,
		PromptVersion: promptVersion,
		Kind:          kind,
		LatencyMs:     int32(latency),
		InputUnits:    optionalInt(call.Usage.InputTokens),
		OutputUnits:   optionalInt(call.Usage.OutputTokens),
		CostUsd:       cost,
		Outcome:       call.Outcome,
		ErrorCode:     optionalString(call.ErrorCode),
	})
	if err != nil {
		return fmt.Errorf("llm: insert call record: %w", err)
	}
	return nil
}

func optionalInt(value int) *int32 {
	if value <= 0 {
		return nil
	}
	converted := int32(value)
	return &converted
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// RecordChat writes one chat call. Prompt and answer text are never part of
// the record.
func (r *Recorder) RecordChat(ctx context.Context, call CallRecord) error {
	if call.Provider == "" {
		return fmt.Errorf("llm: chat call record requires a provider")
	}

	var scanID pgtype.UUID
	if call.ScanID != "" {
		id, err := uuid.Parse(call.ScanID)
		if err != nil {
			return fmt.Errorf("llm: invalid scan id in chat record: %w", err)
		}
		scanID = pgtype.UUID{Bytes: id, Valid: true}
	}
	model := call.Model
	if model == "" {
		model = "unknown"
	}
	version := call.PromptVersion
	if version == "" {
		version = "unknown"
	}
	latency := call.Latency.Milliseconds()
	if latency < 0 {
		latency = 0
	}
	if latency > math.MaxInt32 {
		latency = math.MaxInt32
	}
	var cost pgtype.Numeric
	if call.Usage.CostUSD > 0 {
		cost = pgtype.Numeric{
			Int:   big.NewInt(int64(math.Round(call.Usage.CostUSD * 1_000_000))),
			Exp:   -6,
			Valid: true,
		}
	}
	outcome := call.Outcome
	if outcome != "success" && outcome != "error" {
		outcome = "error"
	}
	_, err := r.queries.InsertLLMCall(ctx, dbgen.InsertLLMCallParams{
		ScanID:        scanID,
		Provider:      call.Provider,
		Model:         model,
		PromptVersion: version,
		Kind:          "chat",
		LatencyMs:     int32(latency),
		InputUnits:    optionalInt(call.Usage.InputTokens),
		OutputUnits:   optionalInt(call.Usage.OutputTokens),
		CostUsd:       cost,
		Outcome:       outcome,
		ErrorCode:     optionalString(call.ErrorCode),
	})
	if err != nil {
		return fmt.Errorf("llm: insert chat record: %w", err)
	}
	return nil
}

// compile-time check that Recorder satisfies the provider seam.
var _ vision.Recorder = (*Recorder)(nil)
var _ ChatRecorder = (*Recorder)(nil)
