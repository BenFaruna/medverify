//go:build integration

package safety_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/migrations"
)

var testNow = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func safetyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	databaseURL := testkit.Postgres(t)
	ctx := context.Background()
	require.NoError(t, migrations.Up(ctx, databaseURL))

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func TestWatchlistProvenanceAndShapeAreEnforcedByTheDatabase(t *testing.T) {
	pool := safetyPool(t)
	ctx := context.Background()

	// A source with no URL is rejected by the database, not by Go.
	_, err := pool.Exec(ctx, `
		INSERT INTO watchlist_sources (issuing_body, title, source_url, issued_at)
		VALUES ('NAFDAC', 'Alert', '', '2026-10-01')`)
	require.Error(t, err, "a source without a URL must not be storable")

	_, err = pool.Exec(ctx, `
		INSERT INTO watchlist_sources (issuing_body, title, source_url, issued_at)
		VALUES ('', 'Alert', 'https://example.test/a', '2026-10-01')`)
	require.Error(t, err, "a source without an issuing body must not be storable")

	// Valid source for the entry checks below.
	var sourceID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO watchlist_sources (issuing_body, title, source_url, issued_at)
		VALUES ('NAFDAC', 'Alert', 'https://example.test/alert', '2026-10-01')
		RETURNING id`).Scan(&sourceID))

	// An entry cannot exist without a source document.
	_, err = pool.Exec(ctx, `
		INSERT INTO watchlist_entries (source_id, kind, nrn, reason, reviewed_by)
		VALUES (NULL, 'alert', '04-1111', 'reason', 'reviewer')`)
	require.Error(t, err, "an entry without a source must not be storable")

	_, err = pool.Exec(ctx, `
		INSERT INTO watchlist_entries (source_id, kind, nrn, reason, reviewed_by)
		VALUES (999999, 'alert', '04-1111', 'reason', 'reviewer')`)
	require.Error(t, err, "an entry must reference an existing source")

	// A product entry with no NRN and no product name is meaningless.
	_, err = pool.Exec(ctx, `
		INSERT INTO watchlist_entries (source_id, kind, reason, reviewed_by)
		VALUES ($1, 'alert', 'reason', 'reviewer')`, sourceID)
	require.Error(t, err, "an entry with no match key must not be storable")

	// Ingredient entries cannot carry an NRN.
	_, err = pool.Exec(ctx, `
		INSERT INTO watchlist_entries (source_id, kind, nrn, ingredient_norm, reason, reviewed_by)
		VALUES ($1, 'banned_ingredient', '04-1111', 'phenylpropanolamine', 'reason', 'reviewer')`, sourceID)
	require.Error(t, err, "an ingredient entry must not carry an NRN")

	// A valid entry still inserts.
	_, err = pool.Exec(ctx, `
		INSERT INTO watchlist_entries (source_id, kind, nrn, reason, reviewed_by)
		VALUES ($1, 'alert', '04-1111', 'reason', 'reviewer')`, sourceID)
	require.NoError(t, err)
}

func TestImportLookupDeactivateAndLifecycle(t *testing.T) {
	pool := safetyPool(t)
	store := safety.NewStore(pool)
	ctx := context.Background()

	issued := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	alertSource := safety.SourceDocument{
		IssuingBody: "NAFDAC",
		Title:       "Public alert on a recalled batch",
		URL:         "https://example.test/nafdac/alerts/1",
		IssuedAt:    issued,
	}
	ingredientSource := safety.SourceDocument{
		IssuingBody: "NAFDAC",
		Title:       "Banned and restricted ingredients list",
		URL:         "https://example.test/nafdac/lists/1",
		IssuedAt:    issued,
	}

	pastWindow := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	reviewOverdue := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	entries := []safety.ImportEntry{
		{
			Kind: safety.EntryAlert, NRN: "04-1111", Reason: "recalled batch",
			ReviewedBy: "Test Reviewer", Source: alertSource,
			ReviewDueAt: &reviewOverdue,
		},
		{
			Kind: safety.EntryWithdrawal, NRN: "04-2222", Reason: "withdrawn product",
			ReviewedBy: "Test Reviewer", Source: alertSource,
		},
		{
			Kind:        safety.EntryAlert,
			ProductName: "Kestrel Test Cough Syrup", ProductNameNorm: "kestrel test cough syrup",
			Manufacturer: "Kestrel Pharma Ltd", ManufacturerNorm: "kestrel",
			Reason: "unregistered brand alert", ReviewedBy: "Test Reviewer",
			Source: ingredientSource,
		},
		{
			Kind: safety.EntryBannedIngredient, IngredientNorm: "phenylpropanolamine",
			Reason: "banned ingredient", ReviewedBy: "Test Reviewer",
			Source: ingredientSource,
		},
		{
			Kind: safety.EntryRestrictedIngredient, IngredientNorm: "codeine",
			Reason: "restricted ingredient", ReviewedBy: "Test Reviewer",
			Source: ingredientSource,
		},
		{
			Kind: safety.EntryAlert, NRN: "04-3333", Reason: "old alert",
			EffectiveUntil: &pastWindow, ReviewedBy: "Test Reviewer",
			Source: alertSource,
		},
	}

	summary, err := store.Import(ctx, entries)
	require.NoError(t, err)
	assert.Equal(t, 6, summary.Entries)
	assert.Equal(t, 2, summary.Sources, "one source row per document, not per entry")

	byNRN, err := store.ProductEntries(ctx, safety.ProductQuery{NRN: "04-1111", Limit: 10, Now: testNow})
	require.NoError(t, err)
	require.Len(t, byNRN, 1)
	assert.Equal(t, safety.EntryAlert, byNRN[0].Kind)
	assert.Equal(t, "recalled batch", byNRN[0].Reason)
	assert.Equal(t, "Test Reviewer", byNRN[0].ReviewedBy)
	assert.Equal(t, alertSource.URL, byNRN[0].Source.URL)
	assert.Equal(t, alertSource.IssuingBody, byNRN[0].Source.IssuingBody)

	// An entry whose effective window closed is not live.
	expiredWindow, err := store.ProductEntries(ctx, safety.ProductQuery{NRN: "04-3333", Limit: 10, Now: testNow})
	require.NoError(t, err)
	assert.Empty(t, expiredWindow)

	// Name lookup returns the entry that has no NRN.
	byName, err := store.ProductEntries(ctx, safety.ProductQuery{
		NameNorm: "kestrel test cough syrup", Limit: 10, Now: testNow,
	})
	require.NoError(t, err)
	require.NotEmpty(t, byName)
	assert.Equal(t, "Kestrel Test Cough Syrup", byName[0].ProductName)

	unknown, err := store.ProductEntries(ctx, safety.ProductQuery{NRN: "04-9999", Limit: 10, Now: testNow})
	require.NoError(t, err)
	assert.Empty(t, unknown)

	byIngredient, err := store.IngredientEntries(ctx, []string{"phenylpropanolamine", "codeine"}, testNow)
	require.NoError(t, err)
	require.Len(t, byIngredient, 2)
	assert.ElementsMatch(t,
		[]safety.EntryKind{safety.EntryBannedIngredient, safety.EntryRestrictedIngredient},
		[]safety.EntryKind{byIngredient[0].Kind, byIngredient[1].Kind})

	total, active, err := store.Counts(ctx)
	require.NoError(t, err)
	assert.Equal(t, 6, total)
	assert.Equal(t, 6, active)

	stale, err := store.Stale(ctx, testNow)
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, "04-1111", stale[0].NRN)

	deactivated, err := store.Deactivate(ctx, byNRN[0].ID)
	require.NoError(t, err)
	assert.True(t, deactivated)

	afterDeactivate, err := store.ProductEntries(ctx, safety.ProductQuery{NRN: "04-1111", Limit: 10, Now: testNow})
	require.NoError(t, err)
	assert.Empty(t, afterDeactivate)

	total, active, err = store.Counts(ctx)
	require.NoError(t, err)
	assert.Equal(t, 6, total)
	assert.Equal(t, 5, active)

	missing, err := store.Deactivate(ctx, 999999)
	require.NoError(t, err)
	assert.False(t, missing)
}

func TestImportIsAtomic(t *testing.T) {
	pool := safetyPool(t)
	store := safety.NewStore(pool)
	ctx := context.Background()

	source := safety.SourceDocument{
		IssuingBody: "NAFDAC",
		Title:       "Alert",
		URL:         "https://example.test/nafdac/alerts/atomic",
		IssuedAt:    time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	}

	_, err := store.Import(ctx, []safety.ImportEntry{
		{Kind: safety.EntryAlert, NRN: "04-1111", Reason: "valid", ReviewedBy: "r", Source: source},
		// Invalid shape: an ingredient entry cannot carry an NRN.
		{Kind: safety.EntryBannedIngredient, NRN: "04-2222", IngredientNorm: "codeine", Reason: "invalid", ReviewedBy: "r", Source: source},
	})
	require.Error(t, err)

	var entries, sources int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM watchlist_entries`).Scan(&entries))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM watchlist_sources`).Scan(&sources))
	assert.Zero(t, entries, "a failed import must not leave a partial entry")
	assert.Zero(t, sources, "a failed import must not leave a source behind")
}
