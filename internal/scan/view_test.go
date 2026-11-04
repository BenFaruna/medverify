package scan_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/scan"
	"github.com/benfaruna/medverify/internal/verdict"
)

func catalog(t *testing.T) *verdict.Catalog {
	t.Helper()
	loaded, err := verdict.LoadCatalog("")
	require.NoError(t, err)
	return &loaded
}

// The public result must never leak a numeric score or a claim that a medicine
// is safe, genuine, or authentic. This is the phase 11 output rule.
func TestViewNeverExposesAScoreOrAReassurance(t *testing.T) {
	t.Parallel()

	completed := time.Now()
	scanned := scan.Scan{
		ID:        uuid.New(),
		Status:    scan.StatusCompleted,
		CreatedAt: completed,
		Result: &scan.Result{
			Verdict: &verdict.Result{
				Verdict:    verdict.ConsistentWithRecords,
				Score:      92,
				Confidence: verdict.ConfidenceHigh,
				Reasons: []verdict.Reason{{
					Code:   verdict.ReasonLabelConsistent,
					Params: map[string]string{"nrn": "01-2345", "product": "Paracetamol"},
				}},
				RulesVersion: "verdict-v1",
			},
		},
	}

	encoded, err := json.Marshal(scanned.View(catalog(t)))
	require.NoError(t, err)
	body := string(encoded)

	assert.NotContains(t, body, `"score"`, "the public result must not carry a numeric score")
	assert.NotContains(t, strings.ToLower(body), "safe")
	assert.NotContains(t, strings.ToLower(body), "genuine")
	assert.NotContains(t, strings.ToLower(body), "authentic")

	// The reassurance word may still appear inside a reviewed reason sentence
	// only if that sentence is a disclaimer; assert the verdict field itself is
	// restricted to the documented enum values.
	var view scan.View
	require.NoError(t, json.Unmarshal(encoded, &view))
	require.NotNil(t, view.Verdict)
	assert.Equal(t, "consistent_with_records", view.Verdict.Value)
	assert.Equal(t, "high", view.Verdict.Confidence)
	assert.Equal(t, "verdict-v1", view.Verdict.RulesVersion)
}

func TestViewRendersReasonTextFromTheCatalog(t *testing.T) {
	t.Parallel()

	scanned := scan.Scan{
		ID:     uuid.New(),
		Status: scan.StatusCompleted,
		Result: &scan.Result{
			Verdict: &verdict.Result{
				Verdict:    verdict.NotFoundInRecords,
				Confidence: verdict.ConfidenceMedium,
				Reasons: []verdict.Reason{{
					Code: verdict.ReasonNotInDataset,
				}},
			},
		},
	}

	view := scanned.View(catalog(t))
	require.Len(t, view.Reasons, 1)
	assert.Equal(t, verdict.ReasonNotInDataset, view.Reasons[0].Code)
	assert.NotEmpty(t, view.Reasons[0].Text, "a public reason must carry displayable text")
	assert.NotContains(t, strings.ToLower(view.Reasons[0].Text), "fake",
		"not found in the dataset is not the same as fake")
}

func TestViewAlwaysCarriesTheDisclaimer(t *testing.T) {
	t.Parallel()

	for _, status := range []scan.Status{scan.StatusQueued, scan.StatusCompleted, scan.StatusUnreadable} {
		view := scan.Scan{ID: uuid.New(), Status: status}.View(nil)
		assert.Equal(t, scan.Disclaimer, view.Disclaimer, "status %s", status)
		assert.Equal(t, []verdict.Rendered{}, view.Reasons)
		assert.JSONEq(t, "[]", string(view.Flags))
	}
}

func TestViewDropsReasonsItCannotRender(t *testing.T) {
	t.Parallel()

	scanned := scan.Scan{
		ID:     uuid.New(),
		Status: scan.StatusCompleted,
		Result: &scan.Result{
			Verdict: &verdict.Result{
				Verdict: verdict.NeedsReview,
				Reasons: []verdict.Reason{{
					// unknown_fields needs a {fields} parameter.
					Code:   verdict.ReasonUnknownFields,
					Params: map[string]string{},
				}},
			},
		},
	}

	view := scanned.View(catalog(t))
	assert.Empty(t, view.Reasons, "a sentence with a hole in it must be dropped, not shown")
}

func TestViewWithoutACatalogShowsNoSentences(t *testing.T) {
	t.Parallel()

	scanned := scan.Scan{
		ID:     uuid.New(),
		Status: scan.StatusCompleted,
		Result: &scan.Result{
			Verdict: &verdict.Result{
				Verdict: verdict.ConsistentWithRecords,
				Reasons: []verdict.Reason{{Code: verdict.ReasonLabelConsistent}},
			},
		},
	}

	view := scanned.View(nil)
	assert.Empty(t, view.Reasons, "without reviewed wording there is nothing safe to display")
	require.NotNil(t, view.Verdict)
}
