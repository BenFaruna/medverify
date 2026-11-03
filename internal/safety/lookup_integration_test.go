//go:build integration

package safety_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/safety"
)

// seedLargeWatchlist loads 10,000 synthetic product entries so the lookup
// budget can be checked against a realistic table instead of an empty one.
func seedLargeWatchlist(tb testing.TB) *safety.Store {
	tb.Helper()

	pool := safetyPool(tb)
	ctx := context.Background()

	var sourceID int64
	require.NoError(tb, pool.QueryRow(ctx, `
		INSERT INTO watchlist_sources (issuing_body, title, source_url, issued_at)
		VALUES ('NAFDAC', 'Synthetic benchmark list', 'https://example.test/nafdac/benchmark', '2026-09-01')
		RETURNING id`).Scan(&sourceID))

	_, err := pool.Exec(ctx, `
		INSERT INTO watchlist_entries (
			source_id, kind, nrn, product_name, product_name_norm, reason, reviewed_by
		)
		SELECT $1, 'alert', 'TT4-' || lpad(g::text, 5, '0'),
		       'Kestrel Test Product ' || g, 'kestrel test product ' || g,
		       'Synthetic benchmark entry', 'Benchmark Reviewer'
		FROM generate_series(1, 10000) AS g`, sourceID)
	require.NoError(tb, err)
	require.NoError(tb, pool.Ping(ctx))

	if _, err := pool.Exec(ctx, `ANALYZE watchlist_entries`); err != nil {
		tb.Logf("could not analyze watchlist table: %v", err)
	}
	return safety.NewStore(pool)
}

func BenchmarkProductEntriesWithTenThousandEntries(b *testing.B) {
	store := seedLargeWatchlist(b)
	ctx := context.Background()

	b.Run("by_nrn", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			entries, err := store.ProductEntries(ctx, safety.ProductQuery{
				NRN: "TT4-05000", Limit: 20, Now: testNow,
			})
			if err != nil {
				b.Fatal(err)
			}
			if len(entries) != 1 {
				b.Fatalf("expected one entry, got %d", len(entries))
			}
		}
	})

	b.Run("by_name", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			entries, err := store.ProductEntries(ctx, safety.ProductQuery{
				NameNorm: "kestrel test product 5000", MinScore: 0.85, Limit: 20, Now: testNow,
			})
			if err != nil {
				b.Fatal(err)
			}
			if len(entries) == 0 {
				b.Fatal("expected at least one name candidate")
			}
		}
	})
}

// TestWatchlistLookupStaysUnderFiftyMilliseconds is the executable form of the
// phase 06 budget: a scan may not sit on a watchlist lookup for 50 ms.
func TestWatchlistLookupStaysUnderFiftyMilliseconds(t *testing.T) {
	store := seedLargeWatchlist(t)
	ctx := context.Background()

	queries := map[string]safety.ProductQuery{
		"nrn":  {NRN: "TT4-04242", Limit: 20, Now: testNow},
		"name": {NameNorm: "kestrel test product 4242", MinScore: 0.85, Limit: 20, Now: testNow},
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			var worst time.Duration
			for i := 0; i < 20; i++ {
				started := time.Now()
				entries, err := store.ProductEntries(ctx, query)
				elapsed := time.Since(started)
				require.NoError(t, err)
				require.NotEmpty(t, entries)
				if elapsed > worst {
					worst = elapsed
				}
			}
			t.Logf("slowest %s lookup over 10,000 entries: %s", name, worst)
			assert.Less(t, worst, 50*time.Millisecond)
		})
	}
}
