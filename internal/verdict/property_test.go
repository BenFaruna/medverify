package verdict_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/verdict"
	"github.com/benfaruna/medverify/internal/verify"
)

func writeTempFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func randomSignals(rt *rapid.T) verdict.Signals {
	outcomes := []verify.Outcome{
		verify.MatchedConsistent,
		verify.MatchedWithMismatch,
		verify.CandidateOnly,
		verify.Ambiguous,
		verify.NotFoundInDataset,
		verify.UnreadableInput,
		"",
	}
	fieldOutcomes := []verify.FieldOutcome{verify.Agree, verify.Disagree, verify.Unknown}

	fields := make(map[string]verify.FieldOutcome)
	for _, field := range []string{
		verify.FieldNRN, verify.FieldName, verify.FieldManufacturer,
		verify.FieldIngredients, verify.FieldStrength, verify.FieldExpiry,
	} {
		fields[field] = rapid.SampledFrom(fieldOutcomes).Draw(rt, field)
	}

	flags := make([]safety.Flag, 0, 3)
	for index := 0; index < rapid.IntRange(0, 3).Draw(rt, "flag_count"); index++ {
		flags = append(flags, safety.Flag{
			Type:     rapid.SampledFrom(safety.FlagTypes).Draw(rt, "flag_type"),
			Severity: rapid.SampledFrom([]safety.Severity{safety.SeverityHigh, safety.SeverityMedium, safety.SeverityLow}).Draw(rt, "severity"),
		})
	}

	return verdict.Signals{
		Outcome:         rapid.SampledFrom(outcomes).Draw(rt, "outcome"),
		Fields:          fields,
		Flags:           flags,
		LabelExpired:    rapid.Bool().Draw(rt, "expired"),
		DatasetProducts: rapid.IntRange(0, 20000).Draw(rt, "products"),
		CandidateCount:  rapid.IntRange(0, 5).Draw(rt, "candidates"),
		Extraction: verdict.Extraction{
			OverallConfidence: rapid.Float64Range(0, 1).Draw(rt, "confidence"),
			Identified:        rapid.Bool().Draw(rt, "identified"),
		},
	}
}

// TestEvaluateIsDeterministic proves there is no hidden state: the same signals
// and rules always produce byte-identical results, including reason order.
func TestEvaluateIsDeterministic(t *testing.T) {
	t.Parallel()

	rules, err := verdict.LoadRules("")
	require.NoError(t, err)

	rapid.Check(t, func(rt *rapid.T) {
		signals := randomSignals(rt)
		first := verdict.Evaluate(signals, rules)
		second := verdict.Evaluate(signals, rules)
		require.Equal(rt, first, second)
	})
}

// TestAddingAConcernNeverLowersTheScore is the monotonicity property: a flag, a
// disagreement, or an expired label may leave the score unchanged or raise it,
// but it must never make a result look less concerning.
func TestAddingAConcernNeverLowersTheScore(t *testing.T) {
	t.Parallel()

	rules, err := verdict.LoadRules("")
	require.NoError(t, err)

	rapid.Check(t, func(rt *rapid.T) {
		base := randomSignals(rt)
		baseResult := verdict.Evaluate(base, rules)

		withHighFlag := base
		withHighFlag.Flags = append(append([]safety.Flag(nil), base.Flags...),
			safety.Flag{Type: safety.WatchlistProduct, Severity: safety.SeverityHigh})
		highResult := verdict.Evaluate(withHighFlag, rules)
		require.GreaterOrEqual(rt, highResult.Score, baseResult.Score)

		withMediumFlag := base
		withMediumFlag.Flags = append(append([]safety.Flag(nil), base.Flags...),
			safety.Flag{Type: safety.StrengthDeviation, Severity: safety.SeverityMedium})
		mediumResult := verdict.Evaluate(withMediumFlag, rules)
		require.GreaterOrEqual(rt, mediumResult.Score, baseResult.Score)

		withExpired := base
		withExpired.LabelExpired = true
		expiredResult := verdict.Evaluate(withExpired, rules)
		require.GreaterOrEqual(rt, expiredResult.Score, baseResult.Score)

		withDisagreement := base
		withDisagreement.Fields = map[string]verify.FieldOutcome{}
		for field, outcome := range base.Fields {
			withDisagreement.Fields[field] = outcome
		}
		withDisagreement.Fields[verify.FieldName] = verify.Disagree
		disagreementResult := verdict.Evaluate(withDisagreement, rules)
		require.GreaterOrEqual(rt, disagreementResult.Score, baseResult.Score)
	})
}
