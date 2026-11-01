package registry_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/registry"
)

var update = flag.Bool("update", false, "update golden files")

var fixtureTime = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func TestParsePageGolden(t *testing.T) {
	t.Parallel()

	cases := []struct {
		fixture string
		rows    int
	}{
		{fixture: "drugs_p1.json", rows: 3},
		{fixture: "vaccines_p1.json", rows: 3},
		{fixture: "missing_nrn_p1.json", rows: 3},
		{fixture: "search_no_results.json", rows: 3},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()

			page := parseFixture(t, tc.fixture)
			rows := page.Products
			if len(rows) > tc.rows {
				rows = rows[:tc.rows]
			}

			got, err := json.MarshalIndent(rows, "", "  ")
			require.NoError(t, err)
			got = append(got, '\n')

			golden := filepath.Join("..", "..", "testdata", "greenbook", "golden", strings.TrimSuffix(tc.fixture, ".json")+".json")
			if *update {
				require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o750))
				require.NoError(t, os.WriteFile(golden, got, 0o600))
				return
			}

			want, err := os.ReadFile(golden)
			require.NoError(t, err, "missing golden file; run go test -run TestParsePageGolden -update")
			assert.JSONEq(t, string(want), string(got))
		})
	}
}

func TestEveryFixtureParsesWithoutRowErrors(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "greenbook", "raw", "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	sort.Strings(files)

	for _, file := range files {
		file := file
		t.Run(filepath.Base(file), func(t *testing.T) {
			t.Parallel()

			page := parseFixture(t, filepath.Base(file))
			assert.Empty(t, page.RowErrors, "fixture %s should have no row errors", file)

			if page.RecordsFiltered == 0 {
				assert.Empty(t, page.Products)
				return
			}
			assert.NotEmpty(t, page.Products)
			assert.Equal(t, 8966, page.RecordsTotal, "recordsTotal should survive parsing")
		})
	}
}

func TestParseProductHandlesOptionalAndUnknownFields(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"product_id": "12345",
		"product_name": "  Example &amp; Co Tablets&#039;  ",
		"NAFDAC": "",
		"status": "Active",
		"unknown_future_field": {"nested": true}
	}`)
	fetchedAt := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	product, err := registry.ParseProduct(raw, fetchedAt, registry.NewNormalizer())
	require.NoError(t, err)

	assert.Equal(t, "12345", product.SourceID)
	assert.Equal(t, "Example & Co Tablets'", product.Name)
	assert.Empty(t, product.NRN)
	assert.Equal(t, "Active", product.Status)
	assert.Empty(t, product.Category)
	assert.Nil(t, product.ApprovedAt)
	assert.Nil(t, product.ExpiresAt)
	assert.Empty(t, product.Ingredients)
	assert.JSONEq(t, string(raw), string(product.Raw))
	assert.Equal(t, fetchedAt, product.FetchedAt)
}

func TestParseProductPairsIngredientsAndStrengthsByPosition(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"product_id": 9,
		"product_name": "Test Capsules",
		"NAFDAC": " A11-0551 ",
		"status": "Active",
		"ingredient_name": "Paracetamol; Caffeine; Unpaired Ingredient",
		"strength": "500 mg; 30 mg",
		"composition": "Each capsule contains: Paracetamol 500 mg, Caffeine 30 mg\r\nExcipients q.s.",
		"product_category": {"name": "Drugs"},
		"applicant": {"name": "Test Applicant"},
		"form": {"name": "Capsule"},
		"route": {"name": "Oral"},
		"approval_date": "2024-02-29",
		"expiry_date": "not-a-date"
	}`)

	product, err := registry.ParseProduct(raw, fixtureTime, registry.NewNormalizer())
	require.NoError(t, err)

	assert.Equal(t, "A11-0551", product.NRN)
	assert.Equal(t, "Drugs", product.Category)
	assert.Equal(t, "Test Applicant", product.Applicant)
	assert.Equal(t, "Capsule", product.Form)
	require.NotNil(t, product.ApprovedAt)
	assert.Equal(t, 2024, product.ApprovedAt.Year())
	assert.Nil(t, product.ExpiresAt)
	assert.Equal(t, "Each capsule contains: Paracetamol 500 mg, Caffeine 30 mg\nExcipients q.s.", product.CompositionRaw)

	require.Len(t, product.Ingredients, 3)
	assert.Equal(t, "paracetamol", product.Ingredients[0].NameNorm)
	require.NotNil(t, product.Ingredients[0].Strength.Value)
	assert.Equal(t, 500.0, *product.Ingredients[0].Strength.Value)
	assert.Equal(t, "caffeine", product.Ingredients[1].NameNorm)
	assert.Equal(t, 30.0, *product.Ingredients[1].Strength.Value)
	assert.Equal(t, "unpaired ingredient", product.Ingredients[2].NameNorm)
	assert.False(t, product.Ingredients[2].Strength.Parsed())
}

func TestParseProductRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	_, err := registry.ParseProduct([]byte(`{"product_id":`), fixtureTime, registry.NewNormalizer())
	require.Error(t, err)
}

func TestParsePageCollectsBadRowsAndKeepsGoodOnes(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"recordsTotal": 2,
		"recordsFiltered": 2,
		"data": [
			{"product_id": 1, "product_name": "Good", "NAFDAC": "04-0001", "status": "Active"},
			{"product_id": 2, "product_name": {"not": "a string"}, "NAFDAC": "04-0002", "status": "Active"}
		]
	}`)

	page, err := registry.ParsePage(payload, fixtureTime, registry.NewNormalizer())
	require.NoError(t, err)
	require.Len(t, page.Products, 1)
	require.Len(t, page.RowErrors, 1)
	assert.Equal(t, 1, page.RowErrors[0].Index)
	assert.Equal(t, "Good", page.Products[0].Name)
}

func TestParsePageRejectsMalformedEnvelope(t *testing.T) {
	t.Parallel()

	_, err := registry.ParsePage([]byte(`not json`), fixtureTime, registry.NewNormalizer())
	require.Error(t, err)
}

func TestPayloadHashChangesWithRawPayload(t *testing.T) {
	t.Parallel()

	one, err := registry.ParseProduct([]byte(`{"product_id":1,"product_name":"A","NAFDAC":"04-0001","status":"Active"}`), fixtureTime, nil)
	require.NoError(t, err)
	two, err := registry.ParseProduct([]byte(`{"product_id":1,"product_name":"B","NAFDAC":"04-0001","status":"Active"}`), fixtureTime, nil)
	require.NoError(t, err)

	assert.NotEmpty(t, one.PayloadHash())
	assert.NotEqual(t, one.PayloadHash(), two.PayloadHash())
}

func parseFixture(t *testing.T, name string) registry.Page {
	t.Helper()

	if !strings.ContainsRune(name, filepath.Separator) && !strings.Contains(name, "/") {
		name = filepath.Join("..", "..", "testdata", "greenbook", "raw", name)
	}
	data, err := os.ReadFile(name)
	require.NoError(t, err)

	page, err := registry.ParsePage(data, fixtureTime, registry.NewNormalizer())
	require.NoError(t, err)
	return page
}
