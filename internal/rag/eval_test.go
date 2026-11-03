package rag_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/rag"
)

type fakeResolver map[string]int64

func (f fakeResolver) ResolveProduct(_ context.Context, reference string) (int64, error) {
	id, ok := f[reference]
	if !ok {
		return 0, assert.AnError
	}
	return id, nil
}

type fakeSearcher map[string][]rag.Hit

func (f fakeSearcher) SearchVector(_ context.Context, _ string, req rag.SearchRequest, _ []float32) ([]rag.Hit, error) {
	return f[req.Query], nil
}

func TestEvaluateScoresRecallAndNoAnswer(t *testing.T) {
	set := rag.EvalSet{
		Version: "test",
		Cases: []rag.EvalCase{
			{ID: "dose", Product: "A", Question: "dose?", Section: "4.2"},
			{ID: "warning", Product: "A", Question: "warning?", Section: "4.4"},
			{ID: "storage", Product: "A", Question: "storage?", Section: "6.4"},
			{ID: "price", Product: "A", Question: "price?", Unanswerable: true},
			{ID: "baby", Product: "A", Question: "baby?", Unanswerable: true},
		},
	}
	searcher := fakeSearcher{
		"dose?":    {{SectionCode: "4.2", VectorScore: 0.8}},
		"warning?": {{SectionCode: "4.1", VectorScore: 0.7}},
		"storage?": {{SectionCode: "6.4", VectorScore: 0.6}},
		"price?":   {{SectionCode: "4.2", VectorScore: 0.1}},
		"baby?":    {{SectionCode: "4.4", VectorScore: 0.9}},
	}
	embedder := rag.NewFakeEmbedder(rag.VectorDimension, "fake-1024")

	report, err := rag.Evaluate(context.Background(), searcher, embedder, fakeResolver{"A": 1}, set, rag.EvalOptions{})
	require.NoError(t, err)

	assert.Equal(t, 5, report.Cases)
	assert.Equal(t, 3, report.Answerable)
	assert.Equal(t, 2, report.AnswerableHits)
	assert.InDelta(t, 2.0/3.0, report.Recall, 1e-9)
	assert.Equal(t, 1, report.NoAnswerCorrect, "a low-similarity hit counts as no answer")
	assert.InDelta(t, 0.5, report.NoAnswerAccuracy, 1e-9)
	assert.Equal(t, rag.SectionScore{Cases: 1, Hits: 1}, report.PerSection["4.2"])
	assert.Equal(t, rag.SectionScore{Cases: 1, Hits: 1}, report.PerSection["6.4"])
	assert.Equal(t, rag.SectionScore{Cases: 1, Hits: 0}, report.PerSection["4.4"])
	require.Len(t, report.Misses, 2)
	assert.Equal(t, "warning", report.Misses[0].ID)
	assert.Equal(t, "recall", report.Misses[0].Kind)
	assert.Equal(t, "baby", report.Misses[1].ID)
	assert.Equal(t, "no_answer", report.Misses[1].Kind)
	assert.Contains(t, report.Summary(), "MISS warning")
}

func TestEvaluateFailsOnAnUnknownProduct(t *testing.T) {
	set := rag.EvalSet{Version: "test", Cases: []rag.EvalCase{
		{ID: "dose", Product: "missing", Question: "dose?", Section: "4.2"},
	}}
	_, err := rag.Evaluate(context.Background(), fakeSearcher{}, rag.NewFakeEmbedder(rag.VectorDimension, "fake"), fakeResolver{}, set, rag.EvalOptions{})
	require.Error(t, err)
}

func TestLoadEvalSetValidatesCases(t *testing.T) {
	set, err := rag.LoadEvalSet("../../testdata/rag/eval-draft.yaml")
	require.NoError(t, err)
	assert.Equal(t, "rag-eval-draft-v1", set.Version)
	assert.Len(t, set.Cases, 5)
	assert.True(t, set.Cases[3].Unanswerable)

	tests := map[string]string{
		"missing section": `
version: v1
cases:
  - {id: a, product: "1", question: "q"}
`,
		"duplicate id": `
version: v1
cases:
  - {id: a, product: "1", question: "q", section: "4.2"}
  - {id: a, product: "1", question: "q2", section: "4.2"}
`,
		"contradictory case": `
version: v1
cases:
  - {id: a, product: "1", question: "q", section: "4.2", unanswerable: true}
`,
		"unknown field": `
version: v1
cases:
  - {id: a, product: "1", question: "q", section: "4.2", expected: "x"}
`,
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "eval.yaml")
			require.NoError(t, os.WriteFile(path, []byte(document), 0o600))
			_, err := rag.LoadEvalSet(path)
			require.Error(t, err)
		})
	}
}
