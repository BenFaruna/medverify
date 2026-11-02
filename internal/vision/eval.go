package vision

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/benfaruna/medverify/internal/registry"
)

// EvalIngredient is one expected ingredient in an eval case.
type EvalIngredient struct {
	Name     string `json:"name"`
	Strength string `json:"strength,omitempty"`
}

// EvalExpected is the hand-labelled ground truth for one pack. Empty fields
// mean the labeller could not read them; they are not scored.
type EvalExpected struct {
	NRN          string           `json:"nrn,omitempty"`
	ProductName  string           `json:"product_name,omitempty"`
	Manufacturer string           `json:"manufacturer,omitempty"`
	BatchNo      string           `json:"batch_no,omitempty"`
	MfgDate      string           `json:"mfg_date,omitempty"`
	ExpiryDate   string           `json:"expiry_date,omitempty"`
	Ingredients  []EvalIngredient `json:"ingredients,omitempty"`
}

// EvalCase is one pack photo set and its ground truth.
type EvalCase struct {
	ID       string       `json:"id"`
	Images   []string     `json:"images"`
	Language string       `json:"language,omitempty"`
	Expected EvalExpected `json:"expected"`
}

// EvalManifest is a versioned extraction eval set. Image paths are relative to
// the manifest file.
type EvalManifest struct {
	Name  string     `json:"name"`
	Cases []EvalCase `json:"cases"`
}

// LoadEvalManifest reads and minimally validates a manifest. It never downloads
// anything; missing image files are reported by the caller.
func LoadEvalManifest(path string) (EvalManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return EvalManifest{}, fmt.Errorf("vision: read eval manifest: %w", err)
	}
	var manifest EvalManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return EvalManifest{}, fmt.Errorf("vision: decode eval manifest: %w", err)
	}
	if len(manifest.Cases) == 0 {
		return EvalManifest{}, fmt.Errorf("vision: eval manifest has no cases")
	}
	seen := make(map[string]struct{}, len(manifest.Cases))
	for i, evalCase := range manifest.Cases {
		if strings.TrimSpace(evalCase.ID) == "" {
			return EvalManifest{}, fmt.Errorf("vision: eval case %d has no id", i)
		}
		if _, exists := seen[evalCase.ID]; exists {
			return EvalManifest{}, fmt.Errorf("vision: duplicate eval case id %q", evalCase.ID)
		}
		seen[evalCase.ID] = struct{}{}
		if len(evalCase.Images) == 0 {
			return EvalManifest{}, fmt.Errorf("vision: eval case %q has no images", evalCase.ID)
		}
	}
	return manifest, nil
}

// FieldScore is exact and normalized agreement for one field.
type FieldScore struct {
	Expected   int     `json:"expected"`
	Exact      int     `json:"exact"`
	Normalized int     `json:"normalized"`
	ExactRate  float64 `json:"exact_rate"`
	NormRate   float64 `json:"normalized_rate"`
}

// EvalReport aggregates a run. FabricatedNRN counts cases where the model
// produced an NRN but the labeller read none.
type EvalReport struct {
	Cases         int                   `json:"cases"`
	Fields        map[string]FieldScore `json:"fields"`
	FabricatedNRN int                   `json:"fabricated_nrn"`
	Failures      []string              `json:"failures"`
}

// ScoreCase adds one case to the report. The caller supplies the validated
// label, so scoring uses exactly what the pipeline would return.
func (r *EvalReport) ScoreCase(evalCase EvalCase, got LabelData) {
	if r.Fields == nil {
		r.Fields = make(map[string]FieldScore)
	}
	r.Cases++

	scoreString(r, "nrn", evalCase.Expected.NRN, got.NRN.Value, registry.NormalizeNRN)
	scoreString(r, "product_name", evalCase.Expected.ProductName, got.ProductName.Value, normalizeName)
	scoreString(r, "manufacturer", evalCase.Expected.Manufacturer, got.Manufacturer.Value, normalizeName)
	scoreString(r, "batch_no", evalCase.Expected.BatchNo, got.BatchNo.Value, normalizeBatch)

	if evalCase.Expected.MfgDate != "" {
		scoreString(r, "mfg_date", normalizeDate(evalCase.Expected.MfgDate), dateValue(got.MfgDate.Value), normalizeDate)
	}
	if evalCase.Expected.ExpiryDate != "" {
		scoreString(r, "expiry_date", normalizeDate(evalCase.Expected.ExpiryDate), dateValue(got.ExpiryDate.Value), normalizeDate)
	}

	if evalCase.Expected.NRN == "" && got.NRN.Value != nil {
		r.FabricatedNRN++
		r.Failures = append(r.Failures, evalCase.ID+": fabricated nrn "+*got.NRN.Value)
	}
	scoreIngredients(r, evalCase, got)
}

func scoreIngredients(report *EvalReport, evalCase EvalCase, got LabelData) {
	if len(evalCase.Expected.Ingredients) == 0 {
		return
	}
	field := report.Fields["ingredients"]
	field.Expected++
	report.Fields["ingredients"] = field

	expected := make(map[string]string, len(evalCase.Expected.Ingredients))
	for _, ingredient := range evalCase.Expected.Ingredients {
		expected[registry.NormalizeIngredient(ingredient.Name)] = normalizeStrength(ingredient.Strength)
	}
	matchedExact, matchedNormalized := 0, 0
	for _, expectedName := range sortedKeys(expected) {
		expectedStrength := expected[expectedName]
		for _, claim := range got.Ingredients {
			if claim.NameNorm != expectedName {
				continue
			}
			if claim.StrengthRaw == evalCase.Expected.rawStrength(expectedName) {
				matchedExact++
			}
			if normalizeStrength(claim.Strength.Raw) == expectedStrength {
				matchedNormalized++
			}
			break
		}
	}
	if matchedExact == len(expected) {
		field.Exact++
	}
	if matchedNormalized == len(expected) {
		field.Normalized++
	}
	if matchedNormalized < len(expected) {
		report.Failures = append(report.Failures, evalCase.ID+": ingredients mismatch")
	}
	report.Fields["ingredients"] = field
}

func (e EvalExpected) rawStrength(normalizedName string) string {
	for _, ingredient := range e.Ingredients {
		if registry.NormalizeIngredient(ingredient.Name) == normalizedName {
			return ingredient.Strength
		}
	}
	return ""
}

func scoreString(report *EvalReport, name, expected string, got *string, normalize func(string) string) {
	if strings.TrimSpace(expected) == "" {
		return
	}
	field := report.Fields[name]
	field.Expected++
	if got != nil {
		if *got == expected {
			field.Exact++
		}
		if normalize(*got) == normalize(expected) {
			field.Normalized++
		}
	}
	field.ExactRate = rate(field.Exact, field.Expected)
	field.NormRate = rate(field.Normalized, field.Expected)
	report.Fields[name] = field
	if got == nil || normalize(*got) != normalize(expected) {
		report.Failures = append(report.Failures, name+": expected "+expected)
	}
}

func rate(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func normalizeName(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}

func normalizeBatch(value string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
}

func normalizeDate(value string) string {
	if parsed, ok := ParseLabelDate(value); ok {
		return parsed.Format()
	}
	return strings.TrimSpace(value)
}

func normalizeStrength(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
}

func dateValue(date *LabelDate) *string {
	if date == nil {
		return nil
	}
	formatted := date.Format()
	return &formatted
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
