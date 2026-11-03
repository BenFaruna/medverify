package verdict

import (
	"bytes"
	"embed"
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

// ConfigVersion is the rules file shipped with this build. Every stored result
// records the version it used; a tuning change must bump it.
const ConfigVersion = "verdict-v1"

//go:embed config/*.yaml
var configFiles embed.FS

// Band is the closed score range for one verdict. Bands do not overlap and are
// ordered, so a higher-concern verdict always scores at least as high as a
// lower one. That keeps sorting by score consistent with the verdict shown.
type Band struct {
	Min int `yaml:"min"`
	Max int `yaml:"max"`
}

// Gate holds the readability threshold. Below it, no comparison is trusted.
type Gate struct {
	MinExtractionConfidence float64 `yaml:"min_extraction_confidence"`
}

// Weights are the integer concern points added inside a verdict's band. They
// affect ordering and analytics, never the verdict itself.
type Weights struct {
	FieldMismatch int `yaml:"field_mismatch"`
	UnknownField  int `yaml:"unknown_field"`
	LabelExpired  int `yaml:"label_expired"`
	MediumFlag    int `yaml:"medium_flag"`
	LowFlag       int `yaml:"low_flag"`
	ThinMatch     int `yaml:"thin_match"`
}

// ConfidenceThresholds turn numbers into the coarse levels shown to users.
type ConfidenceThresholds struct {
	MediumExtraction float64 `yaml:"medium_extraction"`
	HighExtraction   float64 `yaml:"high_extraction"`
}

// Limits caps the output size. Display never grows without bound.
type Limits struct {
	MaxReasons int `yaml:"max_reasons"`
}

// Rules is the complete, versioned scoring policy. It is loaded once at startup
// and passed to the pure evaluator.
type Rules struct {
	Version    string               `yaml:"version"`
	Gate       Gate                 `yaml:"gate"`
	Bands      map[Verdict]Band     `yaml:"bands"`
	Weights    Weights              `yaml:"weights"`
	Confidence ConfidenceThresholds `yaml:"confidence"`
	Limits     Limits               `yaml:"limits"`
}

// LoadRules reads a rules file. An empty path loads the embedded default.
// Unknown YAML fields are rejected so a typo cannot silently leave a weight or
// a threshold at zero.
func LoadRules(path string) (Rules, error) {
	var data []byte
	var err error
	if path == "" {
		data, err = configFiles.ReadFile("config/" + ConfigVersion + ".yaml")
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return Rules{}, fmt.Errorf("verdict: read rules: %w", err)
	}
	return ParseRules(data)
}

// ParseRules decodes and validates a rules document. It is exported for tests
// and for the replay command's own overrides.
func ParseRules(data []byte) (Rules, error) {
	var rules Rules
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&rules); err != nil {
		return Rules{}, fmt.Errorf("verdict: decode rules: %w", err)
	}
	if err := rules.Validate(); err != nil {
		return Rules{}, err
	}
	return rules, nil
}

// Validate checks the rules for values that would produce a misleading score
// or a missing verdict. It runs at load time, never per scan.
func (r Rules) Validate() error {
	if r.Version == "" {
		return fmt.Errorf("verdict: rules version is required")
	}
	if r.Gate.MinExtractionConfidence <= 0 || r.Gate.MinExtractionConfidence >= 1 {
		return fmt.Errorf("verdict: min_extraction_confidence must be in (0, 1)")
	}
	if r.Confidence.MediumExtraction <= 0 || r.Confidence.MediumExtraction >= 1 {
		return fmt.Errorf("verdict: medium_extraction must be in (0, 1)")
	}
	if r.Confidence.HighExtraction <= 0 || r.Confidence.HighExtraction >= 1 {
		return fmt.Errorf("verdict: high_extraction must be in (0, 1)")
	}
	if r.Confidence.HighExtraction <= r.Confidence.MediumExtraction {
		return fmt.Errorf("verdict: high_extraction must be greater than medium_extraction")
	}
	if r.Confidence.MediumExtraction < r.Gate.MinExtractionConfidence {
		return fmt.Errorf("verdict: medium_extraction must not be below min_extraction_confidence")
	}

	previousMax := -1
	for _, value := range Verdicts {
		band, ok := r.Bands[value]
		if !ok {
			return fmt.Errorf("verdict: score band is missing for %s", value)
		}
		if band.Min < 0 || band.Max > 100 {
			return fmt.Errorf("verdict: score band for %s must lie within 0-100", value)
		}
		if band.Min > band.Max {
			return fmt.Errorf("verdict: score band for %s has min above max", value)
		}
		if band.Min <= previousMax {
			return fmt.Errorf("verdict: score bands must not overlap and must follow verdict order")
		}
		previousMax = band.Max
	}
	for value := range r.Bands {
		if !value.Valid() {
			return fmt.Errorf("verdict: score band for unknown verdict %q", value)
		}
	}

	for name, weight := range map[string]int{
		"field_mismatch": r.Weights.FieldMismatch,
		"unknown_field":  r.Weights.UnknownField,
		"label_expired":  r.Weights.LabelExpired,
		"medium_flag":    r.Weights.MediumFlag,
		"low_flag":       r.Weights.LowFlag,
		"thin_match":     r.Weights.ThinMatch,
	} {
		if weight < 0 || weight > 40 {
			return fmt.Errorf("verdict: weight %s must be between 0 and 40", name)
		}
	}
	if r.Limits.MaxReasons <= 0 || r.Limits.MaxReasons > 50 {
		return fmt.Errorf("verdict: max_reasons must be in (0, 50]")
	}
	return nil
}

// Band returns the score range for a verdict. Rules are validated at load, so
// a missing band falls back to a full range rather than panicking in a job.
func (r Rules) band(value Verdict) Band {
	if band, ok := r.Bands[value]; ok {
		return band
	}
	return Band{Min: 0, Max: 100}
}
