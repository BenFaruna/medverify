package verdict

// Verdict is the closed set of outcomes shown for a scan. None of them says a
// medicine is safe, genuine, or authentic, and none of them says it is fake.
// Adding a value requires updating this package, the rules file, the OpenAPI
// enum, and the assistant policy.
type Verdict string

// Verdict values. The wording is deliberately restrained: "not found in
// records" is a statement about our dataset, not about the pack.
const (
	// ConsistentWithRecords means a registered product was found and the
	// printed details agree with it. It is not an authenticity claim.
	ConsistentWithRecords Verdict = "consistent_with_records"
	// MismatchFound means a registered product was found but at least one
	// printed field or safety check disagrees with the record.
	MismatchFound Verdict = "mismatch_found"
	// NotFoundInRecords means nothing in the local dataset matched. The
	// dataset can be incomplete or stale; this is not proof of a fake.
	NotFoundInRecords Verdict = "not_found_in_records"
	// Flagged means a high severity safety flag applies.
	Flagged Verdict = "flagged"
	// NeedsReview means the evidence is ambiguous, thin, or conflicting.
	NeedsReview Verdict = "needs_review"
	// Unreadable means there was not enough readable data to judge.
	Unreadable Verdict = "unreadable"
)

// Verdicts lists every verdict in ascending order of concern. The rules file
// must define a score band for each, in this order.
var Verdicts = []Verdict{
	ConsistentWithRecords,
	NeedsReview,
	NotFoundInRecords,
	MismatchFound,
	Flagged,
	Unreadable,
}

// Valid reports whether the verdict is one this build understands.
func (v Verdict) Valid() bool {
	for _, known := range Verdicts {
		if v == known {
			return true
		}
	}
	return false
}

// Confidence is the coarse certainty shown with a verdict. It is never a
// percentage and never a probability.
type Confidence string

// Confidence levels.
const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

// Valid reports whether the level is known.
func (c Confidence) Valid() bool {
	return c == ConfidenceLow || c == ConfidenceMedium || c == ConfidenceHigh
}

// rank orders confidence levels so the weaker of two signals can be chosen.
func (c Confidence) rank() int {
	switch c {
	case ConfidenceHigh:
		return 3
	case ConfidenceMedium:
		return 2
	case ConfidenceLow:
		return 1
	default:
		return 0
	}
}

func weaker(a, b Confidence) Confidence {
	if a.rank() <= b.rank() {
		return a
	}
	return b
}

// Reason is one stable, translatable explanation for a verdict. Code is the
// key in the reviewed wording catalog; Params carry data for its placeholders.
// Clients must not parse or display Code as-is.
type Reason struct {
	Code   string            `json:"code"`
	Field  string            `json:"field,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// Result is the stored verdict output. Score is an internal 0-100 concern
// ranking for sorting and analytics, never a probability. RulesVersion names
// the file that produced the result so a past answer stays auditable.
type Result struct {
	Verdict      Verdict    `json:"value"`
	Score        int        `json:"score"`
	Confidence   Confidence `json:"confidence"`
	Reasons      []Reason   `json:"reasons"`
	RulesVersion string     `json:"rules_version"`
}
