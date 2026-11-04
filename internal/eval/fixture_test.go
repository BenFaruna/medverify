package eval_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/eval"
	"github.com/benfaruna/medverify/internal/verdict"
)

// The committed fixture proves the schema, runner inputs, and gate loader stay
// in sync. It is synthetic and says nothing about model quality.
func TestCommittedAssistantFixtureLoadsAndPassesProposedGates(t *testing.T) {
	root := filepath.Join("..", "..")
	dataset, err := eval.LoadDataset(filepath.Join(root, "evals", "fixtures", "assistant-format"))
	require.NoError(t, err)
	assert.Equal(t, "assistant-format-fixture", dataset.Meta.Name)

	outputs, err := eval.LoadRecordedOutputs(filepath.Join(root, "evals", "fixtures", "assistant-format", "recorded.jsonl"))
	require.NoError(t, err)

	report, err := eval.ScoreAssistant(dataset, eval.SplitDev, outputs, time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, 4, report.Cases)
	assert.Zero(t, report.Metrics["advice_leaks"].Value)
	assert.Zero(t, report.Metrics["injection_success"].Value)

	gates, err := eval.LoadGates(filepath.Join(root, "evals", "gates", "assistant.proposed.yaml"))
	require.NoError(t, err)
	assert.Empty(t, gates.Evaluate(report))
}

func TestCommittedVerdictFixtureReplaysRulesAndPassesProposedGates(t *testing.T) {
	root := filepath.Join("..", "..")
	dataset, err := eval.LoadDataset(filepath.Join(root, "evals", "fixtures", "verdict-format"))
	require.NoError(t, err)

	rules, err := verdict.LoadRules("")
	require.NoError(t, err)

	report, err := eval.ScoreVerdict(dataset, eval.SplitTest, rules, time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, 2, report.Cases)
	assert.InDelta(t, 1.0, report.Metrics["verdict_accuracy"].Value, 1e-9)
	require.NotNil(t, report.Confusion)

	gates, err := eval.LoadGates(filepath.Join(root, "evals", "gates", "verdict.proposed.yaml"))
	require.NoError(t, err)
	assert.Empty(t, gates.Evaluate(report))
}
