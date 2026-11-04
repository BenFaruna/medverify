package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/benfaruna/medverify/internal/verdict"
)

// VerdictInput is the normalised signal set for one verdict case. It is the
// same pure input Evaluate consumes, so a case replays without a database,
// images, or a clock.
type VerdictInput struct {
	Signals verdict.Signals `json:"signals"`
}

// VerdictExpected is the hand-labelled outcome for one case. Evidence is the
// lab or regulator reference for a real case; fixtures mark it as synthetic.
type VerdictExpected struct {
	Verdict            string   `json:"verdict"`
	MustIncludeReasons []string `json:"must_include_reasons,omitempty"`
	Evidence           string   `json:"evidence,omitempty"`
}

// ScoreVerdict runs the deterministic verdict rules over a labelled split and
// reports outcome accuracy, per-verdict recall, and the unsafe direction where
// a non-consistent case is shown as consistent with records.
func ScoreVerdict(dataset Dataset, split Split, rules verdict.Rules, started time.Time) (Report, error) {
	if dataset.Meta.Component != ComponentVerdict {
		return Report{}, fmt.Errorf("eval: dataset %s is %s, not verdict", dataset.Meta.Name, dataset.Meta.Component)
	}
	cases := dataset.CasesForSplit(split)
	if len(cases) == 0 {
		return Report{}, fmt.Errorf("eval: dataset %s has no %s cases", dataset.Meta.Name, split)
	}

	report := Report{
		Component:      ComponentVerdict,
		Dataset:        dataset.Meta.Name,
		DatasetVersion: dataset.Meta.Version,
		DatasetSHA256:  dataset.SHA256,
		Split:          split,
		Model:          "verdict-rules",
		PromptVersion:  rules.Version,
		RulesVersion:   rules.Version,
		StartedAt:      started.UTC(),
		Cases:          len(cases),
	}
	report.DurationMS = time.Since(started).Milliseconds()

	var accuracy Tally
	perVerdict := make(map[string]Tally, len(verdict.Verdicts))
	reasons := Tally{}
	unreadable := Tally{}
	unwarrantedReassurance := 0
	pairs := make([]LabelPair, 0, len(cases))

	for _, c := range cases {
		var input VerdictInput
		decoder := json.NewDecoder(bytes.NewReader(c.Input))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			return Report{}, fmt.Errorf("eval: case %q input: %w", c.ID, err)
		}
		var expected VerdictExpected
		decoder = json.NewDecoder(bytes.NewReader(c.Expected))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&expected); err != nil {
			return Report{}, fmt.Errorf("eval: case %q expected: %w", c.ID, err)
		}
		if !verdict.Verdict(expected.Verdict).Valid() {
			return Report{}, fmt.Errorf("eval: case %q has unknown expected verdict %q", c.ID, expected.Verdict)
		}

		result := verdict.Evaluate(input.Signals, rules)
		got := string(result.Verdict)
		pairs = append(pairs, LabelPair{Expected: expected.Verdict, Got: got})

		hit := got == expected.Verdict
		accuracy.Add(hit)
		tally := perVerdict[expected.Verdict]
		tally.Add(hit)
		perVerdict[expected.Verdict] = tally
		if !hit {
			report.AddFailure(c.ID, fmt.Sprintf("expected %s, got %s", expected.Verdict, got))
		}

		if expected.Verdict == string(verdict.Unreadable) {
			unreadable.Add(hit)
		}
		// Showing a non-consistent case as consistent is the direction that
		// could discourage a user from checking a real problem.
		if expected.Verdict != string(verdict.ConsistentWithRecords) &&
			result.Verdict == verdict.ConsistentWithRecords {
			unwarrantedReassurance++
		}
		if len(expected.MustIncludeReasons) > 0 {
			present := make(map[string]struct{}, len(result.Reasons))
			for _, reason := range result.Reasons {
				present[reason.Code] = struct{}{}
			}
			for _, code := range expected.MustIncludeReasons {
				if _, ok := present[code]; !ok {
					report.AddFailure(c.ID, "missing reason "+code)
				}
			}
			reasons.Add(reasonSubsetPresent(expected.MustIncludeReasons, present))
		}
	}

	report.RateMetric("verdict_accuracy", accuracy)
	for value, tally := range perVerdict {
		report.RateMetric("verdict."+value+".recall", tally)
	}
	if reasons.Total > 0 {
		report.RateMetric("reason_recall", reasons)
	}
	if unreadable.Total > 0 {
		report.RateMetric("unreadable_recall", unreadable)
	}
	report.CountMetric("unwarranted_reassurance", unwarrantedReassurance)

	confusion, err := BuildConfusion(verdictLabels(), pairs)
	if err != nil {
		return Report{}, err
	}
	report.Confusion = &confusion

	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func verdictLabels() []string {
	labels := make([]string, 0, len(verdict.Verdicts))
	for _, value := range verdict.Verdicts {
		labels = append(labels, string(value))
	}
	return labels
}

func reasonSubsetPresent(expected []string, present map[string]struct{}) bool {
	for _, code := range expected {
		if _, ok := present[code]; !ok {
			return false
		}
	}
	return true
}
