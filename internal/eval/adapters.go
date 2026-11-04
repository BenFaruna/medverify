package eval

import (
	"time"

	"github.com/benfaruna/medverify/internal/rag"
	"github.com/benfaruna/medverify/internal/vision"
)

// ExtractionReport adapts the phase 04 extraction scorer to the shared report
// shape. Field names become `field.<name>.exact` and `field.<name>.normalized`
// so a gate file can target one field without changing code.
func ExtractionReport(datasetName, datasetVersion, datasetSHA, model, promptVersion string, started time.Time, source vision.EvalReport) Report {
	report := Report{
		Component:      ComponentExtraction,
		Dataset:        datasetName,
		DatasetVersion: datasetVersion,
		DatasetSHA256:  datasetSHA,
		Model:          model,
		PromptVersion:  promptVersion,
		StartedAt:      started.UTC(),
		DurationMS:     time.Since(started).Milliseconds(),
		Cases:          source.Cases,
	}
	report.CountMetric("fabricated_nrn", source.FabricatedNRN)
	for field, score := range source.Fields {
		report.RateMetric("field."+field+".exact", Tally{Hits: score.Exact, Total: score.Expected})
		report.RateMetric("field."+field+".normalized", Tally{Hits: score.Normalized, Total: score.Expected})
	}
	for _, failure := range source.Failures {
		report.AddFailure("", failure)
	}
	return report
}

// RetrievalReport adapts the phase 08 retrieval scorer to the shared report
// shape. Per-section recall is reported separately so a gate can require, for
// example, that the warnings section does not collapse while overall recall
// stays flat.
func RetrievalReport(datasetName, datasetVersion, datasetSHA, model string, started time.Time, source rag.EvalReport) Report {
	report := Report{
		Component:      ComponentRetrieval,
		Dataset:        datasetName,
		DatasetVersion: datasetVersion,
		DatasetSHA256:  datasetSHA,
		Model:          model,
		PromptVersion:  "retrieval-v1",
		StartedAt:      started.UTC(),
		DurationMS:     time.Since(started).Milliseconds(),
		Cases:          source.Cases,
	}
	report.RateMetric("recall_at_k", Tally{Hits: source.AnswerableHits, Total: source.Answerable})
	report.RateMetric("correct_no_answer", Tally{Hits: source.NoAnswerCorrect, Total: source.Unanswerable})
	for section, score := range source.PerSection {
		report.RateMetric("section."+section+".recall", Tally{Hits: score.Hits, Total: score.Cases})
	}
	for _, miss := range source.Misses {
		reason := "expected section " + miss.Wanted
		if miss.Kind == "no_answer" {
			reason = "expected no answer"
		}
		report.AddFailure(miss.ID, reason)
	}
	return report
}
