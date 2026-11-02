package verify

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/registry"
	"github.com/benfaruna/medverify/internal/vision"
)

func TestLoadConfigRejectsUnknownFieldsAndBadRanges(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	assert.Equal(t, "v1", cfg.Version)
	assert.Greater(t, cfg.NameThreshold, 0.0)
	assert.Greater(t, cfg.MaxCandidates, 0)

	path := t.TempDir() + "/bad.yaml"
	require.NoError(t, writeFile(path, "version: v1\nname_threshold: 0.5\nunknown: 1\n"))
	_, err = LoadConfig(path)
	require.Error(t, err)

	require.NoError(t, writeFile(path, "version: v1\nname_threshold: 2\n"))
	_, err = LoadConfig(path)
	require.Error(t, err)
}

func TestNormalizeNameStripsDosageAndPackWords(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "paracetamol 500 mg", NormalizeName("Paracetamol Tablets 500 mg"))
	assert.Equal(t, "paracetamol", NormalizeName("PARACETAMOL  film-coated  tablet"))
	assert.Equal(t, "cough", NormalizeName("Cough Syrup (Bottle)"))
	assert.Equal(t, "example", NormalizeName("  ... Example ... "))
}

func TestNormalizeManufacturerStripsCompanySuffixes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "example products", NormalizeManufacturer("Example Products Ltd."))
	assert.Equal(t, "example products", NormalizeManufacturer("EXAMPLE PRODUCTS LIMITED"))
	assert.Equal(t, "example", NormalizeManufacturer("Example Pharmaceuticals Plc"))
}

func TestSimilarityIsDeterministicAndBounded(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 1.0, Similarity("paracetamol", "paracetamol"))
	assert.Greater(t, Similarity("paracetamol", "paracetamal"), 0.5)
	assert.Less(t, Similarity("paracetamol", "ibuprofen"), 0.5)
	assert.Equal(t, 0.0, Similarity("ab", "cd"))
}

func TestStrengthComparisonAcrossUnits(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		label      string
		record     string
		agree      bool
		comparable bool
	}{
		{"500 mg", "0.5 g", true, true},
		{"0.5 g", "500 mg", true, true},
		{"100 mg/5 mL", "20 mg/mL", true, true},
		{"500 mg", "250 mg", false, true},
		{"500 mg", "not a strength", false, false},
	} {
		agree, comparable := strengthsAgree(registry.ParseStrength(tc.label), registry.ParseStrength(tc.record), 0.05)
		assert.Equal(t, tc.agree, agree, tc.label+" / "+tc.record)
		assert.Equal(t, tc.comparable, comparable, tc.label+" / "+tc.record)
	}
}

func TestUnknownPrintedFieldIsNotADisagreement(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	product := testProduct("04-1111", "Paracetamol Tablets 500 mg", "Example Pharma Ltd")
	catalog := &fakeCatalog{
		byNRN: map[string]registry.Product{"04-1111": product},
		ingredients: map[int64][]registry.Ingredient{
			product.ID: {{NameRaw: "Paracetamol", NameNorm: "paracetamol", Strength: registry.ParseStrength("500 mg")}},
		},
		count:  1,
		synced: &now,
	}
	matcher := newTestMatcher(t, catalog, now)

	label := vision.ValidateLabel(vision.RawLabel{
		NRN:         rawField("04-1111", 0.9),
		ProductName: rawField("Paracetamol Tablets 500 mg", 0.9),
		// Manufacturer and ingredients are deliberately absent.
	})
	result, err := matcher.Match(context.Background(), label)
	require.NoError(t, err)

	assert.Equal(t, MatchedConsistent, result.Outcome)
	assert.Equal(t, Unknown, result.Fields[FieldManufacturer].Outcome)
	assert.Equal(t, Unknown, result.Fields[FieldIngredients].Outcome)
	assert.Equal(t, Unknown, result.Fields[FieldStrength].Outcome)
	assert.False(t, hasDisagreement(result.Fields))
}

func TestExpiredLabelIsFlagged(t *testing.T) {
	t.Parallel()

	expiry, ok := vision.ParseLabelDate("Jan 2026")
	require.True(t, ok)
	field, note := compareExpiry(vision.Field[vision.LabelDate]{Value: &expiry}, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, Disagree, field.Outcome)
	assert.Contains(t, field.Reasons, ReasonExpiredOnLabel)
	require.NotNil(t, note)
}

func TestThresholdChangeChangesOutcomeWithoutCode(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	product := registry.Product{
		ID: 1, SourceID: "p1", NRN: "04-2222", Name: "Paracetamol 500 Tablets",
		Manufacturer: "Example Pharma Ltd", Status: "Active",
	}
	catalog := &fakeCatalog{
		byNRN:       map[string]registry.Product{},
		search:      []registry.SearchResult{{Product: product, Score: 0.7}},
		ingredients: map[int64][]registry.Ingredient{},
		count:       1,
		synced:      &now,
	}
	label := vision.ValidateLabel(vision.RawLabel{
		ProductName:  rawField("Paracetamol", 0.9),
		Manufacturer: rawField("Example Pharma Ltd", 0.9),
	})

	loose := newTestMatcher(t, catalog, now)
	result, err := loose.Match(context.Background(), label)
	require.NoError(t, err)
	assert.Equal(t, CandidateOnly, result.Outcome)

	strictConfig := loose.Config()
	strictConfig.NameThreshold = 0.99
	strictConfig.NameOnlyThreshold = 0.99
	strict, err := NewMatcher(Options{Catalog: catalog, Config: strictConfig, Now: func() time.Time { return now }})
	require.NoError(t, err)
	result, err = strict.Match(context.Background(), label)
	require.NoError(t, err)
	assert.Equal(t, NotFoundInDataset, result.Outcome)
}

func TestSelfComparisonAlwaysAgrees(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	names := []string{"Paracetamol Tablets", "Cough Syrup", "Amoxicillin Capsules", "Ibuprofen Gel"}
	manufacturers := []string{"Example Pharma Ltd", "Lagos Health Plc", "Roche Products Limited"}
	for _, name := range names {
		for _, manufacturer := range manufacturers {
			product := testProduct("04-1111", name, manufacturer)
			catalog := &fakeCatalog{
				byNRN: map[string]registry.Product{"04-1111": product},
				ingredients: map[int64][]registry.Ingredient{
					product.ID: {{NameRaw: "Paracetamol", NameNorm: "paracetamol", Strength: registry.ParseStrength("500 mg")}},
				},
				count:  1,
				synced: &now,
			}
			matcher := newTestMatcher(t, catalog, now)
			label := vision.ValidateLabel(vision.RawLabel{
				NRN:          rawField("04-1111", 0.9),
				ProductName:  rawField(name, 0.9),
				Manufacturer: rawField(manufacturer, 0.9),
				Ingredients: []vision.RawIngredient{
					{Name: "Paracetamol", Strength: "500 mg", Confidence: 0.9},
				},
			})
			result, err := matcher.Match(context.Background(), label)
			require.NoError(t, err)
			assert.Equal(t, MatchedConsistent, result.Outcome, name+" / "+manufacturer)
			assert.False(t, hasDisagreement(result.Fields))
		}
	}
}

func TestUnreadableInputNeedsNoCatalogLookup(t *testing.T) {
	t.Parallel()

	catalog := &fakeCatalog{count: 1}
	matcher := newTestMatcher(t, catalog, time.Now())
	result, err := matcher.Match(context.Background(), vision.ValidateLabel(vision.RawLabel{}))
	require.NoError(t, err)
	assert.Equal(t, UnreadableInput, result.Outcome)
	assert.Equal(t, 0, catalog.lookups)
}

func TestCatalogErrorPropagates(t *testing.T) {
	t.Parallel()

	catalog := &fakeCatalog{countErr: errors.New("db down")}
	matcher := newTestMatcher(t, catalog, time.Now())
	_, err := matcher.Match(context.Background(), vision.ValidateLabel(vision.RawLabel{
		NRN: rawField("04-1111", 0.9),
	}))
	require.Error(t, err)
}

func BenchmarkExactMatch(b *testing.B) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	product := testProduct("04-1111", "Paracetamol Tablets 500 mg", "Example Pharma Ltd")
	catalog := &fakeCatalog{
		byNRN: map[string]registry.Product{"04-1111": product},
		ingredients: map[int64][]registry.Ingredient{
			product.ID: {{NameRaw: "Paracetamol", NameNorm: "paracetamol", Strength: registry.ParseStrength("500 mg")}},
		},
		count:  1,
		synced: &now,
	}
	matcher, err := NewMatcher(Options{Catalog: catalog, Config: mustConfig(b), Now: func() time.Time { return now }})
	require.NoError(b, err)
	label := vision.ValidateLabel(vision.RawLabel{
		NRN:         rawField("04-1111", 0.9),
		ProductName: rawField("Paracetamol Tablets 500 mg", 0.9),
	})

	b.ResetTimer()
	for range b.N {
		if _, err := matcher.Match(context.Background(), label); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFuzzyMatch(b *testing.B) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	products := make([]registry.SearchResult, 20)
	for i := range products {
		products[i] = registry.SearchResult{
			Product: registry.Product{
				ID: int64(i + 1), SourceID: "p", NRN: "04-" + string(rune('A'+i)),
				Name: "Paracetamol Tablets 500 mg", Manufacturer: "Example Pharma Ltd",
				Status: "Active",
			},
			Score: 0.8,
		}
	}
	catalog := &fakeCatalog{search: products, count: 1000, synced: &now}
	matcher, err := NewMatcher(Options{Catalog: catalog, Config: mustConfig(b), Now: func() time.Time { return now }})
	require.NoError(b, err)
	label := vision.ValidateLabel(vision.RawLabel{
		ProductName:  rawField("Paracetamol Tablets 500 mg", 0.9),
		Manufacturer: rawField("Example Pharma Ltd", 0.9),
	})

	b.ResetTimer()
	for range b.N {
		if _, err := matcher.Match(context.Background(), label); err != nil {
			b.Fatal(err)
		}
	}
}

// --- test helpers ---------------------------------------------------------

func newTestMatcher(t *testing.T, catalog Catalog, now time.Time) *Matcher {
	t.Helper()
	cfg, err := LoadConfig("")
	require.NoError(t, err)
	matcher, err := NewMatcher(Options{Catalog: catalog, Config: cfg, Now: func() time.Time { return now }})
	require.NoError(t, err)
	return matcher
}

func mustConfig(tb testing.TB) Config {
	tb.Helper()
	cfg, err := LoadConfig("")
	require.NoError(tb, err)
	return cfg
}

func testProduct(nrn, name, manufacturer string) registry.Product {
	expires := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	return registry.Product{
		ID: 1, SourceID: "p1", NRN: nrn, Name: name, Manufacturer: manufacturer,
		Status: "Active", ExpiresAt: &expires,
	}
}

func rawField(value string, confidence float64) vision.RawField {
	return vision.RawField{Value: &value, Confidence: confidence, Source: 1}
}

func writeFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o600)
}

type fakeCatalog struct {
	byNRN       map[string]registry.Product
	search      []registry.SearchResult
	ingredients map[int64][]registry.Ingredient
	count       int
	synced      *time.Time
	countErr    error
	lookups     int
}

func (c *fakeCatalog) GetByNRN(_ context.Context, raw string) (registry.Product, bool, error) {
	c.lookups++
	product, ok := c.byNRN[registry.NormalizeNRN(raw)]
	return product, ok, nil
}

func (c *fakeCatalog) SearchByName(context.Context, string, int) ([]registry.SearchResult, error) {
	c.lookups++
	return c.search, nil
}

func (c *fakeCatalog) Ingredients(_ context.Context, id int64) ([]registry.Ingredient, error) {
	return c.ingredients[id], nil
}

func (c *fakeCatalog) CountProducts(context.Context) (int, error) {
	if c.countErr != nil {
		return 0, c.countErr
	}
	return c.count, nil
}

func (c *fakeCatalog) LatestSuccessfulSync(context.Context) (time.Time, bool, error) {
	if c.synced == nil {
		return time.Time{}, false, nil
	}
	return *c.synced, true, nil
}
