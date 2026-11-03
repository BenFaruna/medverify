package verdict_test

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/verdict"
	"github.com/benfaruna/medverify/internal/verify"
)

type scenarioFile struct {
	Version string     `yaml:"version"`
	Status  string     `yaml:"status"`
	Cases   []scenario `yaml:"cases"`
}

// scenarioFlag mirrors safety.Flag with YAML tags. The production type carries
// JSON tags only, and the scenario table is reviewed as YAML.
type scenarioFlag struct {
	Type       safety.FlagType `yaml:"type"`
	Severity   safety.Severity `yaml:"severity"`
	ReasonCode string          `yaml:"reason_code"`
}

type scenario struct {
	Name            string                         `yaml:"name"`
	Outcome         verify.Outcome                 `yaml:"outcome"`
	Fields          map[string]verify.FieldOutcome `yaml:"fields"`
	Flags           []scenarioFlag                 `yaml:"flags"`
	LabelExpired    bool                           `yaml:"label_expired"`
	DatasetProducts *int                           `yaml:"dataset_products"`
	Extraction      struct {
		Confidence *float64 `yaml:"confidence"`
		Identified *bool    `yaml:"identified"`
		Missing    []string `yaml:"missing"`
	} `yaml:"extraction"`
	Expect struct {
		Verdict     verdict.Verdict    `yaml:"verdict"`
		Confidence  verdict.Confidence `yaml:"confidence"`
		Reasons     []string           `yaml:"reasons"`
		ReasonOrder []string           `yaml:"reason_order"`
		ScoreMin    *int               `yaml:"score_min"`
		ScoreMax    *int               `yaml:"score_max"`
	} `yaml:"expect"`
}

func loadScenarios(t *testing.T) scenarioFile {
	t.Helper()

	data, err := os.ReadFile("testdata/scenarios.yaml")
	require.NoError(t, err)

	var file scenarioFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	require.NoError(t, decoder.Decode(&file))
	return file
}

// TestScenarioTableMatchesTheSpecification is the phase 07 scenario suite. The
// table is the human-written specification, so a failure here means either the
// rules changed without the table or the table needs review; weights are never
// tuned by editing expectations silently.
func TestScenarioTableMatchesTheSpecification(t *testing.T) {
	file := loadScenarios(t)
	rules, err := verdict.LoadRules("")
	require.NoError(t, err)
	catalog, err := verdict.LoadCatalog("")
	require.NoError(t, err)

	assert.Equal(t, "scenarios-v1", file.Version)
	assert.Equal(t, "draft", file.Status, "the table must stay marked draft until it is signed off")
	require.GreaterOrEqual(t, len(file.Cases), 40, "the phase requires at least 40 human-reviewed scenarios")

	totalTargets := map[verdict.Verdict]bool{}
	for _, testCase := range file.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			signals := testCase.signals()
			result := verdict.Evaluate(signals, rules)
			totalTargets[result.Verdict] = true

			// Determinism is also checked over the whole table: the pure
			// evaluator must not depend on map iteration or shared state.
			assert.True(t, reflect.DeepEqual(result, verdict.Evaluate(signals, rules)),
				"evaluating the same signals twice must give an identical result")

			assert.Equal(t, testCase.Expect.Verdict, result.Verdict)
			if testCase.Expect.Confidence != "" {
				assert.Equal(t, testCase.Expect.Confidence, result.Confidence)
			}
			assert.True(t, result.Confidence.Valid())
			assert.Equal(t, rules.Version, result.RulesVersion)

			band := rules.Bands[testCase.Expect.Verdict]
			assert.GreaterOrEqual(t, result.Score, band.Min)
			assert.LessOrEqual(t, result.Score, band.Max)

			if testCase.Expect.ScoreMin != nil {
				assert.GreaterOrEqual(t, result.Score, *testCase.Expect.ScoreMin)
			}
			if testCase.Expect.ScoreMax != nil {
				assert.LessOrEqual(t, result.Score, *testCase.Expect.ScoreMax)
			}

			codes := make([]string, 0, len(result.Reasons))
			for _, reason := range result.Reasons {
				codes = append(codes, reason.Code)
				rendered, err := catalog.Render(reason)
				require.NoError(t, err, "every emitted reason must have reviewed text")
				assert.NotEmpty(t, rendered)
			}
			for _, expected := range testCase.Expect.Reasons {
				assert.Contains(t, codes, expected)
			}
			if len(testCase.Expect.ReasonOrder) > 0 {
				assert.Equal(t, testCase.Expect.ReasonOrder, codes)
			}
			assert.LessOrEqual(t, len(result.Reasons), rules.Limits.MaxReasons)
		})
	}

	// The table must exercise every verdict, not just the common one.
	for _, value := range verdict.Verdicts {
		assert.True(t, totalTargets[value], "scenario table has no case expecting %s", value)
	}
}

func (s scenario) signals() verdict.Signals {
	confidence := 0.90
	if s.Extraction.Confidence != nil {
		confidence = *s.Extraction.Confidence
	}
	identified := true
	if s.Extraction.Identified != nil {
		identified = *s.Extraction.Identified
	}
	products := 12_000
	if s.DatasetProducts != nil {
		products = *s.DatasetProducts
	}
	flags := make([]safety.Flag, 0, len(s.Flags))
	for _, flag := range s.Flags {
		flags = append(flags, safety.Flag{
			Type:       flag.Type,
			Severity:   flag.Severity,
			ReasonCode: flag.ReasonCode,
		})
	}

	return verdict.Signals{
		Outcome:         s.Outcome,
		Fields:          s.Fields,
		Flags:           flags,
		LabelExpired:    s.LabelExpired,
		DatasetProducts: products,
		Extraction: verdict.Extraction{
			OverallConfidence: confidence,
			Identified:        identified,
			MissingFields:     s.Extraction.Missing,
		},
	}
}
