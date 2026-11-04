package scan

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/benfaruna/medverify/internal/verdict"
)

// SandboxReason is the fixed reason a sandbox scan reports. It is a reason that
// asks for more information rather than a reassurance, so a fixed sandbox
// payload can never read as "this medicine is fine".
const SandboxReason = verdict.ReasonInsufficientLabelData

// sandboxReasons is the fixed reason set for a sandbox result, stored as JSON.
var sandboxReasons = json.RawMessage(`[{"code":"` + SandboxReason + `"}]`)

// SandboxConfidence is the fixed confidence band. It is deliberately low.
const SandboxConfidence = verdict.ConfidenceLow

// completeSandbox finishes a sandbox scan without calling any provider. It is
// the whole cost guarantee of sandbox mode: the claim is taken, the fixed
// result is stored, and no image ever reaches a vision model.
func (s *Service) completeSandbox(ctx context.Context, scanned Scan) error {
	claimed, err := s.store.ClaimScan(ctx, scanned.ID)
	if err != nil {
		return err
	}
	if !claimed {
		// Another worker finished it; the state machine already refuses a
		// second terminal write, so there is nothing to do.
		return nil
	}

	// No image was read, so the signals say exactly that. They are also what
	// makes the stored verdict auditable, since the store refuses a verdict
	// result that carries no signals.
	signals, err := json.Marshal(verdict.SignalsFromUnreadable(nil))
	if err != nil {
		return err
	}

	result := Result{
		Verdict: &verdict.Result{
			Verdict:    verdict.NeedsReview,
			Confidence: SandboxConfidence,
			// The score is internal and never serialized to a client.
			Score:        0,
			Reasons:      []verdict.Reason{{Code: SandboxReason}},
			RulesVersion: "sandbox",
		},
		Signals: signals,
		Reasons: sandboxReasons,
		// An empty extraction and no features is the honest shape for a
		// result that was not derived from the images.
		Extracted: json.RawMessage(`{}`),
		Features:  json.RawMessage(`{}`),
		Provider:  "sandbox",
	}

	s.finish(ctx, scanned.ID, StatusCompleted, result)
	s.logger.InfoContext(ctx, "sandbox scan completed without a provider call",
		slog.String("scan_id", scanned.ID.String()),
	)
	return nil
}
