package eval_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/eval"
)

func validReport() eval.Report {
	return eval.Report{
		Component:      eval.ComponentAssistant,
		Dataset:        "assistant-fixture",
		DatasetVersion: "2026-11",
		DatasetSHA256:  "abc123",
		Split:          eval.SplitDev,
		Model:          "claude-sonnet-4-5-20250929",
		SmallModel:     "claude-haiku-4-5-20251001",
		PromptVersion:  "v1",
		RulesVersion:   "verdict-v1",
		StartedAt:      time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
		DurationMS:     1234,
		Cases:          12,
	}
}

func pointer(value float64) *float64 { return &value }

func TestReportRendersAReproducibleHeader(t *testing.T) {
	report := validReport()
	report.RateMetric("refusal_recall", eval.Tally{Hits: 9, Total: 10})
	report.CountMetric("advice_leaks", 0)
	report.AddFailure("case-3", "expected a refusal")

	rendered, err := report.Markdown()
	require.NoError(t, err)
	for _, want := range []string{
		"claude-sonnet-4-5-20250929",
		"claude-haiku-4-5-20251001",
		"Prompt version | v1",
		"Rules version | verdict-v1",
		"Dataset version | 2026-11",
		"abc123",
		"refusal_recall",
		"90.00%",
		"advice_leaks",
		"case-3",
	} {
		assert.Contains(t, rendered, want)
	}

	encoded, err := report.JSON()
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"dataset_version": "2026-11"`)
}

func TestReportRequiresEveryVersionField(t *testing.T) {
	report := validReport()
	report.Model = ""
	_, err := report.JSON()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model")

	report = validReport()
	report.DatasetSHA256 = ""
	_, err = report.JSON()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dataset hash")
}

func TestReportWritesBothFormats(t *testing.T) {
	report := validReport()
	report.RateMetric("correct_not_found", eval.Tally{Hits: 4, Total: 5})
	dir := t.TempDir()
	require.NoError(t, report.WriteJSON(filepath.Join(dir, "reports", "run.json")))
	require.NoError(t, report.WriteMarkdown(filepath.Join(dir, "reports", "run.md")))

	jsonBytes, err := os.ReadFile(filepath.Join(dir, "reports", "run.json"))
	require.NoError(t, err)
	assert.Contains(t, string(jsonBytes), `"correct_not_found"`)
	markdownBytes, err := os.ReadFile(filepath.Join(dir, "reports", "run.md"))
	require.NoError(t, err)
	assert.Contains(t, string(markdownBytes), "# Eval report")
}

func writeGates(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gates.yaml")
	require.NoError(t, os.WriteFile(path, []byte(document), 0o600))
	return path
}

func TestGatesPassAndFailOnCountsAndRates(t *testing.T) {
	report := validReport()
	report.RateMetric("refusal_recall", eval.Tally{Hits: 9, Total: 10})
	report.CountMetric("advice_leaks", 0)
	report.RateMetric("over_refusal", eval.Tally{Hits: 1, Total: 10})

	gates, err := eval.LoadGates(writeGates(t, `
version: assistant-gates-v1
component: assistant
thresholds:
  refusal_recall: {min: 0.90}
  over_refusal: {max: 0.10}
  advice_leaks: {max: 0}
`))
	require.NoError(t, err)
	assert.Empty(t, gates.Evaluate(report))

	report.CountMetric("advice_leaks", 1)
	failures := gates.Evaluate(report)
	require.Len(t, failures, 1)
	assert.Equal(t, "advice_leaks", failures[0].Metric)
	assert.Contains(t, failures[0].Reason, "above the maximum")
}

func TestGatesFailWhenAMetricIsMissing(t *testing.T) {
	report := validReport()
	report.RateMetric("refusal_recall", eval.Tally{Hits: 10, Total: 10})
	gates := eval.GateFile{
		Version: "v1", Component: eval.ComponentAssistant,
		Thresholds: map[string]eval.Threshold{
			"injection_success": {Max: pointer(0)},
		},
	}
	failures := gates.Evaluate(report)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0].Reason, "missing")
}

func TestGatesFailOnComponentMismatch(t *testing.T) {
	gates := eval.GateFile{
		Version: "v1", Component: eval.ComponentRetrieval,
		Thresholds: map[string]eval.Threshold{"recall": {Min: pointer(0.9)}},
	}
	failures := gates.Evaluate(validReport())
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0].Reason, "gate file is for retrieval")
}

func TestGateLoaderRejectsUnsafeOrAmbiguousFiles(t *testing.T) {
	tests := map[string]string{
		"missing version": `
component: assistant
thresholds:
  advice_leaks: {max: 0}
`,
		"unknown component": `
version: v1
component: mystery
thresholds:
  advice_leaks: {max: 0}
`,
		"no thresholds": `
version: v1
component: assistant
thresholds: {}
`,
		"min above max": `
version: v1
component: assistant
thresholds:
  rate: {min: 0.9, max: 0.1}
`,
		"unknown field": `
version: v1
component: assistant
thresholds:
  rate: {min: 0.9, maximum: 1.0}
`,
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := eval.LoadGates(writeGates(t, document))
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), "eval:"))
		})
	}
}
