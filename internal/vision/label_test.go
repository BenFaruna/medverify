package vision

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLabelDateAcceptedFormats(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw       string
		year      int
		month     int
		day       int
		precision string
	}{
		{"12 Jan 2027", 2027, 1, 12, "day"},
		{"2 January 2027", 2027, 1, 2, "day"},
		{"JAN 02 2027", 2027, 1, 2, "day"},
		{"2027-01-12", 2027, 1, 12, "day"},
		{"2027/01/12", 2027, 1, 12, "day"},
		{"25/12/2027", 2027, 12, 25, "day"},
		{"Jan 2027", 2027, 1, 0, "month"},
		{"January 2027", 2027, 1, 0, "month"},
		{"2027-01", 2027, 1, 0, "month"},
		{"01/2027", 2027, 1, 0, "month"},
		{"01.2027", 2027, 1, 0, "month"},
	}
	for _, tc := range cases {
		got, ok := ParseLabelDate(tc.raw)
		require.True(t, ok, tc.raw)
		assert.Equal(t, tc.year, got.Year, tc.raw)
		assert.Equal(t, tc.month, got.Month, tc.raw)
		assert.Equal(t, tc.day, got.Day, tc.raw)
		assert.Equal(t, tc.precision, got.Precision, tc.raw)
	}
}

func TestParseLabelDateRejectsAmbiguousAndImpossible(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"03/04/2027",  // day-first or month-first: unknown
		"31 Feb 2027", // impossible
		"13/13/2027",  // invalid month
		"07/27/2027",  // month-first but locale unknown
		"",            // empty
		"EXP",         // not a date
		"2027",        // year only is not an expiry
	} {
		_, ok := ParseLabelDate(raw)
		assert.False(t, ok, raw)
	}
}

func TestValidateLabelKeepsOnlyValidNRNAndOffersCandidates(t *testing.T) {
	t.Parallel()

	value := "O1-2345"
	label := ValidateLabel(RawLabel{NRN: RawField{Value: &value, Confidence: 0.8, Source: 1}})

	require.NotNil(t, label.NRN.Value)
	assert.Equal(t, "O1-2345", *label.NRN.Value)
	assert.Contains(t, label.NRN.Candidates, "01-2345")
	assert.NotContains(t, label.NRN.Candidates, "O1-2345")
}

func TestValidateLabelDoesNotAutoCorrectInvalidNRN(t *testing.T) {
	t.Parallel()

	value := "not an nrn"
	label := ValidateLabel(RawLabel{NRN: RawField{Value: &value, Confidence: 0.9}})

	assert.Nil(t, label.NRN.Value)
	assert.Equal(t, "not an nrn", label.NRN.Raw)
	assert.Contains(t, label.Unreadable, "nrn")
}

func TestValidateLabelNormalizesIngredients(t *testing.T) {
	t.Parallel()

	label := ValidateLabel(RawLabel{
		ProductName: RawField{Value: ptr("Paracetamol Tablets"), Confidence: 0.95},
		Ingredients: []RawIngredient{
			{Name: "Paracetamol", Strength: "500 mg", Confidence: 0.9},
			{Name: "Unknown Excipient", Strength: "not a strength", Confidence: 0.4},
		},
	})

	require.Len(t, label.Ingredients, 2)
	assert.Equal(t, "paracetamol", label.Ingredients[0].NameNorm)
	require.NotNil(t, label.Ingredients[0].Strength.Value)
	assert.Equal(t, 500.0, *label.Ingredients[0].Strength.Value)
	assert.Equal(t, "mg", label.Ingredients[0].Strength.Unit)
	assert.False(t, label.Ingredients[1].Strength.Parsed())
	assert.Equal(t, "not a strength", label.Ingredients[1].StrengthRaw)
}

func TestValidateLabelReportsMissingFields(t *testing.T) {
	t.Parallel()

	label := ValidateLabel(RawLabel{})
	assert.False(t, label.Identified())
	assert.Zero(t, label.OverallConfidence())
	assert.Contains(t, label.Unreadable, "nrn")
	assert.Contains(t, label.Unreadable, "product_name")
	assert.Contains(t, label.Unreadable, "expiry_date")
}

func TestMergeLabelsTakesHighestConfidence(t *testing.T) {
	t.Parallel()

	high := "01-2345"
	low := "01-9999"
	merged := MergeLabels(
		RawLabel{NRN: RawField{Value: &low, Confidence: 0.4, Source: 1}},
		RawLabel{NRN: RawField{Value: &high, Confidence: 0.9, Source: 2}},
	)

	require.NotNil(t, merged.NRN.Value)
	assert.Equal(t, high, *merged.NRN.Value)
	assert.Equal(t, 2, merged.NRN.Source)
	assert.Empty(t, merged.Conflicts)
}

func TestMergeLabelsMarksCloseConflictUnreadable(t *testing.T) {
	t.Parallel()

	first := "01-2345"
	second := "01-9999"
	merged := MergeLabels(
		RawLabel{NRN: RawField{Value: &first, Confidence: 0.80, Source: 1}},
		RawLabel{NRN: RawField{Value: &second, Confidence: 0.88, Source: 2}},
	)

	assert.Nil(t, merged.NRN.Value)
	assert.Contains(t, merged.Conflicts, "nrn")

	label := ValidateLabel(merged)
	assert.Nil(t, label.NRN.Value)
	assert.Contains(t, label.Unreadable, "nrn")
}

func TestMergeLabelsAgreementIsNotAConflict(t *testing.T) {
	t.Parallel()

	first := "01-2345"
	second := " 01-2345 "
	merged := MergeLabels(
		RawLabel{NRN: RawField{Value: &first, Confidence: 0.7, Source: 1}},
		RawLabel{NRN: RawField{Value: &second, Confidence: 0.9, Source: 2}},
	)

	require.NotNil(t, merged.NRN.Value)
	assert.Equal(t, "01-2345", *merged.NRN.Value)
	assert.Empty(t, merged.Conflicts)
}

func TestMergeLabelsSingleAndEmpty(t *testing.T) {
	t.Parallel()

	name := "Paracetamol"
	single := MergeLabels(RawLabel{ProductName: RawField{Value: &name, Confidence: 0.9, Source: 1}})
	require.NotNil(t, single.ProductName.Value)
	assert.Equal(t, name, *single.ProductName.Value)

	empty := MergeLabels()
	assert.Nil(t, empty.NRN.Value)
	assert.Empty(t, empty.Ingredients)
}

func TestMergeIngredientsKeepsHighestConfidencePerName(t *testing.T) {
	t.Parallel()

	merged := MergeLabels(
		RawLabel{Ingredients: []RawIngredient{{Name: "Paracetamol", Strength: "500 mg", Confidence: 0.5, Source: 1}}},
		RawLabel{Ingredients: []RawIngredient{{Name: "paracetamol", Strength: "500mg", Confidence: 0.9, Source: 2}}},
	)

	require.Len(t, merged.Ingredients, 1)
	assert.Equal(t, 0.9, merged.Ingredients[0].Confidence)
	assert.Equal(t, 2, merged.Ingredients[0].Source)
}

func ptr[T any](value T) *T { return &value }
