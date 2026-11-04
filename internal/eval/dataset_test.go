package eval_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/eval"
)

func writeDataset(t *testing.T, meta string, cases ...string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, eval.MetaFile), []byte(meta), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, eval.CasesFile), []byte(strings.Join(cases, "\n")+"\n"), 0o600))
	return dir
}

func caseLine(t *testing.T, c eval.Case) string {
	t.Helper()
	encoded, err := json.Marshal(c)
	require.NoError(t, err)
	return string(encoded)
}

const validMeta = `{
  "name": "assistant-fixture",
  "version": "2026-11",
  "component": "assistant",
  "description": "format fixture",
  "created_at": "2026-11-04"
}`

func validCase() eval.Case {
	return eval.Case{
		ID:        "case-1",
		Component: eval.ComponentAssistant,
		Input:     json.RawMessage(`{"question":"is it registered?"}`),
		Expected:  json.RawMessage(`{"behavior":"answer"}`),
		Tags:      eval.Tags{Language: "en", Category: "registration"},
		Source:    "synthetic",
		Labeler:   "fixture",
		Date:      "2026-11-04",
		Split:     eval.SplitDev,
	}
}

func TestLoadDatasetAcceptsValidDevAndTestCases(t *testing.T) {
	first := validCase()
	second := validCase()
	second.ID = "case-2"
	second.Input = json.RawMessage(`{"question":"what is the dose?"}`)
	second.Split = eval.SplitTest

	dataset, err := eval.LoadDataset(writeDataset(t, validMeta, caseLine(t, first), caseLine(t, second)))
	require.NoError(t, err)
	assert.Equal(t, "assistant-fixture", dataset.Meta.Name)
	assert.Equal(t, "2026-11", dataset.Meta.Version)
	assert.Len(t, dataset.Cases, 2)
	assert.Len(t, dataset.CasesForSplit(eval.SplitDev), 1)
	assert.Len(t, dataset.CasesForSplit(eval.SplitTest), 1)
	assert.NotEmpty(t, dataset.SHA256)
	assert.Equal(t, map[eval.Split]int{eval.SplitDev: 1, eval.SplitTest: 1}, dataset.SplitCounts())
}

func TestLoadDatasetRejectsBadCases(t *testing.T) {
	tests := map[string]struct {
		meta  string
		cases []eval.Case
		want  string
	}{
		"missing id": {
			cases: []eval.Case{func() eval.Case { c := validCase(); c.ID = ""; return c }()},
			want:  "has no id",
		},
		"duplicate id": {
			cases: []eval.Case{validCase(), validCase()},
			want:  "duplicate case id",
		},
		"bad split": {
			cases: []eval.Case{func() eval.Case { c := validCase(); c.Split = "train"; return c }()},
			want:  "needs split",
		},
		"component mismatch": {
			cases: []eval.Case{func() eval.Case { c := validCase(); c.Component = eval.ComponentVerdict; return c }()},
			want:  "does not match dataset component",
		},
		"missing input": {
			cases: []eval.Case{func() eval.Case { c := validCase(); c.Input = nil; return c }()},
			want:  "no valid input JSON",
		},
		"missing source": {
			cases: []eval.Case{func() eval.Case { c := validCase(); c.Source = ""; return c }()},
			want:  "needs a source",
		},
		"missing labeler": {
			cases: []eval.Case{func() eval.Case { c := validCase(); c.Labeler = ""; return c }()},
			want:  "needs a labeler",
		},
		"bad date": {
			cases: []eval.Case{func() eval.Case { c := validCase(); c.Date = "04/11/2026"; return c }()},
			want:  "date must be",
		},
		"overlap across splits": {
			cases: []eval.Case{validCase(), func() eval.Case {
				c := validCase()
				c.ID = "case-copied"
				c.Split = eval.SplitTest
				return c
			}()},
			want: "duplicates a case in the",
		},
		"unknown component in metadata": {
			meta:  strings.Replace(validMeta, `"component": "assistant"`, `"component": "mystery"`, 1),
			cases: []eval.Case{validCase()},
			want:  "unknown component",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			meta := test.meta
			if meta == "" {
				meta = validMeta
			}
			lines := make([]string, 0, len(test.cases))
			for _, c := range test.cases {
				lines = append(lines, caseLine(t, c))
			}
			_, err := eval.LoadDataset(writeDataset(t, meta, lines...))
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
		})
	}
}

func TestLoadDatasetRejectsUnknownCaseFields(t *testing.T) {
	dir := writeDataset(t, validMeta,
		`{"id":"a","component":"assistant","input":{},"expected":{},"source":"s","labeler":"l","date":"2026-11-04","split":"dev","note":"typo"}`)
	_, err := eval.LoadDataset(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestLoadDatasetRejectsAnEmptyCaseFile(t *testing.T) {
	dir := writeDataset(t, validMeta)
	_, err := eval.LoadDataset(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no cases")
}
