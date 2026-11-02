package verify

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/benfaruna/medverify/internal/registry"
	"github.com/benfaruna/medverify/internal/vision"
)

// TestSelfComparisonNeverDisagrees is the phase 05 property: a registry record
// compared with a label built from that same record must never produce a
// disagreement. Unknown is allowed when a value cannot be compared; treating a
// value as disagree with itself would flag a genuine pack.
func TestSelfComparisonNeverDisagrees(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	strengths := []string{"500 mg", "0.5 g", "250 mcg", "100 mg/5 mL", "20 mg/mL", "10 %", "5 mL"}
	forms := []string{"", " Tablets", " Capsules", " Syrup", " Injection"}
	suffixes := []string{"", " Ltd", " Limited", " Plc", " Pharmaceuticals Ltd"}
	registrations := []string{"04-1111", "A3-100358", "B4-4703"}

	rapid.Check(t, func(rt *rapid.T) {
		base := "z" + rapid.StringMatching(`[a-z]{3,10}`).Draw(rt, "base")
		name := upperFirst(base) + rapid.SampledFrom(forms).Draw(rt, "form") +
			" " + rapid.SampledFrom(strengths).Draw(rt, "name_strength")
		company := upperFirst(base) + rapid.SampledFrom(suffixes).Draw(rt, "suffix")
		strengthText := rapid.SampledFrom(strengths).Draw(rt, "strength")
		nrn := rapid.SampledFrom(registrations).Draw(rt, "nrn")

		expires := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
		product := registry.Product{
			ID: 1, SourceID: "p1", NRN: nrn, Name: name, Manufacturer: company,
			Status: "Active", ExpiresAt: &expires,
			Ingredients: []registry.Ingredient{{
				NameRaw: base, NameNorm: base,
				Strength: registry.ParseStrength(strengthText), StrengthRaw: strengthText,
			}},
		}
		catalog := &fakeCatalog{
			byNRN: map[string]registry.Product{registry.NormalizeNRN(nrn): product},
			ingredients: map[int64][]registry.Ingredient{
				product.ID: product.Ingredients,
			},
			count:  1,
			synced: &now,
		}
		matcher := newTestMatcher(t, catalog, now)

		expiry := vision.LabelDate{Year: 2030, Month: 5, Precision: "month", Raw: "05/2030"}
		label := vision.LabelData{
			NRN:          vision.Field[string]{Value: &nrn, Confidence: 1, Source: 1},
			ProductName:  vision.Field[string]{Value: &name, Confidence: 1, Source: 1},
			Manufacturer: vision.Field[string]{Value: &company, Confidence: 1, Source: 1},
			ExpiryDate:   vision.Field[vision.LabelDate]{Value: &expiry, Confidence: 1, Source: 1},
			Ingredients: []vision.IngredientClaim{{
				NameRaw: base, NameNorm: base, StrengthRaw: strengthText,
				Strength: product.Ingredients[0].Strength, Confidence: 1, Source: 1,
			}},
		}

		result, err := matcher.Match(context.Background(), label)
		require.NoError(t, err)
		assert.Equal(t, MatchedConsistent, result.Outcome, "name=%q company=%q strength=%q", name, company, strengthText)
		for field, comparison := range result.Fields {
			assert.NotEqual(t, Disagree, comparison.Outcome, "field %s disagreed with itself", field)
		}
	})
}

func upperFirst(value string) string {
	if value == "" {
		return value
	}
	return string(value[0]-'a'+'A') + value[1:]
}
