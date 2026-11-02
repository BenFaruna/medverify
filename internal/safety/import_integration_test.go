//go:build integration

package safety_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/verify"
)

func TestFixtureCSVImportsAndAnswersBothLookupPaths(t *testing.T) {
	pool := safetyPool(t)
	ctx := context.Background()

	file, err := os.Open("../../testdata/watchlist/entries.csv")
	require.NoError(t, err)
	defer file.Close()

	entries, err := safety.ParseImportCSV(file)
	require.NoError(t, err)
	require.Len(t, entries, 20)

	store := safety.NewStore(pool)
	summary, err := store.Import(ctx, entries)
	require.NoError(t, err)
	assert.Equal(t, 20, summary.Entries)
	assert.Equal(t, 5, summary.Sources, "entries from the same notice share one source row")

	byNRN, err := store.ProductEntries(ctx, safety.ProductQuery{
		NRN:   "TT4-7001",
		Limit: 10,
		Now:   testNow,
	})
	require.NoError(t, err)
	require.Len(t, byNRN, 1)
	assert.Equal(t, safety.EntryAlert, byNRN[0].Kind)
	assert.Equal(t, "https://example.test/nafdac/alerts/2026-09-01", byNRN[0].Source.URL)
	assert.Equal(t, "Test Reviewer", byNRN[0].ReviewedBy)
	assert.True(t, byNRN[0].Active)

	byName, err := store.ProductEntries(ctx, safety.ProductQuery{
		NameNorm: verify.NormalizeName("Kestrel Test Cold Relief Syrup"),
		Limit:    10,
		Now:      testNow,
	})
	require.NoError(t, err)
	require.Len(t, byName, 1)
	assert.Equal(t, "Kestrel Test Cold Relief Syrup", byName[0].ProductName)

	byIngredient, err := store.IngredientEntries(ctx, []string{"codeine"}, testNow)
	require.NoError(t, err)
	require.Len(t, byIngredient, 1)
	assert.Equal(t, safety.EntryRestrictedIngredient, byIngredient[0].Kind)

	// Importing an unrecognised NRN must not match something else by accident.
	missing, err := store.ProductEntries(ctx, safety.ProductQuery{NRN: "TT4-9999", Limit: 10, Now: testNow})
	require.NoError(t, err)
	assert.Empty(t, missing)

	// Re-importing the same reviewed file is idempotent for sources and
	// entries, so a retried command does not double the watchlist.
	repeated, err := store.Import(ctx, entries)
	require.NoError(t, err)
	assert.Zero(t, repeated.Sources)
	assert.Zero(t, repeated.Entries)

	total, active, err := store.Counts(ctx)
	require.NoError(t, err)
	assert.Equal(t, 20, total)
	assert.Equal(t, 20, active)
}
