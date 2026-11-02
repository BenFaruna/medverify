//go:build integration

// Package verify_test proves registry verification against a real Postgres.
// The dataset is the committed phase 02 fixtures: 196 distinct real Greenbook
// products, minus two duplicate NRN rows the live sync would also skip. Six
// clearly synthetic rows add the cases public data does not contain yet
// (near-identical names, a withdrawn registration, a confusable NRN).
//
// Run with: TESTCONTAINERS_RYUK_DISABLED=true go test -tags=integration ./internal/verify
package verify_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/internal/registry"
	"github.com/benfaruna/medverify/internal/verify"
	"github.com/benfaruna/medverify/internal/vision"
	"github.com/benfaruna/medverify/migrations"
)

// fixedNow keeps registry expiry and label expiry comparisons deterministic.
var fixedNow = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

// The production store is the only catalog the matcher will use.
var _ verify.Catalog = (*registry.Store)(nil)

// syntheticNRNs are deliberately outside the observed Greenbook ranges so a
// fixture refresh can never collide with them.
const (
	nrnFallback     = "09-2001"
	nrnAmbiguousOne = "09-2002"
	nrnAmbiguousTwo = "09-2003"
	nrnWithdrawn    = "09-2004"
	nrnExpired      = "09-2005"
	nrnVariant      = "09-2006"
)

func syntheticProducts() []registry.Product {
	raw := []byte(`{"synthetic":true}`)
	fetchedAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	future := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
	past := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	return []registry.Product{
		{
			SourceID: "synthetic-fallback", NRN: nrnFallback,
			Name: "Kestrel Test Cough Syrup 100 mg/5 mL", Manufacturer: "Kestrel Pharma Ltd",
			Status: "Active", ExpiresAt: &future, Raw: raw, FetchedAt: fetchedAt,
			Ingredients: []registry.Ingredient{{
				NameRaw: "Dextromethorphan", NameNorm: "dextromethorphan",
				Strength: registry.ParseStrength("100 mg/5 mL"), StrengthRaw: "100 mg/5 mL",
			}},
		},
		{
			SourceID: "synthetic-ambiguous-one", NRN: nrnAmbiguousOne,
			Name: "Duomol Test Tablets 500 mg", Manufacturer: "Halcyon Laboratories Ltd",
			Status: "Active", ExpiresAt: &future, Raw: raw, FetchedAt: fetchedAt,
			Ingredients: []registry.Ingredient{{
				NameRaw: "Paracetamol", NameNorm: "paracetamol",
				Strength: registry.ParseStrength("500 mg"), StrengthRaw: "500 mg",
			}},
		},
		{
			SourceID: "synthetic-ambiguous-two", NRN: nrnAmbiguousTwo,
			Name: "Duomol Test Tablets 500 mg", Manufacturer: "Halcyon Laboratories Ltd",
			Status: "Active", ExpiresAt: &future, Raw: raw, FetchedAt: fetchedAt,
			Ingredients: []registry.Ingredient{{
				NameRaw: "Paracetamol", NameNorm: "paracetamol",
				Strength: registry.ParseStrength("500 mg"), StrengthRaw: "500 mg",
			}},
		},
		{
			SourceID: "synthetic-withdrawn", NRN: nrnWithdrawn,
			Name: "Retired Test Capsules 250 mg", Manufacturer: "Halcyon Laboratories Ltd",
			Status: "Withdrawn", ExpiresAt: &future, Raw: raw, FetchedAt: fetchedAt,
		},
		{
			SourceID: "synthetic-expired", NRN: nrnExpired,
			Name: "Lapsed Test Tablets 400 mg", Manufacturer: "Kestrel Pharma Ltd",
			Status: "Active", ExpiresAt: &past, Raw: raw, FetchedAt: fetchedAt,
		},
		{
			SourceID: "synthetic-variant", NRN: nrnVariant,
			Name: "Variant Test Syrup 120 mg/5 mL", Manufacturer: "Kestrel Pharma Ltd",
			Status: "Active", ExpiresAt: &future, Raw: raw, FetchedAt: fetchedAt,
			Ingredients: []registry.Ingredient{{
				NameRaw: "Paracetamol", NameNorm: "paracetamol",
				Strength: registry.ParseStrength("120 mg/5 mL"), StrengthRaw: "120 mg/5 mL",
			}},
		},
	}
}

type seeded struct {
	store    *registry.Store
	matcher  *verify.Matcher
	products []registry.Product
}

// seedRegistry starts a throwaway Postgres, applies migrations, and loads every
// committed Greenbook fixture page plus the synthetic rows.
func seedRegistry(t testing.TB) *seeded {
	t.Helper()

	databaseURL := testkit.Postgres(t)
	ctx := context.Background()
	require.NoError(t, migrations.Up(ctx, databaseURL))

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	store := registry.NewStore(pool)
	fetchedAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "greenbook", "raw", "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)

	seenSource := map[string]struct{}{}
	seenNRN := map[string]struct{}{}
	products := make([]registry.Product, 0, 220)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		require.NoError(t, err, path)
		page, err := registry.ParsePage(data, fetchedAt, nil)
		require.NoError(t, err, path)
		for _, product := range page.Products {
			if _, ok := seenSource[product.SourceID]; ok {
				continue
			}
			seenSource[product.SourceID] = struct{}{}
			if product.NRN != "" {
				if _, ok := seenNRN[product.NRN]; ok {
					// The live sync skips a duplicate NRN instead of failing
					// the page; the fixtures contain two such rows.
					continue
				}
				seenNRN[product.NRN] = struct{}{}
			}
			_, err := store.UpsertProduct(ctx, product)
			require.NoError(t, err, "seed %s from %s", product.SourceID, path)
			products = append(products, product)
		}
	}

	for _, product := range syntheticProducts() {
		_, err := store.UpsertProduct(ctx, product)
		require.NoError(t, err, "seed synthetic product %s", product.SourceID)
		products = append(products, product)
	}

	require.GreaterOrEqual(t, len(products), 100, "seeded dataset must hold at least 100 real products")

	_, err = pool.Exec(ctx, `
		INSERT INTO sync_runs (started_at, finished_at, status, rows_seen)
		VALUES ($1, $1, 'succeeded', $2)`, fetchedAt, len(products))
	require.NoError(t, err)

	cfg, err := verify.LoadConfig("")
	require.NoError(t, err)
	matcher, err := verify.NewMatcher(verify.Options{
		Catalog: store,
		Config:  cfg,
		Now:     func() time.Time { return fixedNow },
	})
	require.NoError(t, err)

	return &seeded{store: store, matcher: matcher, products: products}
}

// stored loads one seeded product and its persisted ingredients by NRN.
func (s *seeded) stored(t *testing.T, nrn string) (registry.Product, []registry.Ingredient) {
	t.Helper()

	ctx := context.Background()
	product, found, err := s.store.GetByNRN(ctx, nrn)
	require.NoError(t, err)
	require.True(t, found, "product %s missing from the seeded dataset", nrn)

	ingredients, err := s.store.Ingredients(ctx, product.ID)
	require.NoError(t, err)
	return product, ingredients
}

// richProduct finds a real active product with printable ingredients and a
// registration that has not lapsed, so mismatch tests exercise a real record.
func (s *seeded) richProduct(t *testing.T) (registry.Product, []registry.Ingredient) {
	t.Helper()

	for _, candidate := range s.products {
		if candidate.Status != "Active" || candidate.NRN == "" || candidate.Manufacturer == "" {
			continue
		}
		if candidate.ExpiresAt != nil && candidate.ExpiresAt.Before(fixedNow) {
			continue
		}
		for _, ingredient := range candidate.Ingredients {
			// A plain mg strength, not a concentration: the mismatch test
			// swaps in "9999 mg" and needs the same shape to be comparable.
			if ingredient.Strength.Parsed() && ingredient.Strength.Unit == "mg" && ingredient.Strength.PerUnit == "" {
				return s.stored(t, candidate.NRN)
			}
		}
	}
	t.Fatal("no active seeded product with a parsed mg ingredient")
	return registry.Product{}, nil
}

func TestOutcomesAgainstSeededDataset(t *testing.T) {
	seed := seedRegistry(t)
	ctx := context.Background()

	t.Run("exact NRN with agreeing fields is matched_consistent", func(t *testing.T) {
		product, ingredients := seed.richProduct(t)

		result, err := seed.matcher.Match(ctx, label(t, vision.RawLabel{
			NRN:          rawString(product.NRN, 0.98),
			ProductName:  rawString(product.Name, 0.97),
			Manufacturer: rawString(product.Manufacturer, 0.95),
			ExpiryDate:   rawString("May 2030", 0.94),
			Ingredients:  rawIngredients(ingredients),
		}))
		require.NoError(t, err)

		assert.Equal(t, verify.MatchedConsistent, result.Outcome, "notes: %+v", result.Notes)
		require.NotNil(t, result.Product)
		assert.Equal(t, product.NRN, result.Product.NRN)
		assert.Equal(t, "Active", result.RegistryStatus)
		assert.Equal(t, verify.Agree, result.Fields[verify.FieldName].Outcome)
		assert.Equal(t, verify.Agree, result.Fields[verify.FieldIngredients].Outcome)
		assert.Equal(t, verify.Agree, result.Fields[verify.FieldStrength].Outcome)
		assert.False(t, hasDisagreement(result))

		// Dataset coverage travels with every answer.
		assert.Equal(t, verify.ConfigVersion, result.Dataset.ConfigVersion)
		require.NotNil(t, result.Dataset.LastSuccessfulSync)
		assert.GreaterOrEqual(t, result.Dataset.Products, 100)
		assert.Equal(t, len(seed.products), result.Dataset.Products)
	})

	t.Run("exact NRN with a different strength is matched_with_mismatch", func(t *testing.T) {
		product, ingredients := seed.richProduct(t)

		claims := rawIngredients(ingredients)
		mismatched := false
		for i, ingredient := range ingredients {
			if mismatched || !ingredient.Strength.Parsed() || ingredient.Strength.Unit != "mg" || ingredient.Strength.PerUnit != "" {
				continue
			}
			claims[i].Strength = "9999 mg"
			mismatched = true
		}
		require.True(t, mismatched)

		result, err := seed.matcher.Match(ctx, label(t, vision.RawLabel{
			NRN:          rawString(product.NRN, 0.98),
			ProductName:  rawString(product.Name, 0.97),
			Manufacturer: rawString(product.Manufacturer, 0.95),
			ExpiryDate:   rawString("May 2030", 0.94),
			Ingredients:  claims,
		}))
		require.NoError(t, err)

		assert.Equal(t, verify.MatchedWithMismatch, result.Outcome)
		require.NotNil(t, result.Product)
		assert.Equal(t, verify.Disagree, result.Fields[verify.FieldStrength].Outcome)
		assert.Contains(t, result.Fields[verify.FieldStrength].Reasons, verify.ReasonStrengthMismatch)
		assert.Contains(t, noteCodes(result.Notes), verify.ReasonStrengthMismatch)
	})

	t.Run("printed expiry in the past is flagged on a matched product", func(t *testing.T) {
		product, ingredients := seed.richProduct(t)

		result, err := seed.matcher.Match(ctx, label(t, vision.RawLabel{
			NRN:          rawString(product.NRN, 0.98),
			ProductName:  rawString(product.Name, 0.97),
			Manufacturer: rawString(product.Manufacturer, 0.95),
			ExpiryDate:   rawString("January 2025", 0.9),
			Ingredients:  rawIngredients(ingredients),
		}))
		require.NoError(t, err)

		assert.Equal(t, verify.MatchedWithMismatch, result.Outcome)
		assert.Equal(t, verify.Disagree, result.Fields[verify.FieldExpiry].Outcome)
		assert.Contains(t, result.Fields[verify.FieldExpiry].Reasons, verify.ReasonExpiredOnLabel)
		assert.Contains(t, noteCodes(result.Notes), verify.ReasonExpiredOnLabel)
	})

	t.Run("unknown NRN and unknown name is not_found_in_dataset", func(t *testing.T) {
		result, err := seed.matcher.Match(ctx, label(t, vision.RawLabel{
			NRN:         rawString("77-7777", 0.9),
			ProductName: rawString("Xylophonic Unlisted Medicine 321 mg", 0.9),
		}))
		require.NoError(t, err)

		assert.Equal(t, verify.NotFoundInDataset, result.Outcome)
		assert.Nil(t, result.Product)
		assert.Empty(t, result.Candidates)
		// Honest dataset context, and no claim that the pack is fake.
		assert.GreaterOrEqual(t, result.Dataset.Products, 100)
		assert.NotContains(t, noteCodes(result.Notes), verify.ReasonDatasetEmpty)
		assert.NotContains(t, noteCodes(result.Notes), verify.ReasonDatasetNeverSynced)
	})

	t.Run("missing NRN with name and manufacturer is candidate_only", func(t *testing.T) {
		result, err := seed.matcher.Match(ctx, label(t, vision.RawLabel{
			ProductName:  rawString("Kestrel Test Cough Syrup 100 mg/5 mL", 0.95),
			Manufacturer: rawString("Kestrel Pharma Ltd", 0.94),
		}))
		require.NoError(t, err)

		assert.Equal(t, verify.CandidateOnly, result.Outcome)
		assert.Nil(t, result.Product, "candidate_only is never a confirmed match")
		require.NotEmpty(t, result.Candidates)
		assert.Equal(t, nrnFallback, result.Candidates[0].NRN)
		assert.False(t, result.Candidates[0].VariantMatch)
		assert.GreaterOrEqual(t, result.Candidates[0].Score, 0.55)
	})

	t.Run("two near-identical products are ambiguous", func(t *testing.T) {
		result, err := seed.matcher.Match(ctx, label(t, vision.RawLabel{
			ProductName:  rawString("Duomol Test Tablets 500 mg", 0.95),
			Manufacturer: rawString("Halcyon Laboratories Ltd", 0.94),
		}))
		require.NoError(t, err)

		assert.Equal(t, verify.Ambiguous, result.Outcome)
		assert.Nil(t, result.Product, "never silently pick one plausible product")
		require.Len(t, result.Candidates, 2)
		assert.LessOrEqual(t, len(result.Candidates), 5)
		assert.ElementsMatch(t,
			[]string{nrnAmbiguousOne, nrnAmbiguousTwo},
			[]string{result.Candidates[0].NRN, result.Candidates[1].NRN})
		assert.Contains(t, noteCodes(result.Notes), verify.ReasonAmbiguousCandidates)
	})

	t.Run("confusable O for 0 variant finds the product and is flagged", func(t *testing.T) {
		// O9-2006 is a valid NRN shape; its only registry variant is 09-2006.
		result, err := seed.matcher.Match(ctx, label(t, vision.RawLabel{
			NRN:         rawString("O9-2006", 0.88),
			ProductName: rawString("Variant Test Syrup 120 mg/5 mL", 0.93),
		}))
		require.NoError(t, err)

		assert.Equal(t, verify.CandidateOnly, result.Outcome)
		assert.Nil(t, result.Product)
		require.NotEmpty(t, result.Candidates)
		assert.Equal(t, nrnVariant, result.Candidates[0].NRN)
		assert.True(t, result.Candidates[0].VariantMatch)
		assert.Contains(t, noteCodes(result.Notes), verify.ReasonNRNVariantMatch)
	})

	t.Run("withdrawn registration is reported with its status", func(t *testing.T) {
		result, err := seed.matcher.Match(ctx, label(t, vision.RawLabel{
			NRN:          rawString(nrnWithdrawn, 0.98),
			ProductName:  rawString("Retired Test Capsules 250 mg", 0.95),
			Manufacturer: rawString("Halcyon Laboratories Ltd", 0.94),
			ExpiryDate:   rawString("May 2030", 0.9),
		}))
		require.NoError(t, err)

		assert.Equal(t, verify.MatchedWithMismatch, result.Outcome)
		assert.Equal(t, "Withdrawn", result.RegistryStatus)
		assert.Equal(t, verify.Disagree, result.Fields[verify.FieldRegistryStatus].Outcome)
		assert.Contains(t, result.Fields[verify.FieldRegistryStatus].Reasons, verify.ReasonRegistryWithdrawn)
		assert.Contains(t, noteCodes(result.Notes), verify.ReasonRegistryWithdrawn)
	})

	t.Run("expired registration is reported with status active and expiry", func(t *testing.T) {
		result, err := seed.matcher.Match(ctx, label(t, vision.RawLabel{
			NRN:          rawString(nrnExpired, 0.98),
			ProductName:  rawString("Lapsed Test Tablets 400 mg", 0.95),
			Manufacturer: rawString("Kestrel Pharma Ltd", 0.94),
			ExpiryDate:   rawString("May 2030", 0.9),
		}))
		require.NoError(t, err)

		assert.Equal(t, verify.MatchedWithMismatch, result.Outcome)
		assert.Equal(t, "Active", result.RegistryStatus)
		assert.Equal(t, verify.Disagree, result.Fields[verify.FieldRegistryStatus].Outcome)
		assert.Contains(t, result.Fields[verify.FieldRegistryStatus].Reasons, verify.ReasonRegistryExpired)
		assert.Contains(t, noteCodes(result.Notes), verify.ReasonRegistryExpired)
	})
}

// BenchmarkExactLookupAtDatasetSize measures the exact-NRN path, including the
// two dataset queries, against the seeded database. Target: under 50 ms.
func BenchmarkExactLookupAtDatasetSize(b *testing.B) {
	seed := seedRegistry(b)
	product, ingredients := seed.richProductB(b)

	ctx := context.Background()
	label := validateLabel(b, vision.RawLabel{
		NRN:          rawString(product.NRN, 0.98),
		ProductName:  rawString(product.Name, 0.97),
		Manufacturer: rawString(product.Manufacturer, 0.95),
		ExpiryDate:   rawString("May 2030", 0.94),
		Ingredients:  rawIngredients(ingredients),
	})

	b.ResetTimer()
	for range b.N {
		if _, err := seed.matcher.Match(ctx, label); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFuzzyLookupAtDatasetSize measures the trigram name plus manufacturer
// path against the seeded database. Target: under 200 ms.
func BenchmarkFuzzyLookupAtDatasetSize(b *testing.B) {
	seed := seedRegistry(b)

	ctx := context.Background()
	label := validateLabel(b, vision.RawLabel{
		ProductName:  rawString("Kestrel Test Cough Syrup 100 mg/5 mL", 0.95),
		Manufacturer: rawString("Kestrel Pharma Ltd", 0.94),
	})

	b.ResetTimer()
	for range b.N {
		result, err := seed.matcher.Match(ctx, label)
		if err != nil {
			b.Fatal(err)
		}
		if result.Outcome != verify.CandidateOnly {
			b.Fatalf("unexpected outcome %s", result.Outcome)
		}
	}
}

// richProductB is richProduct for benchmarks, which have no *testing.T.
func (s *seeded) richProductB(b *testing.B) (registry.Product, []registry.Ingredient) {
	b.Helper()

	for _, candidate := range s.products {
		if candidate.Status != "Active" || candidate.NRN == "" || candidate.Manufacturer == "" {
			continue
		}
		if candidate.ExpiresAt != nil && candidate.ExpiresAt.Before(fixedNow) {
			continue
		}
		for _, ingredient := range candidate.Ingredients {
			if ingredient.Strength.Parsed() && ingredient.Strength.Unit == "mg" && ingredient.Strength.PerUnit == "" {
				product, found, err := s.store.GetByNRN(context.Background(), candidate.NRN)
				require.NoError(b, err)
				require.True(b, found)
				ingredients, err := s.store.Ingredients(context.Background(), product.ID)
				require.NoError(b, err)
				return product, ingredients
			}
		}
	}
	b.Fatal("no active seeded product with a parsed mg ingredient")
	return registry.Product{}, nil
}

func label(t *testing.T, raw vision.RawLabel) vision.LabelData {
	t.Helper()
	return validateLabel(t, raw)
}

func validateLabel(tb testing.TB, raw vision.RawLabel) vision.LabelData {
	tb.Helper()

	label := vision.ValidateLabel(raw)
	require.True(tb, label.Identified(), "test label has no NRN or product name")
	return label
}

func rawString(value string, confidence float64) vision.RawField {
	return vision.RawField{Value: &value, Confidence: confidence, Source: 1}
}

func rawIngredients(ingredients []registry.Ingredient) []vision.RawIngredient {
	claims := make([]vision.RawIngredient, 0, len(ingredients))
	for _, ingredient := range ingredients {
		claims = append(claims, vision.RawIngredient{
			Name:       ingredient.NameRaw,
			Strength:   ingredient.StrengthRaw,
			Confidence: 0.95,
			Source:     1,
		})
	}
	return claims
}

func hasDisagreement(result verify.MatchResult) bool {
	for _, comparison := range result.Fields {
		if comparison.Outcome == verify.Disagree {
			return true
		}
	}
	return false
}

func noteCodes(notes []verify.Note) []string {
	codes := make([]string, 0, len(notes))
	for _, note := range notes {
		codes = append(codes, note.Code)
	}
	return codes
}
