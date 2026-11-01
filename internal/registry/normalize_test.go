package registry_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/benfaruna/medverify/internal/registry"
)

func TestNormalizeNRN(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"03-1450":         "03-1450",
		" 03-1450 ":       "03-1450",
		"a11-0551":        "A11-0551",
		"A11-0237 ":       "A11-0237",
		" A4-100942":      "A4-100942",
		"A7-2373L":        "A7-2373L",
		"a4-4479l":        "A4-4479L",
		"A6-0069":         "A6-0069",
		"A99-999999":      "A99-999999",
		"00-0000":         "00-0000",
		"A11\u20100551":   "A11-0551", // en dash
		"A11\u22120551":   "A11-0551", // minus sign
		"\uFEFFA11-0551":  "A11-0551", // byte-order mark
		"":                "",
		"not an nrn 1234": "NOTANNRN1234",
	}

	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, want, registry.NormalizeNRN(raw))
		})
	}
}

func TestNormalizeNRNIsIdempotent(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		raw := rapid.StringMatching(`[A-Za-z0-9\- ]{0,24}`).Draw(t, "nrn")
		once := registry.NormalizeNRN(raw)
		assert.Equal(t, once, registry.NormalizeNRN(once), "input %q", raw)
	})
}

func TestNormalizeNRNValidation(t *testing.T) {
	t.Parallel()

	valid := []string{
		"03-1450", "04-0858", "A11-0551", "A4-101970", "A99-999999",
		"A7-2373L", "A4-4479L",
	}
	for _, value := range valid {
		assert.True(t, registry.ValidNRN(value), "expected %q to be valid", value)
	}

	invalid := []string{
		"", "04-", "A-123", "04-12", "NAFDAC 04-1234", "00-0000A1", "not-an-nrn",
	}
	for _, value := range invalid {
		assert.False(t, registry.ValidNRN(value), "expected %q to be invalid", value)
	}
}

func TestNormalizeIngredientRealCompositions(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		// Plain names from the captured fixtures.
		"Paracetamol":                           "paracetamol",
		"Acetaminophen":                         "paracetamol",
		"Acetominophen":                         "paracetamol",
		"Lamivudine":                            "lamivudine",
		"Zidovudine":                            "zidovudine",
		"Azithromycin (Azithromycin Dihydrate)": "azithromycin",
		"Pregabalin":                            "pregabalin",
		"Tocilizumab":                           "tocilizumab",
		"Bevacizumab":                           "bevacizumab",
		"Metformin":                             "metformin",
		"Omeprazole":                            "omeprazole",
		"Artemether":                            "artemether",
		"Lumefantrine":                          "lumefantrine",
		"Ceftriaxone":                           "ceftriaxone",
		"Ciprofloxacin":                         "ciprofloxacin",

		// Salts and hydrates.
		"Paracetamol Hydrochloride":                       "paracetamol",
		"Amoxicillin Trihydrate":                          "amoxicillin",
		"Amoxycillin":                                     "amoxicillin",
		"Chlorpheniramine Maleate":                        "chlorphenamine",
		"Chlorphenamine Maleate":                          "chlorphenamine",
		"Tramadol Hydrochloride":                          "tramadol",
		"Pseudoephedrine Hydrochloride":                   "pseudoephedrine",
		"Orphenadrine Citrate":                            "orphenadrine",
		"Diclofenac Sodium":                               "diclofenac",
		"Diclofenac Potassium":                            "diclofenac",
		"Metformin Hydrochloride":                         "metformin",
		"Thiamine Hydrochloride":                          "thiamine",
		"Pyridoxine Hydrochloride":                        "pyridoxine",
		"Cyanocobalamin":                                  "cyanocobalamin",
		"Ferrous Fumarate":                                "ferrous fumarate",
		"Dextrose Anhydrous":                              "dextrose",
		"Glucose (as Monohydrate)":                        "glucose",
		"Paracetamol BP":                                  "paracetamol",
		"Azithromycin Dihydrate USP":                      "azithromycin",
		"Insulin regular (Human)":                         "insulin regular",
		"Epoetin alfa (Recombinant human erythropoietin)": "epoetin alfa",

		// Compounds that must not lose their second word.
		"Sodium Chloride":     "sodium chloride",
		"Potassium Chloride":  "potassium chloride",
		"Calcium Carbonate":   "calcium carbonate",
		"Magnesium Hydroxide": "magnesium hydroxide",
		"Zinc Sulfate":        "zinc sulfate",
		"Aluminium Hydroxide": "aluminium hydroxide",
		"Aluminum Hydroxide":  "aluminium hydroxide",

		// Vitamin synonyms.
		"Vitamin C":                    "ascorbic acid",
		"Vit C":                        "ascorbic acid",
		"Vitamin B1":                   "thiamine",
		"Vit. B1":                      "thiamine",
		"Vitamin B2":                   "riboflavin",
		"Vitamin B6":                   "pyridoxine",
		"Vitamin B12":                  "cyanocobalamin",
		"Vitamin B12 (Cyanocobalamin)": "cyanocobalamin",
		"Vitamin A":                    "retinol",
		"Vitamin E":                    "tocopherol",
		"Niacinamide":                  "nicotinamide",
		"Folate":                       "folic acid",
		"Folic Acid":                   "folic acid",

		// Formatting noise from the source.
		"N-Acetyl Cysteine": "n acetyl cysteine",
		"Co Enzyme Q10":     "co enzyme q10",
		"OMEGA 3":           "omega 3",
		"Methylrosanilinium Chloride (Gentian Violet)":                                  "methylrosanilinium",
		"Human papillomavirus bivalent (types 16 & 18) L1 protein vaccine, recombinant": "human papillomavirus bivalent l1 protein vaccine recombinant",
		"Avian herpes virus (Marek's disease) vaccine, Live":                            "avian herpes virus vaccine live",
		"Hepatitis A vaccine, Inactivated":                                              "hepatitis a vaccine inactivated",

		// Unknown names pass through unchanged.
		"Some New Investigational Moiety": "some new investigational moiety",
		"":                                "",
	}

	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, want, registry.NormalizeIngredient(raw))
		})
	}
}

func TestNormalizeIngredientIsIdempotent(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		raw := rapid.StringMatching(`[A-Za-z0-9 ,.;:()\-]{0,60}`).Draw(t, "ingredient")
		once := registry.NormalizeIngredient(raw)
		assert.Equal(t, once, registry.NormalizeIngredient(once), "input %q", raw)
	})
}

func TestNormalizerCountsUnknownIngredients(t *testing.T) {
	t.Parallel()

	normalizer := registry.NewNormalizer()
	assert.Equal(t, "paracetamol", normalizer.Ingredient("Acetaminophen"))
	assert.Equal(t, "some new moiety", normalizer.Ingredient("Some New Moiety"))
	assert.Equal(t, "some new moiety", normalizer.Ingredient("some new moiety"))

	unknown := normalizer.Unknown()
	assert.Equal(t, 2, unknown["some new moiety"])
	assert.NotContains(t, unknown, "paracetamol")
}

func TestParseStrength(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw     string
		value   *float64
		unit    string
		perUnit string
	}{
		{raw: "500 mg", value: ptr(500), unit: "mg"},
		{raw: "500mg", value: ptr(500), unit: "mg"},
		{raw: "100MG", value: ptr(100), unit: "mg"},
		{raw: "250 mcg", value: ptr(250), unit: "mcg"},
		{raw: "250 µg", value: ptr(250), unit: "mcg"},
		{raw: "250 ug", value: ptr(250), unit: "mcg"},
		{raw: "1.5 g", value: ptr(1.5), unit: "g"},
		{raw: "120 mg/5 mL", value: ptr(120), unit: "mg", perUnit: "5 mL"},
		{raw: "125mg/5ml", value: ptr(125), unit: "mg", perUnit: "5 mL"},
		{raw: "100 mg/mL", value: ptr(100), unit: "mg", perUnit: "mL"},
		{raw: "300mg/2ml", value: ptr(300), unit: "mg", perUnit: "2 mL"},
		{raw: "10,000 IU/1 mL", value: ptr(10000), unit: "IU", perUnit: "1 mL"},
		{raw: "150,000,000 IU", value: ptr(150000000), unit: "IU"},
		{raw: "0.05%", value: ptr(0.05), unit: "%"},
		{raw: "5 %", value: ptr(5), unit: "%"},
		{raw: "160 ELISA units/0.5 mL", value: nil, unit: ""},
		{raw: "see Composition", value: nil, unit: ""},
		{raw: "NA", value: nil, unit: ""},
		{raw: "", value: nil, unit: ""},
		{raw: "1 x 60's", value: nil, unit: ""},
		{raw: "not less than 5 Lf (40 IU)", value: nil, unit: ""},
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			got := registry.ParseStrength(tc.raw)
			assert.Equal(t, tc.raw, got.Raw)
			if tc.value == nil {
				assert.Nil(t, got.Value, "value")
			} else {
				require.NotNil(t, got.Value, "value")
				assert.InDelta(t, *tc.value, *got.Value, 1e-9)
			}
			assert.Equal(t, tc.unit, got.Unit)
			assert.Equal(t, tc.perUnit, got.PerUnit)
		})
	}
}

func TestParseStrengthsSplitsListsAndKeepsRawText(t *testing.T) {
	t.Parallel()

	strengths := registry.ParseStrengths("100 mg; 500 mg ; see Composition;")
	require.Len(t, strengths, 3)
	assert.Equal(t, "100 mg", strengths[0].Raw)
	assert.Equal(t, 500.0, *strengths[1].Value)
	assert.False(t, strengths[2].Parsed())
	assert.Equal(t, "see Composition", strengths[2].Raw)
}

func TestParseStrengthIsIdempotentOnRaw(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		raw := rapid.StringMatching(`[0-9.,/ %A-Za-zµμ]{0,30}`).Draw(t, "strength")
		first := registry.ParseStrength(raw)
		second := registry.ParseStrength(first.Raw)
		assert.Equal(t, first.Raw, second.Raw)
		assert.Equal(t, first.Unit, second.Unit)
		assert.Equal(t, first.PerUnit, second.PerUnit)
	})
}

func ptr(value float64) *float64 { return &value }
