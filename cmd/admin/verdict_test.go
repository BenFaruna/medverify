package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/benfaruna/medverify/internal/verdict"
)

func TestVerdictDifferenceNamesEveryChange(t *testing.T) {
	base := verdict.Result{
		Verdict:    verdict.ConsistentWithRecords,
		Score:      12,
		Confidence: verdict.ConfidenceHigh,
		Reasons:    []verdict.Reason{{Code: "fields_agree"}},
	}

	assert.Empty(t, verdictDifference(base, base), "an unchanged replay is not a difference")

	changed := base
	changed.Verdict = verdict.NeedsReview
	changed.Score = 30
	changed.Confidence = verdict.ConfidenceMedium
	changed.Reasons = []verdict.Reason{{Code: "thin_match"}}
	assert.Equal(t, "verdict,score,confidence,reasons", verdictDifference(base, changed))

	// Same code but different params is still a wording change worth showing.
	params := base
	params.Reasons = []verdict.Reason{{Code: "fields_agree", Params: map[string]string{"field": "nrn"}}}
	assert.Equal(t, "reasons", verdictDifference(base, params))
}

func TestSummarizeVerdictIsStable(t *testing.T) {
	assert.Equal(t, "needs_review/low/40", summarizeVerdict(verdict.Result{
		Verdict:    verdict.NeedsReview,
		Score:      40,
		Confidence: verdict.ConfidenceLow,
	}))
}
