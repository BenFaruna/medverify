package eval_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/eval"
	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/verdict"
	"github.com/benfaruna/medverify/internal/verify"
)

func verdictCase(id string, signals verdict.Signals, expected eval.VerdictExpected, split eval.Split) eval.Case {
	input, _ := json.Marshal(eval.VerdictInput{Signals: signals})
	expectedJSON, _ := json.Marshal(expected)
	return eval.Case{
		ID:        id,
		Component: eval.ComponentVerdict,
		Input:     input,
		Expected:  expectedJSON,
		Source:    "synthetic fixture, no lab or regulator evidence",
		Labeler:   "fixture",
		Date:      "2026-11-04",
		Split:     split,
	}
}

func readableSignals(outcome verify.Outcome) verdict.Signals {
	return verdict.Signals{
		Outcome:         outcome,
		DatasetProducts: 3,
		Extraction:      verdict.Extraction{Identified: true, OverallConfidence: 0.95},
	}
}

func verdictDataset() eval.Dataset {
	cases := []eval.Case{
		verdictCase("consistent", readableSignals(verify.MatchedConsistent),
			eval.VerdictExpected{Verdict: string(verdict.ConsistentWithRecords), MustIncludeReasons: []string{verdict.ReasonLabelConsistent}}, eval.SplitDev),
		verdictCase("not-found", readableSignals(verify.NotFoundInDataset),
			eval.VerdictExpected{Verdict: string(verdict.NotFoundInRecords), MustIncludeReasons: []string{verdict.ReasonNotInDataset}}, eval.SplitDev),
		verdictCase("unreadable", verdict.Signals{Outcome: verify.UnreadableInput},
			eval.VerdictExpected{Verdict: string(verdict.Unreadable), MustIncludeReasons: []string{verdict.ReasonInsufficientLabelData}}, eval.SplitDev),
		verdictCase("flagged", func() verdict.Signals {
			signals := readableSignals(verify.MatchedConsistent)
			signals.Flags = []safety.Flag{{Type: safety.WatchlistProduct, Severity: safety.SeverityHigh, ReasonCode: "watchlist_hit"}}
			return signals
		}(), eval.VerdictExpected{Verdict: string(verdict.Flagged), MustIncludeReasons: []string{verdict.ReasonSafetyFlag}}, eval.SplitDev),
		verdictCase("mismatch", func() verdict.Signals {
			signals := readableSignals(verify.MatchedWithMismatch)
			signals.Fields = map[string]verify.FieldOutcome{verify.FieldName: verify.Disagree}
			return signals
		}(), eval.VerdictExpected{Verdict: string(verdict.MismatchFound)}, eval.SplitDev),
		verdictCase("candidate", readableSignals(verify.CandidateOnly),
			eval.VerdictExpected{Verdict: string(verdict.NeedsReview)}, eval.SplitDev),
		verdictCase("test-only", readableSignals(verify.MatchedConsistent),
			eval.VerdictExpected{Verdict: string(verdict.ConsistentWithRecords)}, eval.SplitTest),
	}
	return eval.Dataset{
		Meta:  eval.Meta{Name: "verdict-fixture", Version: "2026-11-fixture", Component: eval.ComponentVerdict},
		Cases: cases, SHA256: "abc",
	}
}

func TestScoreVerdictRunsDeterministicRules(t *testing.T) {
	rules, err := verdict.LoadRules("")
	require.NoError(t, err)

	report, err := eval.ScoreVerdict(verdictDataset(), eval.SplitDev, rules, time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	assert.Equal(t, eval.ComponentVerdict, report.Component)
	assert.Equal(t, 6, report.Cases)
	assert.Equal(t, "verdict-rules", report.Model)
	assert.Equal(t, rules.Version, report.RulesVersion)
	assert.InDelta(t, 1.0, report.Metrics["verdict_accuracy"].Value, 1e-9)
	assert.InDelta(t, 1.0, report.Metrics["unreadable_recall"].Value, 1e-9)
	assert.Equal(t, 0, report.Metrics["unwarranted_reassurance"].Count)
	assert.InDelta(t, 1.0, report.Metrics["reason_recall"].Value, 1e-9)
	assert.InDelta(t, 1.0, report.Metrics["verdict.flagged.recall"].Value, 1e-9)
	assert.Empty(t, report.Failures)
	require.NotNil(t, report.Confusion)
	assert.Equal(t, [][]int{{1, 0, 0, 0, 0, 0}, {0, 1, 0, 0, 0, 0}, {0, 0, 1, 0, 0, 0}, {0, 0, 0, 1, 0, 0}, {0, 0, 0, 0, 1, 0}, {0, 0, 0, 0, 0, 1}}, report.Confusion.Counts)
}

func TestScoreVerdictMarksUnwarrantedReassuranceAndFailures(t *testing.T) {
	rules, err := verdict.LoadRules("")
	require.NoError(t, err)

	dataset := verdictDataset()
	// Claim a flagged case should be flagged, but feed a clean consistent match.
	dataset.Cases[3] = verdictCase("flagged", readableSignals(verify.MatchedConsistent),
		eval.VerdictExpected{Verdict: string(verdict.Flagged)}, eval.SplitDev)
	// Claim an unreadable case should be unreadable, but feed a clean match.
	dataset.Cases[2] = verdictCase("unreadable", readableSignals(verify.MatchedConsistent),
		eval.VerdictExpected{Verdict: string(verdict.Unreadable)}, eval.SplitDev)

	report, err := eval.ScoreVerdict(dataset, eval.SplitDev, rules, time.Now())
	require.NoError(t, err)

	assert.Equal(t, 2, report.Metrics["unwarranted_reassurance"].Count)
	assert.InDelta(t, 0.0, report.Metrics["unreadable_recall"].Value, 1e-9)
	assert.InDelta(t, 4.0/6.0, report.Metrics["verdict_accuracy"].Value, 1e-9)
	assert.Len(t, report.Failures, 2)
}

func TestScoreVerdictRejectsBrokenCases(t *testing.T) {
	rules, err := verdict.LoadRules("")
	require.NoError(t, err)

	dataset := verdictDataset()
	dataset.Meta.Component = eval.ComponentAssistant
	_, err = eval.ScoreVerdict(dataset, eval.SplitDev, rules, time.Now())
	require.Error(t, err)

	dataset = verdictDataset()
	dataset.Cases[0].Expected = json.RawMessage(`{"verdict":"safe"}`)
	_, err = eval.ScoreVerdict(dataset, eval.SplitDev, rules, time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown expected verdict")

	dataset = verdictDataset()
	_, err = eval.ScoreVerdict(dataset, eval.Split("holdout"), rules, time.Now())
	require.Error(t, err)
}
