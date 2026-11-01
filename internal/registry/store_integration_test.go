//go:build integration

package registry_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/clock"
	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/internal/registry"
	"github.com/benfaruna/medverify/migrations"
)

func TestUpsertProductIsIdempotentAndReplacesIngredientsOnChange(t *testing.T) {
	pool, store := integrationStore(t)
	ctx := context.Background()

	page := parseFixture(t, "drugs_p1.json")
	product := page.Products[0]

	first, err := store.UpsertProduct(ctx, product)
	require.NoError(t, err)
	assert.True(t, first.Inserted)
	assert.False(t, first.Changed)

	second, err := store.UpsertProduct(ctx, product)
	require.NoError(t, err)
	assert.False(t, second.Inserted)
	assert.False(t, second.Changed, "same payload hash should be a no-op")

	assert.Equal(t, 1, countRows(t, pool, "products"))
	assert.Equal(t, len(product.Ingredients), countRows(t, pool, "product_ingredients"))

	// A changed payload updates exactly one product and its ingredients.
	changed := product
	changed.Name = "Changed Product Name"
	changed.Raw = replaceJSONField(t, product.Raw, "product_name", changed.Name)
	changed.Ingredients = append([]registry.Ingredient(nil), product.Ingredients...)
	changed.Ingredients[0].NameNorm = "changed ingredient"

	third, err := store.UpsertProduct(ctx, changed)
	require.NoError(t, err)
	assert.False(t, third.Inserted)
	assert.True(t, third.Changed)

	assert.Equal(t, 1, countRows(t, pool, "products"))
	assert.Equal(t, len(product.Ingredients), countRows(t, pool, "product_ingredients"))

	var name string
	require.NoError(t, pool.QueryRow(ctx, `SELECT name FROM products WHERE source_id = $1`, product.SourceID).Scan(&name))
	assert.Equal(t, "Changed Product Name", name)
}

func TestSyncAbortsBeforeWritingWhenRowCountDrops(t *testing.T) {
	pool, store := integrationStore(t)
	ctx := context.Background()

	source := &stubSource{page: subsetPage(t, "drugs_p1.json", 10)}
	syncer := newSyncer(t, source, store, registry.AnomalyPolicy{
		MaxDropPercent:         30,
		MaxParseFailurePercent: 10,
	})

	first, err := syncer.Sync(ctx, registry.SyncOptions{Trigger: "test"})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", first.Status)
	assert.Equal(t, 10, first.RowsSeen)
	assert.Equal(t, 10, first.RowsAdded)
	assert.Equal(t, 10, countRows(t, pool, "products"))

	// 70% fewer rows: the guard must abort before touching the dataset.
	source.page = subsetPage(t, "drugs_p1.json", 3)
	second, err := syncer.Sync(ctx, registry.SyncOptions{Trigger: "test"})
	require.ErrorIs(t, err, registry.ErrAnomaly)
	assert.Equal(t, "aborted", second.Status)
	assert.True(t, second.Anomaly.Aborted())
	assert.Equal(t, 10, countRows(t, pool, "products"), "aborted sync must not delete or overwrite products")

	third, err := syncer.Sync(ctx, registry.SyncOptions{Trigger: "test", OverrideAnomaly: true})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", third.Status)
	assert.True(t, third.AnomalyOverridden)
	assert.Equal(t, 10, countRows(t, pool, "products"))

	var statuses []string
	rows, err := pool.Query(ctx, `SELECT status FROM sync_runs ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var status string
		require.NoError(t, rows.Scan(&status))
		statuses = append(statuses, status)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"succeeded", "aborted", "succeeded"}, statuses)
}

func TestSyncRecordsParseFailuresAndAbortsOverPolicy(t *testing.T) {
	pool, store := integrationStore(t)
	ctx := context.Background()

	// A page whose rows all lack a required field is a 100% parse failure.
	source := &stubSource{page: pageWithoutRequiredFields(t, 5)}
	syncer := newSyncer(t, source, store, registry.AnomalyPolicy{
		MaxDropPercent:         30,
		MaxParseFailurePercent: 10,
	})

	summary, err := syncer.Sync(ctx, registry.SyncOptions{Trigger: "test"})
	require.ErrorIs(t, err, registry.ErrAnomaly)
	assert.Equal(t, "aborted", summary.Status)
	assert.Equal(t, 5, summary.RowsSeen)
	assert.Equal(t, 5, summary.RowsFailed)
	assert.Equal(t, 0, countRows(t, pool, "products"))
}

func TestLeafletStorageRoundTrip(t *testing.T) {
	pool, store := integrationStore(t)
	ctx := context.Background()

	product := parseFixture(t, "drugs_p1.json").Products[0]
	product.SmpcURL = "https://admin.greenbook.nafdac.gov.ng/leaflet.pdf"
	_, err := store.UpsertProduct(ctx, product)
	require.NoError(t, err)

	targets, err := store.ListProductsForLeafletSync(ctx, 10)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, product.SourceID, targets[0].SourceID)
	assert.Equal(t, product.SmpcURL, targets[0].URL)

	fetchedAt := time.Now().UTC()
	docID, err := store.SaveSMPCDocument(ctx, registry.SMPCDocument{
		ProductID:        targets[0].ProductID,
		SourceURL:        targets[0].URL,
		SHA256:           "abc123",
		StorageKey:       "leaflets/x/abc123.pdf",
		FetchedAt:        &fetchedAt,
		ExtractionMethod: "pdftotext",
		Status:           registry.LeafletStatusOK,
	})
	require.NoError(t, err)

	require.NoError(t, store.ReplaceSMPCSections(ctx, docID, []registry.SMPCSection{
		{Code: "1", Heading: "NAME OF THE MEDICINAL PRODUCT", Text: "Example", PageStart: 1},
		{Code: "2", Heading: "COMPOSITION", Text: "500 mg", PageStart: 2},
	}))

	doc, found, err := store.GetSMPCDocument(ctx, targets[0].ProductID, targets[0].URL)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, docID, doc.ID)
	assert.Equal(t, "abc123", doc.SHA256)
	assert.Equal(t, 2, countRows(t, pool, "smpc_sections"))

	// A successful document is not selected again; a changed product is.
	targets, err = store.ListProductsForLeafletSync(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, targets)
}

func integrationStore(t *testing.T) (*pgxpool.Pool, *registry.Store) {
	t.Helper()

	databaseURL := testkit.Postgres(t)
	ctx := context.Background()
	require.NoError(t, migrations.Up(ctx, databaseURL))

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return pool, registry.NewStore(pool)
}

func newSyncer(t *testing.T, source registry.PageSource, store *registry.Store, policy registry.AnomalyPolicy) *registry.Syncer {
	t.Helper()

	syncer, err := registry.NewSyncer(registry.SyncerOptions{
		Source:   source,
		Store:    store,
		Policy:   policy,
		PageSize: 100,
		Clock:    &clock.Fixed{Current: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)},
	})
	require.NoError(t, err)
	return syncer
}

type stubSource struct {
	page []byte
}

func (s *stubSource) FetchProducts(context.Context, registry.PageRequest) ([]byte, error) {
	return s.page, nil
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()

	var count int
	// The table name is a test constant, not user input.
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&count))
	return count
}

func subsetPage(t *testing.T, fixture string, rows int) []byte {
	t.Helper()

	data, err := os.ReadFile(fixturePath(fixture))
	require.NoError(t, err)

	var page map[string]any
	require.NoError(t, json.Unmarshal(data, &page))
	items, ok := page["data"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(items), rows)
	page["data"] = items[:rows]
	page["recordsTotal"] = rows
	page["recordsFiltered"] = rows

	out, err := json.Marshal(page)
	require.NoError(t, err)
	return out
}

func pageWithoutRequiredFields(t *testing.T, rows int) []byte {
	t.Helper()

	items := make([]map[string]any, 0, rows)
	for i := range rows {
		items = append(items, map[string]any{
			"product_id":   i + 1,
			"product_name": "",
			"NAFDAC":       "04-0000",
			"status":       "",
		})
	}
	page := map[string]any{
		"recordsTotal":    rows,
		"recordsFiltered": rows,
		"data":            items,
	}
	out, err := json.Marshal(page)
	require.NoError(t, err)
	return out
}

func replaceJSONField(t *testing.T, raw []byte, field, value string) []byte {
	t.Helper()

	var item map[string]any
	require.NoError(t, json.Unmarshal(raw, &item))
	item[field] = value
	out, err := json.Marshal(item)
	require.NoError(t, err)
	return out
}

func fixturePath(name string) string {
	return filepath.Join("..", "..", "testdata", "greenbook", "raw", name)
}
