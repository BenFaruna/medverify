package vision

import (
	"sort"
	"strings"

	"github.com/benfaruna/medverify/internal/registry"
)

// ConflictThreshold is the confidence gap below which two different values are
// treated as a conflict. A conflict downgrades the field to unreadable instead
// of picking one at random.
const ConflictThreshold = 0.15

type candidate struct {
	value      string
	confidence float64
	source     int
	raw        string
}

// MergeLabels combines per-image provider output. For each field the highest
// confidence wins; two different values with similar confidence become a
// conflict and are left unreadable. Input order does not matter.
func MergeLabels(labels ...RawLabel) RawLabel {
	var merged RawLabel
	merged.NRN = mergeRawField(labels, "nrn", func(label RawLabel) RawField { return label.NRN }, &merged)
	merged.ProductName = mergeRawField(labels, "product_name", func(label RawLabel) RawField { return label.ProductName }, &merged)
	merged.Manufacturer = mergeRawField(labels, "manufacturer", func(label RawLabel) RawField { return label.Manufacturer }, &merged)
	merged.BatchNo = mergeRawField(labels, "batch_no", func(label RawLabel) RawField { return label.BatchNo }, &merged)
	merged.MfgDate = mergeRawField(labels, "mfg_date", func(label RawLabel) RawField { return label.MfgDate }, &merged)
	merged.ExpiryDate = mergeRawField(labels, "expiry_date", func(label RawLabel) RawField { return label.ExpiryDate }, &merged)
	merged.Ingredients = mergeIngredients(labels, &merged)
	merged.Unreadable = mergeUnreadable(labels)
	merged.Conflicts = uniqueSorted(merged.Conflicts)
	return merged
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}

func mergeRawField(labels []RawLabel, name string, get func(RawLabel) RawField, merged *RawLabel) RawField {
	var candidates []candidate
	for _, label := range labels {
		field := get(label)
		if field.Value == nil {
			continue
		}
		value := strings.TrimSpace(*field.Value)
		if value == "" {
			continue
		}
		candidates = append(candidates, candidate{
			value:      value,
			confidence: clampConfidence(field.Confidence),
			source:     field.Source,
			raw:        value,
		})
	}
	return resolveCandidates(candidates, name, merged)
}

func resolveCandidates(candidates []candidate, name string, merged *RawLabel) RawField {
	if len(candidates) == 0 {
		return RawField{}
	}
	// Highest confidence first. Ties keep the first source deterministically.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].confidence > candidates[j].confidence
	})

	best := candidates[0]
	bestKey := normalizeMergeKey(best.value)
	for _, other := range candidates[1:] {
		if normalizeMergeKey(other.value) == bestKey {
			continue
		}
		if best.confidence-other.confidence < ConflictThreshold {
			// Two images read different values with similar confidence.
			merged.Conflicts = append(merged.Conflicts, name)
			return RawField{}
		}
	}
	value := best.value
	return RawField{Value: &value, Confidence: best.confidence, Source: best.source}
}

func mergeIngredients(labels []RawLabel, merged *RawLabel) []RawIngredient {
	type ingredientCandidate struct {
		claim    RawIngredient
		strength string
	}
	byName := make(map[string][]ingredientCandidate)
	order := make([]string, 0)
	for _, label := range labels {
		for _, ingredient := range label.Ingredients {
			name := strings.TrimSpace(ingredient.Name)
			if name == "" {
				continue
			}
			key := registry.NormalizeIngredient(name)
			if key == "" {
				key = strings.ToLower(name)
			}
			ingredient.Confidence = clampConfidence(ingredient.Confidence)
			if _, seen := byName[key]; !seen {
				order = append(order, key)
			}
			byName[key] = append(byName[key], ingredientCandidate{
				claim:    ingredient,
				strength: normalizeMergeKey(ingredient.Strength),
			})
		}
	}
	sort.Strings(order)

	ingredients := make([]RawIngredient, 0, len(order))
	for _, key := range order {
		candidates := byName[key]
		sort.SliceStable(candidates, func(i, j int) bool {
			return candidates[i].claim.Confidence > candidates[j].claim.Confidence
		})
		best := candidates[0]
		for _, other := range candidates[1:] {
			if other.strength == best.strength {
				continue
			}
			if best.claim.Confidence-other.claim.Confidence < ConflictThreshold {
				merged.Conflicts = append(merged.Conflicts, "ingredient:"+key)
				break
			}
		}
		ingredients = append(ingredients, best.claim)
	}
	return ingredients
}

func mergeUnreadable(labels []RawLabel) []string {
	seen := make(map[string]struct{})
	var names []string
	for _, label := range labels {
		for _, name := range label.Unreadable {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func normalizeMergeKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}
