package eval

import (
	"fmt"
	"math"
	"sort"
)

// Tally is a count of correct outcomes out of a total.
type Tally struct {
	Hits  int `json:"hits"`
	Total int `json:"total"`
}

// Add records one outcome.
func (t *Tally) Add(hit bool) {
	t.Total++
	if hit {
		t.Hits++
	}
}

// Rate returns hits/total, or 0 for an empty tally. Callers that gate on a rate
// must also check Total so an empty slice cannot pass a floor.
func (t Tally) Rate() float64 {
	if t.Total == 0 {
		return 0
	}
	return float64(t.Hits) / float64(t.Total)
}

// RecallAtK reports whether the wanted section appears among the retrieved
// hits. K is applied by the caller; this helper only answers membership so the
// scoring stays exact for any K.
func RecallAtK(wanted string, got []string) bool {
	for _, value := range got {
		if value == wanted {
			return true
		}
	}
	return false
}

// ExactMatch reports whether two string slices contain the same values in the
// same order.
func ExactMatch(expected, got []string) bool {
	if len(expected) != len(got) {
		return false
	}
	for i := range expected {
		if expected[i] != got[i] {
			return false
		}
	}
	return true
}

// SetMatch reports whether two string slices contain the same distinct values,
// ignoring order and duplicates.
func SetMatch(expected, got []string) bool {
	return ExactMatch(distinctSorted(expected), distinctSorted(got))
}

func distinctSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// LabelPair is one human label and one system label for kappa.
type LabelPair struct {
	Expected string
	Got      string
}

// CohensKappa computes agreement corrected for chance. It returns an error when
// kappa is undefined: no pairs, or both raters used only one identical label.
// Reporting a made-up 1.0 in those cases would overstate judge quality.
func CohensKappa(pairs []LabelPair) (float64, error) {
	if len(pairs) == 0 {
		return 0, fmt.Errorf("eval: kappa needs at least one labelled pair")
	}
	countsExpected := map[string]int{}
	countsGot := map[string]int{}
	agree := 0
	for _, pair := range pairs {
		countsExpected[pair.Expected]++
		countsGot[pair.Got]++
		if pair.Expected == pair.Got {
			agree++
		}
	}
	total := float64(len(pairs))
	observed := float64(agree) / total
	expected := 0.0
	for label, count := range countsExpected {
		expected += (float64(count) / total) * (float64(countsGot[label]) / total)
	}
	if math.Abs(1-expected) < 1e-12 {
		return 0, fmt.Errorf("eval: kappa is undefined when both raters use one identical label")
	}
	return (observed - expected) / (1 - expected), nil
}

// ConfusionTable counts expected-versus-got labels in a fixed label order so a
// report renders deterministically. Rows are expected; columns are got.
type ConfusionTable struct {
	Labels []string       `json:"labels"`
	Counts [][]int        `json:"counts"`
	Totals map[string]int `json:"totals"`
}

// BuildConfusion builds a square table. A got label outside the declared set is
// counted in Totals[expected] but not placed in a column: silently adding a
// label would hide an unknown output.
func BuildConfusion(labels []string, pairs []LabelPair) (ConfusionTable, error) {
	index := make(map[string]int, len(labels))
	for i, label := range labels {
		if _, exists := index[label]; exists {
			return ConfusionTable{}, fmt.Errorf("eval: duplicate confusion label %q", label)
		}
		index[label] = i
	}
	counts := make([][]int, len(labels))
	for i := range counts {
		counts[i] = make([]int, len(labels))
	}
	totals := make(map[string]int, len(labels))
	unknown := 0
	for _, pair := range pairs {
		expectedIndex, ok := index[pair.Expected]
		if !ok {
			return ConfusionTable{}, fmt.Errorf("eval: expected label %q is not in the confusion set", pair.Expected)
		}
		totals[pair.Expected]++
		gotIndex, ok := index[pair.Got]
		if !ok {
			unknown++
			continue
		}
		counts[expectedIndex][gotIndex]++
	}
	if unknown > 0 {
		// Keep the count visible rather than letting unknown outputs vanish.
		totals["(unknown)"] = unknown
	}
	copied := make([]string, len(labels))
	copy(copied, labels)
	return ConfusionTable{Labels: copied, Counts: counts, Totals: totals}, nil
}

// Accuracy is the trace of the confusion table divided by all pairs, excluding
// unknown outputs from the numerator but keeping them in the denominator.
func (t ConfusionTable) Accuracy() float64 {
	correct, total := 0, 0
	for row := range t.Counts {
		for column := range t.Counts[row] {
			total += t.Counts[row][column]
			if row == column {
				correct += t.Counts[row][column]
			}
		}
	}
	if unknown := t.Totals["(unknown)"]; unknown > 0 {
		total += unknown
	}
	if total == 0 {
		return 0
	}
	return float64(correct) / float64(total)
}
