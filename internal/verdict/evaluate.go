package verdict

import (
	"sort"
	"strings"

	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/verify"
)

// Evaluate turns normalized signals into a verdict, a concern score, a coarse
// confidence, and ordered reasons. It is pure: no I/O, no clock, no
// randomness, no database. The same signals and rules always give the same
// answer, which is what makes results auditable and replayable.
func Evaluate(signals Signals, rules Rules) Result {
	reasons := make([]Reason, 0, rules.Limits.MaxReasons)
	extraction := signals.Extraction

	// Gate first: a bad read never becomes a comparison.
	if !extraction.Identified || extraction.OverallConfidence < rules.Gate.MinExtractionConfidence {
		if !extraction.Identified {
			reasons = append(reasons, Reason{Code: ReasonInsufficientLabelData})
		}
		if extraction.Identified && extraction.OverallConfidence < rules.Gate.MinExtractionConfidence {
			reasons = append(reasons, Reason{Code: ReasonLowConfidence})
		}
		if missing := missingFields(signals); len(missing) > 0 {
			reasons = append(reasons, unknownFieldsReason(missing))
		}
		return buildResult(Unreadable, ConfidenceLow, reasons, 0, rules)
	}

	// A matcher that could not use the label is also unreadable, whatever the
	// extraction confidence says. Treating it as any other outcome would let a
	// high severity flag score below it.
	if signals.Outcome == verify.UnreadableInput {
		reasons = append(reasons, Reason{Code: ReasonInsufficientLabelData})
		if missing := missingFields(signals); len(missing) > 0 {
			reasons = append(reasons, unknownFieldsReason(missing))
		}
		return buildResult(Unreadable, ConfidenceLow, reasons, 0, rules)
	}

	highFlags, mediumFlags, lowFlags := countFlagSeverities(signals.Flags)
	points := concernPoints(signals, rules)

	// Hard rule: a high severity safety flag wins over every other signal.
	if highFlags > 0 {
		reasons = append(reasons, Reason{Code: ReasonSafetyFlag})
		reasons = appendFieldReasons(reasons, signals.Fields)
		if signals.LabelExpired {
			reasons = append(reasons, Reason{Code: ReasonLabelExpired})
		}
		if signals.Outcome == verify.NotFoundInDataset {
			reasons = append(reasons, Reason{Code: ReasonNotInDataset})
			if signals.DatasetProducts <= 0 {
				reasons = append(reasons, Reason{Code: ReasonDatasetEmpty})
			}
		}
		return buildResult(Flagged, confidenceFor(signals, rules), reasons, points, rules)
	}

	var value Verdict
	switch signals.Outcome {
	case verify.MatchedConsistent:
		switch {
		case mediumFlags > 0:
			// A medium flag is a label-versus-record disagreement, so the
			// verdict is a mismatch rather than a clean match.
			value = MismatchFound
		case lowFlags > 0 || signals.LabelExpired:
			value = NeedsReview
		default:
			value = ConsistentWithRecords
		}
		reasons = append(reasons, Reason{Code: ReasonLabelConsistent})
	case verify.MatchedWithMismatch:
		value = MismatchFound
	case verify.NotFoundInDataset:
		value = NotFoundInRecords
		reasons = append(reasons, Reason{Code: ReasonNotInDataset})
		if signals.DatasetProducts <= 0 {
			reasons = append(reasons, Reason{Code: ReasonDatasetEmpty})
		}
	case verify.CandidateOnly:
		value = NeedsReview
		reasons = append(reasons, Reason{Code: ReasonCandidateOnly})
	case verify.Ambiguous:
		value = NeedsReview
		reasons = append(reasons, Reason{Code: ReasonAmbiguousCandidates})
	case verify.UnreadableInput:
		// Handled before scoring; kept for completeness if the gate changes.
		value = Unreadable
		reasons = append(reasons, Reason{Code: ReasonInsufficientLabelData})
	default:
		value = NeedsReview
		reasons = append(reasons, Reason{Code: ReasonOutcomeUnrecognized})
	}

	if value == MismatchFound || value == ConsistentWithRecords || value == NeedsReview {
		reasons = appendFieldReasons(reasons, signals.Fields)
	}
	if signals.LabelExpired {
		reasons = append(reasons, Reason{Code: ReasonLabelExpired})
	}
	if len(signals.Flags) > 0 {
		reasons = append(reasons, Reason{Code: ReasonSafetyFlag})
	}
	if missing := missingFields(signals); len(missing) > 0 {
		reasons = append(reasons, unknownFieldsReason(missing))
	}

	return buildResult(value, confidenceFor(signals, rules), reasons, points, rules)
}

// concernPoints accumulates the weights that order results inside a band.
func concernPoints(signals Signals, rules Rules) int {
	points := 0
	for _, outcome := range signals.Fields {
		switch outcome {
		case verify.Disagree:
			points += rules.Weights.FieldMismatch
		case verify.Unknown:
			points += rules.Weights.UnknownField
		}
	}
	if signals.LabelExpired {
		points += rules.Weights.LabelExpired
	}
	for _, flag := range signals.Flags {
		switch flag.Severity {
		case safety.SeverityMedium:
			points += rules.Weights.MediumFlag
		case safety.SeverityLow:
			points += rules.Weights.LowFlag
		}
	}
	switch signals.Outcome {
	case verify.CandidateOnly, verify.Ambiguous:
		points += rules.Weights.ThinMatch
	}
	return points
}

// confidenceFor takes the weaker of what the extractor read and what the match
// proved. Unknown fields do not enter this calculation, so a partial but strong
// match is not punished for fields the label simply lacks.
func confidenceFor(signals Signals, rules Rules) Confidence {
	level := ConfidenceLow
	switch {
	case signals.Extraction.OverallConfidence >= rules.Confidence.HighExtraction:
		level = ConfidenceHigh
	case signals.Extraction.OverallConfidence >= rules.Confidence.MediumExtraction:
		level = ConfidenceMedium
	}

	var matchCap Confidence
	switch signals.Outcome {
	case verify.MatchedConsistent, verify.MatchedWithMismatch:
		matchCap = ConfidenceHigh
	case verify.NotFoundInDataset:
		matchCap = ConfidenceMedium
	default:
		matchCap = ConfidenceLow
	}
	return weaker(level, matchCap)
}

func buildResult(value Verdict, confidence Confidence, reasons []Reason, points int, rules Rules) Result {
	reasons = dedupeReasons(reasons)
	sortReasons(reasons)
	if len(reasons) > rules.Limits.MaxReasons {
		reasons = reasons[:rules.Limits.MaxReasons]
	}

	band := rules.band(value)
	score := band.Min + points
	if score > band.Max {
		score = band.Max
	}
	if score < band.Min {
		score = band.Min
	}

	return Result{
		Verdict:      value,
		Score:        score,
		Confidence:   confidence,
		Reasons:      reasons,
		RulesVersion: rules.Version,
	}
}

func countFlagSeverities(flags []safety.Flag) (high, medium, low int) {
	for _, flag := range flags {
		switch flag.Severity {
		case safety.SeverityHigh:
			high++
		case safety.SeverityMedium:
			medium++
		case safety.SeverityLow:
			low++
		}
	}
	return high, medium, low
}

// appendFieldReasons adds one reason per disagreeing field, in field-name order
// so output is deterministic.
func appendFieldReasons(reasons []Reason, fields map[string]verify.FieldOutcome) []Reason {
	names := make([]string, 0, len(fields))
	for name, outcome := range fields {
		if outcome == verify.Disagree {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		reasons = append(reasons, Reason{
			Code:  ReasonFieldMismatch,
			Field: name,
			Params: map[string]string{
				"field": name,
			},
		})
	}
	return reasons
}

// missingFields is the union of fields the extractor flagged as unreadable and
// fields the matcher recorded as unknown. Both are evidence of partial data, so
// the reason must list them without duplication.
func missingFields(signals Signals) []string {
	seen := make(map[string]struct{}, len(signals.Extraction.MissingFields))
	for _, field := range signals.Extraction.MissingFields {
		if field != "" {
			seen[field] = struct{}{}
		}
	}
	for field, outcome := range signals.Fields {
		if outcome == verify.Unknown && field != "" {
			seen[field] = struct{}{}
		}
	}
	fields := make([]string, 0, len(seen))
	for field := range seen {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

func unknownFieldsReason(fields []string) Reason {
	sorted := append([]string(nil), fields...)
	sort.Strings(sorted)
	return Reason{
		Code: ReasonUnknownFields,
		Params: map[string]string{
			"fields": strings.Join(sorted, ", "),
		},
	}
}
