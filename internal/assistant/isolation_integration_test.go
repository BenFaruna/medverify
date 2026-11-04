//go:build integration

package assistant_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/assistant"
	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/internal/rag"
	"github.com/benfaruna/medverify/internal/verify"
	"github.com/benfaruna/medverify/migrations"
)

type isolationFixture struct {
	pool     *pgxpool.Pool
	embedder rag.Embedder
	store    *rag.Store
}

func newIsolationFixture(t *testing.T) *isolationFixture {
	t.Helper()
	ctx := context.Background()
	databaseURL := testkit.Postgres(t)
	require.NoError(t, migrations.Up(ctx, databaseURL))

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return &isolationFixture{
		pool:     pool,
		embedder: rag.NewFakeEmbedder(rag.VectorDimension, "fake-1024"),
		store:    rag.NewStore(pool),
	}
}

func (f *isolationFixture) seedProduct(t *testing.T, nrn, name, text string) int64 {
	t.Helper()
	ctx := context.Background()
	var productID, documentID int64
	require.NoError(t, f.pool.QueryRow(ctx, `
		INSERT INTO products (source_id, nrn, name, status, raw, payload_hash, fetched_at, updated_at)
		VALUES ($1, $2, $3, 'Active', '{}'::jsonb, 'hash', now(), now())
		RETURNING id`, "src-"+nrn, nrn, name).Scan(&productID))
	require.NoError(t, f.pool.QueryRow(ctx, `
		INSERT INTO smpc_documents (product_id, source_url, sha256, fetched_at, extraction_method, status)
		VALUES ($1, $2, $3, now(), 'text', 'ok')
		RETURNING id`, productID, "https://example.test/leaflets/"+nrn+".pdf", "sha-"+nrn).Scan(&documentID))
	_, err := f.pool.Exec(ctx, `
		INSERT INTO smpc_sections (document_id, section_code, heading, text, page_start)
		VALUES ($1, '4.4', 'Warnings', $2, 1)`, documentID, text)
	require.NoError(t, err)
	return productID
}

func TestSearchLeafletNeverReturnsAnotherProductsChunks(t *testing.T) {
	fixture := newIsolationFixture(t)
	ctx := context.Background()

	first := fixture.seedProduct(t, "04-0858", "Paracetamol Tablets",
		"Paracetamol warnings: do not exceed 4000 mg in 24 hours.")
	second := fixture.seedProduct(t, "04-0860", "Ibuprofen Tablets",
		"Ibuprofen warnings: do not take with other medicines without advice.")
	_, err := fixture.store.Index(ctx, fixture.embedder, rag.IndexOptions{Limit: 10})
	require.NoError(t, err)

	toolbox := assistant.NewToolbox(searcher{store: fixture.store, embedder: fixture.embedder})
	row := assistant.DefaultPolicy().Decide(assistant.IntentLeaflet)
	session := assistant.Session{
		ScanID:    uuid.New(),
		ProductID: first,
		Language:  "en",
		Match: verify.MatchResult{
			Outcome: verify.MatchedConsistent,
			Product: &verify.ProductRef{SourceID: "src-04-0858", NRN: "04-0858", Name: "Paracetamol Tablets"},
		},
	}

	// The model supplies the other product, NRN, and a scan id. All are data,
	// and the session must win.
	args := json.RawMessage(`{"query":"ibuprofen","product_id":` +
		intString(second) + `,"nrn":"04-0860","scan_id":"` + uuid.New().String() + `"}`)
	result, err := toolbox.Call(ctx, row, session, assistant.ToolSearchLeaflet, args)
	require.NoError(t, err)
	require.False(t, result.IsError)

	for _, citation := range result.Citations {
		assert.NotContains(t, citation.Text, "Ibuprofen")
		assert.NotContains(t, citation.Text, "do not take with other medicines")
	}
	for _, citation := range result.Citations {
		assert.Contains(t, citation.Text, "Paracetamol")
	}

	// A second call against the other session gets only the other leaflet.
	session.ProductID = second
	result, err = toolbox.Call(ctx, row, session, assistant.ToolSearchLeaflet,
		json.RawMessage(`{"query":"paracetamol","product_id":`+intString(first)+`}`))
	require.NoError(t, err)
	for _, citation := range result.Citations {
		assert.NotContains(t, citation.Text, "4000 mg")
	}
}

func intString(value int64) string {
	return strconv.FormatInt(value, 10)
}

type searcher struct {
	store    *rag.Store
	embedder rag.Embedder
}

func (s searcher) Search(ctx context.Context, request rag.SearchRequest) ([]rag.Hit, error) {
	return s.store.Search(ctx, s.embedder, request)
}
