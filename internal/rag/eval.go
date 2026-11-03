package rag

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// EvalCase is one labelled retrieval question. An empty Section means the
// question is not answerable from this product's leaflet; Unanswerable makes
// that explicit so a missing label cannot be mistaken for one.
type EvalCase struct {
	ID           string `yaml:"id"`
	Product      string `yaml:"product"`
	Question     string `yaml:"question"`
	Section      string `yaml:"section,omitempty"`
	Unanswerable bool   `yaml:"unanswerable,omitempty"`
	Language     string `yaml:"language,omitempty"`
}

// EvalSet is a versioned, human-labelled retrieval evaluation set.
type EvalSet struct {
	Version string     `yaml:"version"`
	Cases   []EvalCase `yaml:"cases"`
}

// LoadEvalSet reads and validates an evaluation file. Unknown fields are
// rejected so a typo cannot silently turn a labelled case into an unanswerable
// one.
func LoadEvalSet(path string) (EvalSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return EvalSet{}, fmt.Errorf("rag: read eval set: %w", err)
	}
	var set EvalSet
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&set); err != nil {
		return EvalSet{}, fmt.Errorf("rag: decode eval set: %w", err)
	}
	if set.Version == "" {
		return EvalSet{}, fmt.Errorf("rag: eval set has no version")
	}
	seen := make(map[string]bool, len(set.Cases))
	for i, c := range set.Cases {
		if c.ID == "" || c.Product == "" || c.Question == "" {
			return EvalSet{}, fmt.Errorf("rag: eval case %d needs id, product, and question", i+1)
		}
		if seen[c.ID] {
			return EvalSet{}, fmt.Errorf("rag: duplicate eval case id %q", c.ID)
		}
		seen[c.ID] = true
		if c.Unanswerable && c.Section != "" {
			return EvalSet{}, fmt.Errorf("rag: eval case %q is unanswerable but names section %q", c.ID, c.Section)
		}
		if !c.Unanswerable && c.Section == "" {
			return EvalSet{}, fmt.Errorf("rag: eval case %q needs a section or unanswerable: true", c.ID)
		}
	}
	return set, nil
}

// EvalOptions controls scoring.
type EvalOptions struct {
	// K is the number of passages retrieved per question. Default 5.
	K int
	// MinVectorScore is the similarity below which a hit counts as no answer.
	// Default 0.35.
	MinVectorScore float64
}

// EvalMiss records one failing case.
type EvalMiss struct {
	ID      string
	Kind    string // "recall" or "no_answer"
	Wanted  string
	Got     []string
	Product int64
}

// EvalReport is the outcome of one evaluation run.
type EvalReport struct {
	Cases            int
	Answerable       int
	Unanswerable     int
	AnswerableHits   int
	NoAnswerCorrect  int
	Recall           float64
	NoAnswerAccuracy float64
	PerSection       map[string]SectionScore
	Misses           []EvalMiss
}

// SectionScore is recall for one expected section code.
type SectionScore struct {
	Cases int
	Hits  int
}

// ProductResolver looks up the product an evaluation case refers to.
type ProductResolver interface {
	ResolveProduct(ctx context.Context, reference string) (int64, error)
}

// EvalSearcher is the retrieval half needed for scoring.
type EvalSearcher interface {
	SearchVector(ctx context.Context, model string, req SearchRequest, queryVector []float32) ([]Hit, error)
}

// Evaluate runs every case and scores recall at K for answerable questions and
// correct-no-answer for unanswerable ones. It never writes to the database and
// never mutates the corpus.
func Evaluate(ctx context.Context, searcher EvalSearcher, embedder Embedder, resolver ProductResolver, set EvalSet, opts EvalOptions) (EvalReport, error) {
	if searcher == nil || embedder == nil || resolver == nil {
		return EvalReport{}, fmt.Errorf("rag: eval needs a searcher, an embedder, and a product resolver")
	}
	if opts.K <= 0 {
		opts.K = DefaultSearchLimit
	}
	if opts.MinVectorScore <= 0 {
		opts.MinVectorScore = 0.35
	}

	report := EvalReport{Cases: len(set.Cases), PerSection: map[string]SectionScore{}}
	for _, c := range set.Cases {
		productID, err := resolver.ResolveProduct(ctx, c.Product)
		if err != nil {
			return EvalReport{}, fmt.Errorf("rag: eval case %s: %w", c.ID, err)
		}
		vector, err := embedder.EmbedQuery(ctx, c.Question)
		if err != nil {
			return EvalReport{}, fmt.Errorf("rag: eval case %s: embed question: %w", c.ID, err)
		}
		hits, err := searcher.SearchVector(ctx, embedder.Model(), SearchRequest{
			ProductID: productID,
			Query:     c.Question,
			Limit:     opts.K,
		}, vector)
		if err != nil {
			return EvalReport{}, fmt.Errorf("rag: eval case %s: %w", c.ID, err)
		}

		if c.Unanswerable {
			report.Unanswerable++
			if noAnswer(hits, opts.MinVectorScore) {
				report.NoAnswerCorrect++
				continue
			}
			report.Misses = append(report.Misses, EvalMiss{
				ID: c.ID, Kind: "no_answer", Got: sections(hits), Product: productID,
			})
			continue
		}

		report.Answerable++
		section := report.PerSection[c.Section]
		section.Cases++
		if containsSection(hits, c.Section) {
			report.AnswerableHits++
			section.Hits++
			report.PerSection[c.Section] = section
			continue
		}
		report.PerSection[c.Section] = section
		report.Misses = append(report.Misses, EvalMiss{
			ID: c.ID, Kind: "recall", Wanted: c.Section, Got: sections(hits), Product: productID,
		})
	}

	if report.Answerable > 0 {
		report.Recall = float64(report.AnswerableHits) / float64(report.Answerable)
	}
	if report.Unanswerable > 0 {
		report.NoAnswerAccuracy = float64(report.NoAnswerCorrect) / float64(report.Unanswerable)
	}
	sectionsSorted := make([]string, 0, len(report.PerSection))
	for code := range report.PerSection {
		sectionsSorted = append(sectionsSorted, code)
	}
	sort.Strings(sectionsSorted)
	return report, nil
}

func containsSection(hits []Hit, code string) bool {
	for _, hit := range hits {
		if hit.SectionCode == code {
			return true
		}
	}
	return false
}

// noAnswer treats empty retrieval and hits below the similarity floor as a
// correct refusal to answer.
func noAnswer(hits []Hit, minScore float64) bool {
	for _, hit := range hits {
		if hit.VectorScore >= minScore {
			return false
		}
	}
	return true
}

func sections(hits []Hit) []string {
	out := make([]string, 0, len(hits))
	for _, hit := range hits {
		code := hit.SectionCode
		if code == "" {
			code = "(no section)"
		}
		out = append(out, code)
	}
	return out
}

// Summary renders a short report for the CLI.
func (r EvalReport) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "cases=%d answerable=%d unanswerable=%d\n", r.Cases, r.Answerable, r.Unanswerable)
	fmt.Fprintf(&b, "recall@k=%.1f%% (%d/%d) no-answer accuracy=%.1f%% (%d/%d)\n",
		r.Recall*100, r.AnswerableHits, r.Answerable,
		r.NoAnswerAccuracy*100, r.NoAnswerCorrect, r.Unanswerable)
	codes := make([]string, 0, len(r.PerSection))
	for code := range r.PerSection {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		score := r.PerSection[code]
		fmt.Fprintf(&b, "  section %s: %d/%d\n", code, score.Hits, score.Cases)
	}
	for _, miss := range r.Misses {
		if miss.Kind == "no_answer" {
			fmt.Fprintf(&b, "  MISS %s: expected no answer, got sections %v\n", miss.ID, miss.Got)
			continue
		}
		fmt.Fprintf(&b, "  MISS %s: wanted section %s, got %v\n", miss.ID, miss.Wanted, miss.Got)
	}
	return b.String()
}
