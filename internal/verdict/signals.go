package verdict

import (
	"slices"

	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/verify"
	"github.com/benfaruna/medverify/internal/vision"
)

// Extraction is the label-readability part of the signals: what the vision
// step actually read. MissingFields keeps the field names the extractor could
// not trust, so an unreadable verdict can say what was missing without
// guessing.
type Extraction struct {
	OverallConfidence float64  `json:"overall_confidence"`
	Identified        bool     `json:"identified"`
	MissingFields     []string `json:"missing_fields,omitempty"`
}

// Signals is the complete, normalized input to Evaluate. It stores only what
// the rules need, so a scan can be replayed under a new rules version without
// the database, the clock, or the original images.
type Signals struct {
	Outcome         verify.Outcome                 `json:"outcome"`
	Fields          map[string]verify.FieldOutcome `json:"fields,omitempty"`
	FieldReasons    map[string][]string            `json:"field_reasons,omitempty"`
	Flags           []safety.Flag                  `json:"flags,omitempty"`
	Extraction      Extraction                     `json:"extraction"`
	DatasetProducts int                            `json:"dataset_products"`
	CandidateCount  int                            `json:"candidate_count,omitempty"`
	LabelExpired    bool                           `json:"label_expired,omitempty"`
}

// SignalsFrom normalizes a match result, safety flags, and label data into the
// pure input Evaluate consumes. It performs no I/O.
func SignalsFrom(match verify.MatchResult, flags []safety.Flag, label vision.LabelData) Signals {
	fields := make(map[string]verify.FieldOutcome, len(match.Fields))
	reasons := make(map[string][]string, len(match.Fields))
	for name, comparison := range match.Fields {
		fields[name] = comparison.Outcome
		if len(comparison.Reasons) > 0 {
			reasons[name] = append([]string(nil), comparison.Reasons...)
		}
	}

	expired := false
	if comparison, ok := match.Fields[verify.FieldExpiry]; ok {
		expired = comparison.Outcome == verify.Disagree &&
			slices.Contains(comparison.Reasons, verify.ReasonExpiredOnLabel)
	}

	missing := append([]string(nil), label.Unreadable...)
	slices.Sort(missing)

	return Signals{
		Outcome:         match.Outcome,
		Fields:          fields,
		FieldReasons:    reasons,
		Flags:           append([]safety.Flag(nil), flags...),
		DatasetProducts: match.Dataset.Products,
		CandidateCount:  len(match.Candidates),
		LabelExpired:    expired,
		Extraction: Extraction{
			OverallConfidence: label.OverallConfidence(),
			Identified:        label.Identified(),
			MissingFields:     missing,
		},
	}
}

// SignalsFromUnreadable builds signals for a scan whose images could not be
// read at all. It exists so the unreadable path still stores an auditable
// verdict rather than an empty one.
func SignalsFromUnreadable(missing []string) Signals {
	sorted := append([]string(nil), missing...)
	slices.Sort(sorted)
	return Signals{
		Outcome: verify.UnreadableInput,
		Extraction: Extraction{
			Identified:    false,
			MissingFields: sorted,
		},
	}
}
