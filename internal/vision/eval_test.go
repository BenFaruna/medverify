package vision

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvalReportScoresNormalizedFields(t *testing.T) {
	t.Parallel()

	got := ValidateLabel(RawLabel{
		NRN:          RawField{Value: ptr("01-2345"), Confidence: 0.9},
		ProductName:  RawField{Value: ptr("Paracetamol  Tablets"), Confidence: 0.9},
		Manufacturer: RawField{Value: ptr("example pharma ltd"), Confidence: 0.9},
		BatchNo:      RawField{Value: ptr("PCM-001"), Confidence: 0.9},
		MfgDate:      RawField{Value: ptr("Jan 2026"), Confidence: 0.9},
		ExpiryDate:   RawField{Value: ptr("01/2028"), Confidence: 0.9},
		Ingredients:  []RawIngredient{{Name: "Paracetamol", Strength: "500mg", Confidence: 0.9}},
	})

	report := EvalReport{}
	report.ScoreCase(EvalCase{
		ID: "case-1",
		Expected: EvalExpected{
			NRN:          "01-2345",
			ProductName:  "Paracetamol Tablets",
			Manufacturer: "Example Pharma Ltd",
			BatchNo:      "PCM-001",
			MfgDate:      "2026-01",
			ExpiryDate:   "2028-01",
			Ingredients:  []EvalIngredient{{Name: "paracetamol", Strength: "500 mg"}},
		},
	}, got)

	assert.Equal(t, 1, report.Cases)
	assert.Equal(t, 0, report.FabricatedNRN)
	assert.Equal(t, 1, report.Fields["nrn"].Exact)
	assert.Equal(t, 1, report.Fields["product_name"].Normalized)
	assert.Equal(t, 1, report.Fields["ingredients"].Normalized)
	assert.Empty(t, report.Failures)
}

func TestEvalReportCountsFabricatedNRN(t *testing.T) {
	t.Parallel()

	got := ValidateLabel(RawLabel{
		NRN:         RawField{Value: ptr("01-2345"), Confidence: 0.9},
		ProductName: RawField{Value: ptr("Something"), Confidence: 0.9},
	})
	report := EvalReport{}
	report.ScoreCase(EvalCase{ID: "blurred", Expected: EvalExpected{ProductName: "Something"}}, got)

	assert.Equal(t, 1, report.FabricatedNRN)
	assert.Contains(t, report.Failures, "blurred: fabricated nrn 01-2345")
}

func TestLoadEvalManifestValidatesCases(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "manifest.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"name":"x","cases":[{"id":"a","images":["a.jpg"],"expected":{}}]}`), 0o600))

	manifest, err := LoadEvalManifest(path)
	require.NoError(t, err)
	assert.Equal(t, "x", manifest.Name)
	require.Len(t, manifest.Cases, 1)

	require.NoError(t, os.WriteFile(path, []byte(`{"cases":[]}`), 0o600))
	_, err = LoadEvalManifest(path)
	require.Error(t, err)

	require.NoError(t, os.WriteFile(path, []byte(`{"cases":[{"id":"a","images":[]}]}`), 0o600))
	_, err = LoadEvalManifest(path)
	require.Error(t, err)
}
