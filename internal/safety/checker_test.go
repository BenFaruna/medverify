package safety_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/benfaruna/medverify/internal/registry"
	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/verify"
	"github.com/benfaruna/medverify/internal/vision"
)

var checkerNow = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

type fakeWatchlist struct {
	productEntries    []safety.Entry
	ingredientEntries []safety.Entry
	productQueries    []safety.ProductQuery
	ingredientQueries [][]string
}

func (f *fakeWatchlist) ProductEntries(_ context.Context, query safety.ProductQuery) ([]safety.Entry, error) {
	f.productQueries = append(f.productQueries, query)
	return f.productEntries, nil
}

func (f *fakeWatchlist) IngredientEntries(_ context.Context, names []string, _ time.Time) ([]safety.Entry, error) {
	f.ingredientQueries = append(f.ingredientQueries, names)
	return f.ingredientEntries, nil
}

func newChecker(t *testing.T, watchlist safety.Watchlist) *safety.Checker {
	t.Helper()
	config, err := safety.LoadConfig("")
	require.NoError(t, err)
	checker, err := safety.NewChecker(safety.CheckerOptions{
		Watchlist: watchlist,
		Config:    config,
		Now:       func() time.Time { return checkerNow },
	})
	require.NoError(t, err)
	return checker
}

func productEntry(id int64, kind safety.EntryKind) safety.Entry {
	return safety.Entry{
		ID:         id,
		Kind:       kind,
		Active:     true,
		ReviewedBy: "Test Reviewer",
		Source: safety.SourceDocument{
			ID:          1,
			IssuingBody: "NAFDAC",
			Title:       "Public alert on a test batch",
			URL:         "https://example.test/nafdac/alerts/2026-09-01",
			IssuedAt:    time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		},
	}
}

func ingredientEntry(id int64, kind safety.EntryKind, ingredient string) safety.Entry {
	entry := productEntry(id, kind)
	entry.IngredientNorm = ingredient
	return entry
}

func confirmedMatch(nrn string, ingredients ...verify.IngredientRef) verify.MatchResult {
	return verify.MatchResult{
		Outcome: verify.MatchedConsistent,
		Product: &verify.ProductRef{
			SourceID:    "p1",
			NRN:         nrn,
			Name:        "Kestrel Test Cold Relief Syrup",
			Status:      "Active",
			Ingredients: ingredients,
		},
		Fields:         map[string]verify.FieldComparison{},
		RegistryStatus: "Active",
	}
}

func labelWithNRN(nrn string) vision.LabelData {
	return vision.LabelData{NRN: vision.Field[string]{Value: &nrn, Confidence: 0.95}}
}

func claim(name, strength string, confidence float64) vision.IngredientClaim {
	return vision.IngredientClaim{
		NameRaw:     name,
		NameNorm:    registry.NormalizeIngredient(name),
		StrengthRaw: strength,
		Strength:    registry.ParseStrength(strength),
		Confidence:  confidence,
	}
}

func flagsOfType(flags []safety.Flag, flagType safety.FlagType) []safety.Flag {
	var matched []safety.Flag
	for _, flag := range flags {
		if flag.Type == flagType {
			matched = append(matched, flag)
		}
	}
	return matched
}

func TestWatchlistProductByNRNCarriesTheSourceDocument(t *testing.T) {
	t.Parallel()

	entry := productEntry(7, safety.EntryAlert)
	entry.NRN = "TT4-7001"
	watchlist := &fakeWatchlist{productEntries: []safety.Entry{entry}}

	flags, err := newChecker(t, watchlist).Check(context.Background(), confirmedMatch("TT4-7001"), labelWithNRN("TT4-7001"))
	require.NoError(t, err)
	require.Len(t, flags, 1)

	flag := flags[0]
	assert.Equal(t, safety.WatchlistProduct, flag.Type)
	assert.Equal(t, safety.SeverityHigh, flag.Severity)
	assert.Equal(t, safety.ReasonWatchlistAlert, flag.ReasonCode)
	assert.Equal(t, safety.SourceWatchlistEntry, flag.Source.Kind)
	assert.Equal(t, int64(7), flag.Source.WatchlistEntryID)
	assert.Equal(t, "https://example.test/nafdac/alerts/2026-09-01", flag.Source.URL)
	assert.Equal(t, "https://example.test/nafdac/alerts/2026-09-01", flag.Evidence["source_url"])
	assert.Equal(t, "2026-09-01", flag.Evidence["issued_at"])

	require.Len(t, watchlist.productQueries, 1)
	assert.Equal(t, "TT4-7001", watchlist.productQueries[0].NRN)
}

func TestWatchlistProductWithADifferentNRNProducesNoFlag(t *testing.T) {
	t.Parallel()

	entry := productEntry(7, safety.EntryAlert)
	entry.NRN = "TT4-7001"

	flags, err := newChecker(t, &fakeWatchlist{productEntries: []safety.Entry{entry}}).
		Check(context.Background(), confirmedMatch("TT4-7002"), labelWithNRN("TT4-7002"))
	require.NoError(t, err)
	assert.Empty(t, flags)
}

func TestNameEntryNeedsAManufacturerOnTheLabel(t *testing.T) {
	t.Parallel()

	entry := productEntry(8, safety.EntryAlert)
	entry.ProductName = "Kestrel Test Cold Relief Syrup"
	entry.ProductNameNorm = verify.NormalizeName(entry.ProductName)
	entry.Manufacturer = "Kestrel Pharma Ltd"
	entry.ManufacturerNorm = verify.NormalizeManufacturer(entry.Manufacturer)

	name := "Kestrel Test Cold Relief Syrup"
	manufacturer := "Kestrel Pharma Ltd"

	label := vision.LabelData{
		ProductName:  vision.Field[string]{Value: &name, Confidence: 0.9},
		Manufacturer: vision.Field[string]{Value: &manufacturer, Confidence: 0.9},
	}
	flags, err := newChecker(t, &fakeWatchlist{productEntries: []safety.Entry{entry}}).
		Check(context.Background(), verify.MatchResult{Outcome: verify.NotFoundInDataset}, label)
	require.NoError(t, err)
	require.Len(t, flags, 1)
	assert.Equal(t, safety.WatchlistProduct, flags[0].Type)

	// The same brand with no readable manufacturer must not be attributed.
	label.Manufacturer = vision.Field[string]{}
	flags, err = newChecker(t, &fakeWatchlist{productEntries: []safety.Entry{entry}}).
		Check(context.Background(), verify.MatchResult{Outcome: verify.NotFoundInDataset}, label)
	require.NoError(t, err)
	assert.Empty(t, flags, "a bare brand name is not enough to name a company")
}

func TestIngredientSynonymAndSaltFormMatchTheBaseEntry(t *testing.T) {
	t.Parallel()

	entry := ingredientEntry(9, safety.EntryBannedIngredient, "paracetamol")
	watchlist := &fakeWatchlist{ingredientEntries: []safety.Entry{entry}}

	label := vision.LabelData{
		Ingredients: []vision.IngredientClaim{
			claim("Acetaminophen", "500 mg", 0.95),
			claim("Paracetamol hydrochloride", "500 mg", 0.95),
		},
	}
	flags, err := newChecker(t, watchlist).Check(context.Background(), verify.MatchResult{}, label)
	require.NoError(t, err)
	require.Len(t, flags, 1)
	assert.Equal(t, safety.WatchlistIngredient, flags[0].Type)
	assert.Equal(t, safety.ReasonWatchlistBannedIngredient, flags[0].ReasonCode)
	assert.Equal(t, "paracetamol", flags[0].Evidence["ingredient"])

	require.NotEmpty(t, watchlist.ingredientQueries)
	for _, name := range watchlist.ingredientQueries[0] {
		assert.Equal(t, "paracetamol", name, "both spellings must normalize to one lookup key")
	}
}

func TestInactiveOrExpiredEntryProducesNoFlag(t *testing.T) {
	t.Parallel()

	entry := productEntry(10, safety.EntryAlert)
	entry.NRN = "TT4-7001"
	entry.Active = false

	flags, err := newChecker(t, &fakeWatchlist{productEntries: []safety.Entry{entry}}).
		Check(context.Background(), confirmedMatch("TT4-7001"), labelWithNRN("TT4-7001"))
	require.NoError(t, err)
	assert.Empty(t, flags)

	entry.Active = true
	expired := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	entry.EffectiveUntil = &expired

	flags, err = newChecker(t, &fakeWatchlist{productEntries: []safety.Entry{entry}}).
		Check(context.Background(), confirmedMatch("TT4-7001"), labelWithNRN("TT4-7001"))
	require.NoError(t, err)
	assert.Empty(t, flags)
}

func TestLabelIngredientDifferencesBecomeOneFlagEach(t *testing.T) {
	t.Parallel()

	match := confirmedMatch("TT4-7010",
		verify.IngredientRef{Name: "paracetamol", Strength: "500 mg"},
		verify.IngredientRef{Name: "caffeine", Strength: "30 mg"},
	)
	label := vision.LabelData{
		Ingredients: []vision.IngredientClaim{
			claim("Paracetamol", "500 mg", 0.95),
			claim("Cetirizine", "10 mg", 0.95),
		},
	}

	flags, err := newChecker(t, &fakeWatchlist{}).Check(context.Background(), match, label)
	require.NoError(t, err)

	undeclared := flagsOfType(flags, safety.UndeclaredIngredient)
	require.Len(t, undeclared, 1)
	assert.Equal(t, "cetirizine", undeclared[0].Evidence["ingredient"])
	assert.Equal(t, safety.SourceRegistry, undeclared[0].Source.Kind)
	assert.Equal(t, "TT4-7010", undeclared[0].Source.NRN)

	missing := flagsOfType(flags, safety.MissingIngredient)
	require.Len(t, missing, 1)
	assert.Equal(t, "caffeine", missing[0].Evidence["ingredient"])
}

func TestLowConfidenceIngredientsDoNotProduceAbsenceFlags(t *testing.T) {
	t.Parallel()

	match := confirmedMatch("TT4-7010",
		verify.IngredientRef{Name: "paracetamol", Strength: "500 mg"},
		verify.IngredientRef{Name: "caffeine", Strength: "30 mg"},
	)
	label := vision.LabelData{
		Ingredients: []vision.IngredientClaim{
			claim("Paracetamol", "500 mg", 0.3),
			claim("Cetirizine", "10 mg", 0.2),
		},
	}

	flags, err := newChecker(t, &fakeWatchlist{}).Check(context.Background(), match, label)
	require.NoError(t, err)
	assert.Empty(t, flags, "a poor photo must stay silent, not accuse the pack")
}

func TestStrengthDeviationUsesTheConfiguredTolerance(t *testing.T) {
	t.Parallel()

	match := confirmedMatch("TT4-7011",
		verify.IngredientRef{Name: "paracetamol", Strength: "500 mg"},
	)

	within := vision.LabelData{Ingredients: []vision.IngredientClaim{claim("Paracetamol", "0.5 g", 0.95)}}
	flags, err := newChecker(t, &fakeWatchlist{}).Check(context.Background(), match, within)
	require.NoError(t, err)
	assert.Empty(t, flags)

	outside := vision.LabelData{Ingredients: []vision.IngredientClaim{claim("Paracetamol", "250 mg", 0.95)}}
	flags, err = newChecker(t, &fakeWatchlist{}).Check(context.Background(), match, outside)
	require.NoError(t, err)
	require.Len(t, flags, 1)
	assert.Equal(t, safety.StrengthDeviation, flags[0].Type)
	assert.Equal(t, "paracetamol", flags[0].Evidence["ingredient"])
	assert.Equal(t, "250 mg", flags[0].Evidence["label_strength"])
	assert.Equal(t, "500 mg", flags[0].Evidence["registry_strength"])
	assert.Equal(t, "0.05", flags[0].Evidence["tolerance"], "the threshold is never hidden")
}

func TestNoFlagsWhenEverythingAgrees(t *testing.T) {
	t.Parallel()

	match := confirmedMatch("TT4-7012",
		verify.IngredientRef{Name: "paracetamol", Strength: "500 mg"},
	)
	label := vision.LabelData{
		NRN:         vision.Field[string]{Value: strPtr("TT4-7012"), Confidence: 1},
		Ingredients: []vision.IngredientClaim{claim("Paracetamol", "500 mg", 0.99)},
	}

	flags, err := newChecker(t, &fakeWatchlist{}).Check(context.Background(), match, label)
	require.NoError(t, err)
	assert.Empty(t, flags)
}

func TestRegistrationProblemIsSourcedToTheRegistryRecord(t *testing.T) {
	t.Parallel()

	match := confirmedMatch("TT4-7013")
	match.RegistryStatus = "Expired"
	match.Product.Status = "Expired"
	match.Fields[verify.FieldRegistryStatus] = verify.FieldComparison{
		Outcome:  verify.Disagree,
		Registry: "Expired",
		Reasons:  []string{verify.ReasonRegistryExpired},
	}

	flags, err := newChecker(t, &fakeWatchlist{}).Check(context.Background(), match, labelWithNRN("TT4-7013"))
	require.NoError(t, err)
	require.Len(t, flags, 1)
	assert.Equal(t, safety.RegistrationProblem, flags[0].Type)
	assert.Equal(t, verify.ReasonRegistryExpired, flags[0].ReasonCode)
	assert.Equal(t, safety.SourceRegistry, flags[0].Source.Kind)
	assert.Equal(t, "p1", flags[0].Source.RegistrySourceID)
	assert.Equal(t, "TT4-7013", flags[0].Source.NRN)
}

// TestEveryFlagIsSourced is the phase 06 property: whatever the input, a flag
// that comes back cites something a person can open.
func TestEveryFlagIsSourced(t *testing.T) {
	t.Parallel()

	entries := []safety.Entry{
		func() safety.Entry { e := productEntry(1, safety.EntryAlert); e.NRN = "TT4-7001"; return e }(),
		func() safety.Entry {
			e := productEntry(2, safety.EntryAlert)
			e.ProductName, e.ProductNameNorm = "Kestrel Test Cold Relief Syrup", verify.NormalizeName("Kestrel Test Cold Relief Syrup")
			e.Manufacturer, e.ManufacturerNorm = "Kestrel Pharma Ltd", verify.NormalizeManufacturer("Kestrel Pharma Ltd")
			return e
		}(),
		ingredientEntry(3, safety.EntryBannedIngredient, "paracetamol"),
		ingredientEntry(4, safety.EntryRestrictedIngredient, "codeine"),
	}
	checker := newChecker(t, &fakeWatchlist{productEntries: entries, ingredientEntries: entries})

	names := []string{"", "paracetamol", "caffeine", "Paracetamol hydrochloride", "acetaminophen"}
	strengths := []string{"", "500 mg", "0.5 g", "250 mg"}
	nrns := []string{"", "TT4-7001", "TT4-7002"}
	products := []string{"", "Kestrel Test Cold Relief Syrup", "Halcyon Test Pain Tablets"}
	manufacturers := []string{"", "Kestrel Pharma Ltd", "Halcyon Laboratories Ltd"}

	rapid.Check(t, func(rt *rapid.T) {
		name := rapid.SampledFrom(names).Draw(rt, "ingredient")
		label := vision.LabelData{
			NRN:          vision.Field[string]{Value: strPtr(rapid.SampledFrom(nrns).Draw(rt, "nrn")), Confidence: 1},
			ProductName:  vision.Field[string]{Value: strPtr(rapid.SampledFrom(products).Draw(rt, "product")), Confidence: 1},
			Manufacturer: vision.Field[string]{Value: strPtr(rapid.SampledFrom(manufacturers).Draw(rt, "manufacturer")), Confidence: 1},
			Ingredients: []vision.IngredientClaim{{
				NameRaw:     name,
				NameNorm:    registry.NormalizeIngredient(name),
				StrengthRaw: rapid.SampledFrom(strengths).Draw(rt, "strength"),
				Strength:    registry.ParseStrength(rapid.SampledFrom(strengths).Draw(rt, "strength_parsed")),
				Confidence:  rapid.Float64Range(0, 1).Draw(rt, "confidence"),
			}},
		}

		flags, err := checker.Check(context.Background(), confirmedMatch(
			rapid.SampledFrom([]string{"TT4-7001", "TT4-7002"}).Draw(rt, "match_nrn"),
			verify.IngredientRef{Name: rapid.SampledFrom([]string{"paracetamol", "caffeine"}).Draw(rt, "registered"), Strength: "500 mg"},
		), label)
		require.NoError(rt, err)

		for _, flag := range flags {
			require.True(rt, flag.Type.Valid(), "unknown flag type %q", flag.Type)
			require.True(rt, flag.Severity.Valid(), "flag %s has no severity", flag.Type)
			require.NotEmpty(rt, flag.ReasonCode, "flag %s has no reason code", flag.Type)
			switch flag.Source.Kind {
			case safety.SourceWatchlistEntry:
				require.NotEmpty(rt, flag.Source.URL, "watchlist flag %s cites no URL", flag.Type)
			case safety.SourceRegistry:
				require.NotEmpty(rt, flag.Source.NRN, "registry flag %s cites no NRN", flag.Type)
			default:
				rt.Fatalf("flag %s has no source", flag.Type)
			}
		}
	})
}

func strPtr(value string) *string { return &value }
