package vision

import (
	"sort"
	"strings"

	"github.com/benfaruna/medverify/internal/registry"
)

// Field is one extracted label value plus the model's confidence and the
// 1-based image position it came from. A nil Value means the field was not read
// confidently; it is never filled by guessing.
type Field[T any] struct {
	Value      *T       `json:"value,omitempty"`
	Confidence float64  `json:"confidence"`
	Source     int      `json:"source,omitempty"`
	Raw        string   `json:"raw,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
}

// LabelDate is a parsed manufacturing or expiry date. Precision is "day" or
// "month"; a month-only label never invents a day.
type LabelDate struct {
	Year      int    `json:"year"`
	Month     int    `json:"month"`
	Day       int    `json:"day,omitempty"`
	Precision string `json:"precision"`
	Raw       string `json:"raw"`
}

// IngredientClaim is one active ingredient with its normalized name and parsed
// strength. Raw text is kept even when it cannot be parsed.
type IngredientClaim struct {
	NameRaw     string            `json:"name_raw"`
	NameNorm    string            `json:"name_norm"`
	StrengthRaw string            `json:"strength_raw,omitempty"`
	Strength    registry.Strength `json:"strength"`
	Confidence  float64           `json:"confidence"`
	Source      int               `json:"source,omitempty"`
}

// LabelData is the validated extraction result stored on a scan. Every field
// can be empty; Unreadable lists the fields the model could not read, and
// Conflicts lists fields where images disagreed.
type LabelData struct {
	NRN           Field[string]     `json:"nrn"`
	ProductName   Field[string]     `json:"product_name"`
	Manufacturer  Field[string]     `json:"manufacturer"`
	BatchNo       Field[string]     `json:"batch_no"`
	MfgDate       Field[LabelDate]  `json:"mfg_date"`
	ExpiryDate    Field[LabelDate]  `json:"expiry_date"`
	Ingredients   []IngredientClaim `json:"ingredients"`
	Unreadable    []string          `json:"unreadable"`
	Conflicts     []string          `json:"conflicts"`
	Provider      string            `json:"provider,omitempty"`
	Model         string            `json:"model,omitempty"`
	PromptVersion string            `json:"prompt_version,omitempty"`
}

// RawField is one value as the provider returned it, before validators run.
type RawField struct {
	Value      *string `json:"value"`
	Confidence float64 `json:"confidence"`
	Source     int     `json:"source,omitempty"`
}

// RawIngredient is one provider ingredient claim, before normalization.
type RawIngredient struct {
	Name       string  `json:"name"`
	Strength   string  `json:"strength"`
	Confidence float64 `json:"confidence"`
	Source     int     `json:"source,omitempty"`
}

// RawLabel is the provider output. Nothing in it is trusted until ValidateLabel
// has run.
type RawLabel struct {
	NRN          RawField        `json:"nrn"`
	ProductName  RawField        `json:"product_name"`
	Manufacturer RawField        `json:"manufacturer"`
	BatchNo      RawField        `json:"batch_no"`
	MfgDate      RawField        `json:"mfg_date"`
	ExpiryDate   RawField        `json:"expiry_date"`
	Ingredients  []RawIngredient `json:"ingredients"`
	Unreadable   []string        `json:"unreadable"`
	Conflicts    []string        `json:"conflicts,omitempty"`
}

// WithSource returns a copy whose unset field sources point at image position.
func (r RawLabel) WithSource(position int) RawLabel {
	r.NRN = withSource(r.NRN, position)
	r.ProductName = withSource(r.ProductName, position)
	r.Manufacturer = withSource(r.Manufacturer, position)
	r.BatchNo = withSource(r.BatchNo, position)
	r.MfgDate = withSource(r.MfgDate, position)
	r.ExpiryDate = withSource(r.ExpiryDate, position)
	if len(r.Ingredients) > 0 {
		ingredients := make([]RawIngredient, len(r.Ingredients))
		copy(ingredients, r.Ingredients)
		for i := range ingredients {
			if ingredients[i].Source == 0 {
				ingredients[i].Source = position
			}
		}
		r.Ingredients = ingredients
	}
	return r
}

func withSource(field RawField, position int) RawField {
	if field.Source == 0 {
		field.Source = position
	}
	return field
}

// ValidateLabel turns untrusted provider output into typed label data. Invalid
// values are dropped into Unreadable with their raw text retained; nothing is
// auto-corrected.
func ValidateLabel(raw RawLabel) LabelData {
	label := LabelData{Conflicts: []string{}, Unreadable: []string{}}
	unreadable := make(map[string]struct{})
	addUnreadable := func(name string) {
		if _, exists := unreadable[name]; !exists {
			unreadable[name] = struct{}{}
			label.Unreadable = append(label.Unreadable, name)
		}
	}
	for _, name := range raw.Unreadable {
		name = strings.TrimSpace(name)
		if name != "" {
			addUnreadable(name)
		}
	}
	label.Conflicts = append(label.Conflicts, raw.Conflicts...)

	label.ProductName = validateStringField(raw.ProductName, "product_name", addUnreadable)
	label.Manufacturer = validateStringField(raw.Manufacturer, "manufacturer", addUnreadable)
	label.BatchNo = validateStringField(raw.BatchNo, "batch_no", addUnreadable)
	label.NRN = validateNRN(raw.NRN, addUnreadable)
	label.MfgDate = validateDateField(raw.MfgDate, "mfg_date", addUnreadable)
	label.ExpiryDate = validateDateField(raw.ExpiryDate, "expiry_date", addUnreadable)

	label.Ingredients = make([]IngredientClaim, 0, len(raw.Ingredients))
	for _, ingredient := range raw.Ingredients {
		name := strings.TrimSpace(ingredient.Name)
		if name == "" {
			continue
		}
		claim := IngredientClaim{
			NameRaw:     name,
			NameNorm:    registry.NormalizeIngredient(name),
			StrengthRaw: strings.TrimSpace(ingredient.Strength),
			Strength:    registry.ParseStrength(ingredient.Strength),
			Confidence:  clampConfidence(ingredient.Confidence),
			Source:      ingredient.Source,
		}
		if claim.NameNorm == "" {
			claim.NameNorm = strings.ToLower(name)
		}
		label.Ingredients = append(label.Ingredients, claim)
	}
	if len(label.Ingredients) == 0 {
		addUnreadable("ingredients")
	}

	sort.Strings(label.Unreadable)
	return label
}

func validateStringField(raw RawField, name string, addUnreadable func(string)) Field[string] {
	field := Field[string]{Confidence: clampConfidence(raw.Confidence), Source: raw.Source}
	if raw.Value == nil {
		addUnreadable(name)
		return field
	}
	value := strings.TrimSpace(*raw.Value)
	if value == "" {
		addUnreadable(name)
		return field
	}
	field.Value = &value
	field.Raw = value
	return field
}

func validateNRN(raw RawField, addUnreadable func(string)) Field[string] {
	field := Field[string]{Confidence: clampConfidence(raw.Confidence), Source: raw.Source}
	if raw.Value == nil {
		addUnreadable("nrn")
		return field
	}
	field.Raw = strings.TrimSpace(*raw.Value)
	if field.Raw == "" {
		addUnreadable("nrn")
		return field
	}

	normalized := registry.NormalizeNRN(field.Raw)
	if !registry.NRNPattern.MatchString(normalized) {
		addUnreadable("nrn")
		return field
	}
	field.Value = &normalized
	field.Candidates = NRNVariants(normalized)
	return field
}

func validateDateField(raw RawField, name string, addUnreadable func(string)) Field[LabelDate] {
	field := Field[LabelDate]{Confidence: clampConfidence(raw.Confidence), Source: raw.Source}
	if raw.Value == nil {
		addUnreadable(name)
		return field
	}
	field.Raw = strings.TrimSpace(*raw.Value)
	if field.Raw == "" {
		addUnreadable(name)
		return field
	}
	date, ok := ParseLabelDate(field.Raw)
	if !ok {
		addUnreadable(name)
		return field
	}
	field.Value = &date
	return field
}

// ClampConfidence forces a provider confidence into [0, 1].
func clampConfidence(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

// OverallConfidence is the mean confidence of the fields the model returned.
// It is used only as a coarse gate; per-field confidence is preserved.
func (l LabelData) OverallConfidence() float64 {
	total := 0.0
	count := 0
	add := func(confidence float64, present bool) {
		if present {
			total += confidence
			count++
		}
	}
	add(l.NRN.Confidence, l.NRN.Value != nil)
	add(l.ProductName.Confidence, l.ProductName.Value != nil)
	add(l.Manufacturer.Confidence, l.Manufacturer.Value != nil)
	add(l.BatchNo.Confidence, l.BatchNo.Value != nil)
	add(l.MfgDate.Confidence, l.MfgDate.Value != nil)
	add(l.ExpiryDate.Confidence, l.ExpiryDate.Value != nil)
	for _, ingredient := range l.Ingredients {
		add(ingredient.Confidence, true)
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

// Identified reports whether at least one of the fields needed to verify a pack
// was read. A result without these is unreadable, never a "not found" verdict.
func (l LabelData) Identified() bool {
	return l.NRN.Value != nil || l.ProductName.Value != nil
}

// confusable maps characters that commonly confuse OCR and human readers. The
// NRN validator never rewrites them; phase 05 uses the candidates to decide
// which variant, if any, exists in the registry.
var confusable = map[rune][]rune{
	'O': {'0'},
	'0': {'O'},
	'I': {'1'},
	'1': {'I'},
	'S': {'5'},
	'5': {'S'},
	'B': {'8'},
	'8': {'B'},
	'Z': {'2'},
	'2': {'Z'},
	'G': {'6'},
	'6': {'G'},
}

// NRNVariants returns unique NRNs that differ from normalized by one or two
// confusable-character substitutions and still match the NRN shape. The
// original is never included, and no variant is treated as the read value.
func NRNVariants(normalized string) []string {
	seen := make(map[string]struct{})
	var variants []string
	runes := []rune(normalized)

	var walk func(index, changes int, current []rune)
	walk = func(index, changes int, current []rune) {
		if len(variants) >= 32 {
			return
		}
		if index == len(runes) {
			if changes == 0 {
				return
			}
			candidate := string(current)
			if candidate == normalized || !registry.NRNPattern.MatchString(candidate) {
				return
			}
			if _, exists := seen[candidate]; exists {
				return
			}
			seen[candidate] = struct{}{}
			variants = append(variants, candidate)
			return
		}

		walk(index+1, changes, current)
		if replacements, ok := confusable[runes[index]]; ok && changes < 2 {
			for _, replacement := range replacements {
				next := make([]rune, len(current))
				copy(next, current)
				next[index] = replacement
				walk(index+1, changes+1, next)
			}
		}
	}
	walk(0, 0, runes)
	sort.Strings(variants)
	return variants
}
