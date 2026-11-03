package verdict_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/verdict"
)

func TestEmbeddedRulesLoadAndAreOrdered(t *testing.T) {
	rules, err := verdict.LoadRules("")
	require.NoError(t, err)

	assert.Equal(t, verdict.ConfigVersion, rules.Version)
	previousMax := -1
	for _, value := range verdict.Verdicts {
		band, ok := rules.Bands[value]
		require.True(t, ok, "missing band for %s", value)
		assert.Greater(t, band.Min, previousMax, "bands must not overlap and must follow verdict order")
		assert.LessOrEqual(t, band.Min, band.Max)
		previousMax = band.Max
	}
	assert.Equal(t, 100, previousMax, "the bands must cover the full score range")
}

func TestRulesValidationRejectsUnsafeConfigurations(t *testing.T) {
	base := func() verdict.Rules {
		rules, err := verdict.LoadRules("")
		require.NoError(t, err)
		return rules
	}

	cases := map[string]func(rules *verdict.Rules){
		"missing version": func(rules *verdict.Rules) {
			rules.Version = ""
		},
		"gate at zero": func(rules *verdict.Rules) {
			rules.Gate.MinExtractionConfidence = 0
		},
		"gate above one": func(rules *verdict.Rules) {
			rules.Gate.MinExtractionConfidence = 1
		},
		"missing band": func(rules *verdict.Rules) {
			delete(rules.Bands, verdict.NeedsReview)
		},
		"band out of range": func(rules *verdict.Rules) {
			rules.Bands[verdict.Unreadable] = verdict.Band{Min: 100, Max: 101}
		},
		"band min above max": func(rules *verdict.Rules) {
			band := rules.Bands[verdict.Flagged]
			band.Min, band.Max = band.Max, band.Min
			rules.Bands[verdict.Flagged] = band
		},
		"overlapping bands": func(rules *verdict.Rules) {
			band := rules.Bands[verdict.MismatchFound]
			band.Min = 50
			rules.Bands[verdict.MismatchFound] = band
		},
		"unknown verdict band": func(rules *verdict.Rules) {
			rules.Bands["looks_fine"] = verdict.Band{Min: 0, Max: 1}
		},
		"negative weight": func(rules *verdict.Rules) {
			rules.Weights.FieldMismatch = -1
		},
		"oversized weight": func(rules *verdict.Rules) {
			rules.Weights.MediumFlag = 41
		},
		"confidence levels out of order": func(rules *verdict.Rules) {
			rules.Confidence.HighExtraction = rules.Confidence.MediumExtraction
		},
		"medium confidence below gate": func(rules *verdict.Rules) {
			rules.Confidence.MediumExtraction = 0.4
		},
		"no reasons allowed": func(rules *verdict.Rules) {
			rules.Limits.MaxReasons = 0
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			rules := base()
			mutate(&rules)
			assert.Error(t, rules.Validate())
		})
	}
}

func TestParseRulesRejectsUnknownFields(t *testing.T) {
	_, err := verdict.ParseRules([]byte("version: verdict-test\ngate:\n  min_extraction_confidence: 0.5\nunknown_key: 1\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown_key")

	// A typo inside a nested section must fail too, not silently zero a weight.
	_, err = verdict.ParseRules([]byte("version: verdict-test\nweights:\n  field_mismach: 8\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field_mismach")
}
