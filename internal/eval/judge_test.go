package eval_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/eval"
)

func TestCalibrateJudgeComputesAgreementAndKappa(t *testing.T) {
	records := []eval.JudgeRecord{
		{ID: "1", Human: "supported", Judge: "supported"},
		{ID: "2", Human: "supported", Judge: "supported"},
		{ID: "3", Human: "supported", Judge: "unsupported"},
		{ID: "4", Human: "unsupported", Judge: "supported"},
		{ID: "5", Human: "unsupported", Judge: "unsupported"},
		{ID: "6", Human: "unsupported", Judge: "unsupported"},
	}
	calibration, err := eval.CalibrateJudge(records, []string{"supported", "unsupported"})
	require.NoError(t, err)

	assert.Equal(t, 6, calibration.Cases)
	assert.InDelta(t, 4.0/6.0, calibration.Agreement, 1e-9)
	assert.InDelta(t, 1.0/3.0, calibration.Kappa, 1e-9)
	assert.Equal(t, [][]int{{2, 1}, {1, 2}}, calibration.Confusion.Counts)
	assert.Empty(t, calibration.Meets(0.3, 6))
	assert.Len(t, calibration.Meets(0.5, 6), 1)
	assert.Len(t, calibration.Meets(0.3, 50), 1)
}

func TestCalibrateJudgeRejectsUnusableLabels(t *testing.T) {
	tests := map[string]struct {
		records []eval.JudgeRecord
		labels  []string
	}{
		"no labels": {records: []eval.JudgeRecord{{ID: "1", Human: "a", Judge: "a"}}},
		"duplicate label": {
			records: []eval.JudgeRecord{{ID: "1", Human: "a", Judge: "a"}},
			labels:  []string{"a", "a"},
		},
		"unknown human label": {
			records: []eval.JudgeRecord{{ID: "1", Human: "c", Judge: "a"}},
			labels:  []string{"a", "b"},
		},
		"unknown judge label": {
			records: []eval.JudgeRecord{{ID: "1", Human: "a", Judge: "c"}},
			labels:  []string{"a", "b"},
		},
		"duplicate case": {
			records: []eval.JudgeRecord{
				{ID: "1", Human: "a", Judge: "a"},
				{ID: "1", Human: "b", Judge: "b"},
			},
			labels: []string{"a", "b"},
		},
		"missing id": {
			records: []eval.JudgeRecord{{Human: "a", Judge: "a"}},
			labels:  []string{"a", "b"},
		},
		"degenerate single label": {
			records: []eval.JudgeRecord{
				{ID: "1", Human: "a", Judge: "a"},
				{ID: "2", Human: "a", Judge: "a"},
			},
			labels: []string{"a", "b"},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := eval.CalibrateJudge(test.records, test.labels)
			require.Error(t, err)
		})
	}
}

func TestLoadJudgeLabelsRejectsUnknownFieldsAndEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "labels.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"id":"1","human":"supported","judge":"supported","answer":"raw user text"}`+"\n"), 0o600))
	_, err := eval.LoadJudgeLabels(path)
	require.Error(t, err)

	empty := filepath.Join(dir, "empty.jsonl")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	_, err = eval.LoadJudgeLabels(empty)
	require.Error(t, err)
}

func TestLoadJudgeLabelsReadsPairs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "labels.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		`{"id":"1","human":"supported","judge":"supported"}`+"\n"+
			`{"id":"2","human":"unsupported","judge":"supported"}`+"\n"), 0o600))
	records, err := eval.LoadJudgeLabels(path)
	require.NoError(t, err)
	assert.Len(t, records, 2)
	assert.Equal(t, "unsupported", records[1].Human)
}
