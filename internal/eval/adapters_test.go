package eval_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/benfaruna/medverify/internal/eval"
	"github.com/benfaruna/medverify/internal/rag"
	"github.com/benfaruna/medverify/internal/vision"
)

func TestExtractionReportMapsFieldsAndFabrication(t *testing.T) {
	source := vision.EvalReport{
		Cases: 3,
		Fields: map[string]vision.FieldScore{
			"nrn": {Expected: 3, Exact: 2, Normalized: 3},
		},
		FabricatedNRN: 1,
		Failures:      []string{"pack-2: fabricated nrn 00-0000"},
	}
	report := eval.ExtractionReport("extraction-fixture", "2026-11", "hash", "vision-model", "v1", time.Now(), source)

	assert.Equal(t, eval.ComponentExtraction, report.Component)
	assert.Equal(t, 3, report.Cases)
	assert.Equal(t, 1, report.Metrics["fabricated_nrn"].Count)
	assert.InDelta(t, 2.0/3.0, report.Metrics["field.nrn.exact"].Value, 1e-9)
	assert.InDelta(t, 1.0, report.Metrics["field.nrn.normalized"].Value, 1e-9)
	assert.Len(t, report.Failures, 1)
}

func TestRetrievalReportMapsRecallAndSections(t *testing.T) {
	source := rag.EvalReport{
		Cases: 4, Answerable: 3, AnswerableHits: 2, Unanswerable: 1, NoAnswerCorrect: 1,
		PerSection: map[string]rag.SectionScore{
			"4.2": {Cases: 1, Hits: 1},
			"4.4": {Cases: 2, Hits: 1},
		},
		Misses: []rag.EvalMiss{
			{ID: "warning-2", Kind: "recall", Wanted: "4.4"},
		},
	}
	report := eval.RetrievalReport("retrieval-fixture", "v1", "hash", "fake-1024", time.Now(), source)

	assert.Equal(t, eval.ComponentRetrieval, report.Component)
	assert.InDelta(t, 2.0/3.0, report.Metrics["recall_at_k"].Value, 1e-9)
	assert.InDelta(t, 1.0, report.Metrics["correct_no_answer"].Value, 1e-9)
	assert.InDelta(t, 1.0, report.Metrics["section.4.2.recall"].Value, 1e-9)
	assert.InDelta(t, 0.5, report.Metrics["section.4.4.recall"].Value, 1e-9)
	assert.Len(t, report.Failures, 1)
}
