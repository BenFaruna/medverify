package verdict

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ReasonCodes is the closed set of explanation codes the evaluator can emit.
// Every code must have reviewed text in every supported language.
const (
	ReasonLabelConsistent       = "label_consistent"
	ReasonFieldMismatch         = "field_mismatch"
	ReasonUnknownFields         = "unknown_fields"
	ReasonLabelExpired          = "label_expired"
	ReasonNotInDataset          = "not_in_dataset"
	ReasonDatasetEmpty          = "dataset_empty"
	ReasonCandidateOnly         = "candidate_only"
	ReasonAmbiguousCandidates   = "ambiguous_candidates"
	ReasonInsufficientLabelData = "insufficient_label_data"
	ReasonLowConfidence         = "low_confidence"
	ReasonSafetyFlag            = "safety_flag"
	ReasonOutcomeUnrecognized   = "outcome_unrecognized"
)

// ReasonCodes lists every reason code in display priority order.
var ReasonCodes = []string{
	ReasonInsufficientLabelData,
	ReasonLowConfidence,
	ReasonSafetyFlag,
	ReasonFieldMismatch,
	ReasonLabelExpired,
	ReasonNotInDataset,
	ReasonDatasetEmpty,
	ReasonCandidateOnly,
	ReasonAmbiguousCandidates,
	ReasonOutcomeUnrecognized,
	ReasonUnknownFields,
	ReasonLabelConsistent,
}

// CatalogVersion identifies the reviewed wording file. The language and version
// belong with the result so a client can pick a translation deliberately.
const CatalogVersion = "reasons-v1"

//go:embed reasons/*.yaml
var reasonFiles embed.FS

var placeholderPattern = regexp.MustCompile(`\{([a-z_]+)\}`)

// Catalog is a reviewed set of verdict reason templates for one language.
type Catalog struct {
	Version   string            `yaml:"version"`
	Language  string            `yaml:"language"`
	Templates map[string]string `yaml:"templates"`
}

// LoadCatalog reads a reason catalog. An empty path loads the embedded English
// file. Every code must be covered so a new reason cannot ship silent.
func LoadCatalog(path string) (Catalog, error) {
	var data []byte
	var err error
	if path == "" {
		data, err = reasonFiles.ReadFile("reasons/en.yaml")
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return Catalog{}, fmt.Errorf("verdict: read reason catalog: %w", err)
	}

	var catalog Catalog
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("verdict: decode reason catalog: %w", err)
	}
	if catalog.Version == "" || catalog.Language == "" {
		return Catalog{}, fmt.Errorf("verdict: reason catalog version and language are required")
	}
	for _, code := range ReasonCodes {
		template := strings.TrimSpace(catalog.Templates[code])
		if template == "" {
			return Catalog{}, fmt.Errorf("verdict: reason catalog is missing %s", code)
		}
		if strings.Contains(template, "{") && !placeholderPattern.MatchString(template) {
			return Catalog{}, fmt.Errorf("verdict: reason template %s has a malformed placeholder", code)
		}
	}
	for code, template := range catalog.Templates {
		if !knownReasonCode(code) {
			return Catalog{}, fmt.Errorf("verdict: reason template %q has no code", code)
		}
		if strings.TrimSpace(template) == "" {
			return Catalog{}, fmt.Errorf("verdict: reason template %q is empty", code)
		}
	}
	return catalog, nil
}

// Render fills a template from reason parameters. A missing value is an error:
// it is better to show no sentence than one with a hole in it.
func (c Catalog) Render(reason Reason) (string, error) {
	template, ok := c.Templates[reason.Code]
	if !ok {
		return "", fmt.Errorf("verdict: no template for reason %q", reason.Code)
	}

	var missing []string
	rendered := placeholderPattern.ReplaceAllStringFunc(template, func(token string) string {
		key := strings.TrimSuffix(strings.TrimPrefix(token, "{"), "}")
		value, ok := reason.Params[key]
		if !ok || value == "" {
			missing = append(missing, key)
			return token
		}
		return value
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("verdict: reason %s is missing %s", reason.Code, strings.Join(missing, ", "))
	}
	return rendered, nil
}

func knownReasonCode(code string) bool {
	for _, known := range ReasonCodes {
		if code == known {
			return true
		}
	}
	return false
}

// reasonPriority gives every code a stable rank so ordering never depends on
// map iteration or on the order rules fired.
func reasonPriority(code string) int {
	for index, known := range ReasonCodes {
		if code == known {
			return index
		}
	}
	return len(ReasonCodes)
}

func sortReasons(reasons []Reason) {
	sort.SliceStable(reasons, func(i, j int) bool {
		left, right := reasons[i], reasons[j]
		if reasonPriority(left.Code) != reasonPriority(right.Code) {
			return reasonPriority(left.Code) < reasonPriority(right.Code)
		}
		if left.Field != right.Field {
			return left.Field < right.Field
		}
		return paramsKey(left.Params) < paramsKey(right.Params)
	})
}

func paramsKey(params map[string]string) string {
	if len(params) == 0 {
		return ""
	}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteString("=")
		builder.WriteString(params[key])
		builder.WriteString(";")
	}
	return builder.String()
}

func dedupeReasons(reasons []Reason) []Reason {
	seen := make(map[string]struct{}, len(reasons))
	unique := make([]Reason, 0, len(reasons))
	for _, reason := range reasons {
		key := reason.Code + "|" + reason.Field + "|" + paramsKey(reason.Params)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, reason)
	}
	return unique
}
