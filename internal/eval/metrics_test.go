package eval_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/eval"
)

func TestRecallAtKAndMatches(t *testing.T) {
	assert.True(t, eval.RecallAtK("4.2", []string{"4.1", "4.2", "6.4"}))
	assert.False(t, eval.RecallAtK("4.4", []string{"4.1", "4.2", "6.4"}))
	assert.True(t, eval.ExactMatch([]string{"a", "b"}, []string{"a", "b"}))
	assert.False(t, eval.ExactMatch([]string{"a", "b"}, []string{"b", "a"}))
	assert.True(t, eval.SetMatch([]string{"a", "b", "a"}, []string{"b", "a"}))
	assert.False(t, eval.SetMatch([]string{"a"}, []string{"a", "b"}))
}

func TestTallyRateHandlesEmpty(t *testing.T) {
	tally := eval.Tally{}
	assert.Zero(t, tally.Rate())
	tally.Add(false)
	tally.Add(true)
	tally.Add(true)
	assert.Equal(t, 3, tally.Total)
	assert.Equal(t, 2, tally.Hits)
	assert.InDelta(t, 2.0/3.0, tally.Rate(), 1e-9)
}

func TestCohensKappaMatchesAHandComputedExample(t *testing.T) {
	pairs := make([]eval.LabelPair, 0, 50)
	for i := 0; i < 20; i++ {
		pairs = append(pairs, eval.LabelPair{Expected: "A", Got: "A"})
	}
	for i := 0; i < 5; i++ {
		pairs = append(pairs, eval.LabelPair{Expected: "A", Got: "B"})
	}
	for i := 0; i < 10; i++ {
		pairs = append(pairs, eval.LabelPair{Expected: "B", Got: "A"})
	}
	for i := 0; i < 15; i++ {
		pairs = append(pairs, eval.LabelPair{Expected: "B", Got: "B"})
	}
	// observed 35/50 = 0.7; chance = 0.5*0.6 + 0.5*0.4 = 0.5; kappa = 0.4.
	kappa, err := eval.CohensKappa(pairs)
	require.NoError(t, err)
	assert.InDelta(t, 0.4, kappa, 1e-9)
}

func TestCohensKappaRejectsDegenerateSamples(t *testing.T) {
	_, err := eval.CohensKappa(nil)
	require.Error(t, err)

	_, err = eval.CohensKappa([]eval.LabelPair{{Expected: "A", Got: "A"}, {Expected: "A", Got: "A"}})
	require.Error(t, err)
}

func TestConfusionTableMatchesHandComputedCounts(t *testing.T) {
	table, err := eval.BuildConfusion([]string{"safe_answer", "refusal"}, []eval.LabelPair{
		{Expected: "safe_answer", Got: "safe_answer"},
		{Expected: "safe_answer", Got: "safe_answer"},
		{Expected: "safe_answer", Got: "refusal"},
		{Expected: "refusal", Got: "refusal"},
		{Expected: "safe_answer", Got: "unexpected"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"safe_answer", "refusal"}, table.Labels)
	assert.Equal(t, [][]int{{2, 1}, {0, 1}}, table.Counts)
	assert.Equal(t, 4, table.Totals["safe_answer"])
	assert.Equal(t, 1, table.Totals["refusal"])
	assert.Equal(t, 1, table.Totals["(unknown)"])
	// Three correct out of four real pairs, plus the unknown pair stays in the
	// denominator: 3/5.
	assert.InDelta(t, 0.6, table.Accuracy(), 1e-9)
}

func TestConfusionTableRejectsUnknownExpectedLabels(t *testing.T) {
	_, err := eval.BuildConfusion([]string{"a"}, []eval.LabelPair{{Expected: "b", Got: "a"}})
	require.Error(t, err)
}
