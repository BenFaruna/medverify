package verdict_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/verdict"
	"github.com/benfaruna/medverify/internal/verify"
)

func matchedSignals() verdict.Signals {
	return verdict.Signals{
		Outcome: verify.MatchedConsistent,
		Fields: map[string]verify.FieldOutcome{
			verify.FieldNRN:          verify.Agree,
			verify.FieldName:         verify.Agree,
			verify.FieldManufacturer: verify.Agree,
		},
		DatasetProducts: 12000,
		Extraction: verdict.Extraction{
			OverallConfidence: 0.93,
			Identified:        true,
		},
	}
}

// TestEveryHighSeverityFlagIsFlagged is the phase 07 hard rule: priority does
// not depend on which flag type fired.
func TestEveryHighSeverityFlagIsFlagged(t *testing.T) {
	rules, err := verdict.LoadRules("")
	require.NoError(t, err)

	for _, flagType := range safety.FlagTypes {
		t.Run(string(flagType), func(t *testing.T) {
			signals := matchedSignals()
			signals.Flags = []safety.Flag{{Type: flagType, Severity: safety.SeverityHigh, ReasonCode: "test"}}
			result := verdict.Evaluate(signals, rules)
			assert.Equal(t, verdict.Flagged, result.Verdict)
			assert.GreaterOrEqual(t, result.Score, rules.Bands[verdict.Flagged].Min)
		})
	}
}

// TestUnknownFieldsDoNotLowerConfidence proves partial data is treated fairly:
// unread optional fields add concern points and a reason, but they must not
// push an otherwise strong match below the confidence gate.
func TestUnknownFieldsDoNotLowerConfidence(t *testing.T) {
	rules, err := verdict.LoadRules("")
	require.NoError(t, err)

	signals := matchedSignals()
	signals.Fields["batch_no"] = verify.Unknown
	signals.Fields[verify.FieldExpiry] = verify.Unknown
	signals.Fields[verify.FieldStrength] = verify.Unknown
	signals.Extraction.MissingFields = []string{"batch_no", "expiry", "strength"}

	result := verdict.Evaluate(signals, rules)
	assert.Equal(t, verdict.ConsistentWithRecords, result.Verdict)
	assert.Equal(t, verdict.ConfidenceHigh, result.Confidence)

	for _, reason := range result.Reasons {
		assert.NotEqual(t, verdict.ReasonLowConfidence, reason.Code)
	}
}

func TestGateAlwaysWinsOverFlags(t *testing.T) {
	rules, err := verdict.LoadRules("")
	require.NoError(t, err)

	signals := matchedSignals()
	signals.Extraction.OverallConfidence = 0.2
	signals.Flags = []safety.Flag{{Type: safety.WatchlistProduct, Severity: safety.SeverityHigh, ReasonCode: "watchlist_alert"}}

	result := verdict.Evaluate(signals, rules)
	assert.Equal(t, verdict.Unreadable, result.Verdict)
	assert.Equal(t, rules.Bands[verdict.Unreadable].Min, result.Score)
	for _, reason := range result.Reasons {
		assert.NotEqual(t, verdict.ReasonSafetyFlag, reason.Code)
	}
}

// TestScoreOrderFollowsVerdictOrder keeps sorting honest: a result with more
// concern can never score below a lower-concern verdict.
func TestScoreOrderFollowsVerdictOrder(t *testing.T) {
	rules, err := verdict.LoadRules("")
	require.NoError(t, err)

	previousMax := -1
	for _, value := range verdict.Verdicts {
		band := rules.Bands[value]
		assert.Greater(t, band.Min, previousMax)
		previousMax = band.Max
	}

	// Concrete results must stay inside their own band.
	flagged := matchedSignals()
	flagged.Flags = []safety.Flag{{Type: safety.WatchlistProduct, Severity: safety.SeverityHigh, ReasonCode: "watchlist_alert"}}
	result := verdict.Evaluate(flagged, rules)
	assert.Equal(t, verdict.Flagged, result.Verdict)
	assert.Less(t, result.Score, rules.Bands[verdict.Unreadable].Min)
}

func TestReasonsAreCappedAndOrdered(t *testing.T) {
	rules, err := verdict.LoadRules("")
	require.NoError(t, err)

	signals := matchedSignals()
	signals.Fields = map[string]verify.FieldOutcome{}
	for _, field := range []string{
		verify.FieldNRN, verify.FieldName, verify.FieldManufacturer, verify.FieldIngredients,
		verify.FieldStrength, verify.FieldExpiry, verify.FieldRegistryStatus,
	} {
		signals.Fields[field] = verify.Disagree
	}
	signals.Extraction.MissingFields = []string{"batch_no", "mfg_date"}

	result := verdict.Evaluate(signals, rules)
	assert.LessOrEqual(t, len(result.Reasons), rules.Limits.MaxReasons)
	assert.Equal(t, verdict.ReasonFieldMismatch, result.Reasons[0].Code)
	for index := 1; index < len(result.Reasons); index++ {
		if result.Reasons[index-1].Field == "" || result.Reasons[index].Field == "" {
			continue
		}
		assert.Less(t, result.Reasons[index-1].Field, result.Reasons[index].Field)
	}
}

func TestReasonsCarryNoUnreviewedText(t *testing.T) {
	rules, err := verdict.LoadRules("")
	require.NoError(t, err)
	catalog, err := verdict.LoadCatalog("")
	require.NoError(t, err)

	signals := matchedSignals()
	signals.Fields[verify.FieldName] = verify.Disagree
	signals.Flags = []safety.Flag{{Type: safety.StrengthDeviation, Severity: safety.SeverityMedium, ReasonCode: "strength_deviation"}}
	signals.Extraction.MissingFields = []string{"batch_no"}

	for _, reason := range verdict.Evaluate(signals, rules).Reasons {
		_, err := catalog.Render(reason)
		require.NoError(t, err, "reason %s must render from the reviewed catalog", reason.Code)
		assert.NotContains(t, reason.Code, " ")
	}
}
