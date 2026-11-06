//go:build integration

package rag_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benfaruna/medverify/internal/platform/testkit"
	"github.com/benfaruna/medverify/internal/rag"
	"github.com/benfaruna/medverify/migrations"
)

type ragFixture struct {
	pool  *pgxpool.Pool
	store *rag.Store
}

func newRAGFixture(t *testing.T) *ragFixture {
	t.Helper()
	ctx := context.Background()
	databaseURL := testkit.Postgres(t)
	require.NoError(t, migrations.Up(ctx, databaseURL))

	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return &ragFixture{pool: pool, store: rag.NewStore(pool)}
}

// seedLeaflet inserts a product with one readable document and its sections.
func (f *ragFixture) seedLeaflet(t *testing.T, nrn, name, sha string, sections []seedSection) (productID, documentID int64) {
	t.Helper()
	ctx := context.Background()

	require.NoError(t, f.pool.QueryRow(ctx, `
		INSERT INTO products (source_id, nrn, name, status, raw, payload_hash, fetched_at, updated_at)
		VALUES ($1, $2, $3, 'Active', '{}'::jsonb, 'hash', now(), now())
		RETURNING id`, "src-"+nrn, nrn, name).Scan(&productID))

	require.NoError(t, f.pool.QueryRow(ctx, `
		INSERT INTO smpc_documents (product_id, source_url, sha256, fetched_at, extraction_method, status)
		VALUES ($1, $2, $3, now(), 'text', 'ok')
		RETURNING id`, productID, "https://example.test/leaflets/"+nrn+".pdf", sha).Scan(&documentID))

	for _, section := range sections {
		_, err := f.pool.Exec(ctx, `
			INSERT INTO smpc_sections (document_id, section_code, heading, text, page_start)
			VALUES ($1, $2, $3, $4, $5)`,
			documentID, section.code, section.heading, section.text, section.page)
		require.NoError(t, err)
	}
	return productID, documentID
}

type seedSection struct {
	code    string
	heading string
	text    string
	page    int
}

func paracetamolSections() []seedSection {
	return []seedSection{
		{
			code:    "4.2",
			heading: "Posology and method of administration",
			page:    1,
			text: "Adults and children over 12 years: 500 mg to 1000 mg every 4 to 6 hours as required. " +
				"Do not exceed 4000 mg in 24 hours.",
		},
		{
			code:    "4.4",
			heading: "Special warnings and precautions for use",
			page:    2,
			text: "Care is advised in patients with liver or kidney impairment. " +
				"Patients should not take other paracetamol-containing products at the same time.",
		},
	}
}

func TestIndexEmbedsEveryReadableLeafletOnce(t *testing.T) {
	fixture := newRAGFixture(t)
	ctx := context.Background()
	embedder := rag.NewFakeEmbedder(rag.VectorDimension, "fake-1024")

	productID, documentID := fixture.seedLeaflet(t, "04-0858", "Paracetamol Tablets BP 500 mg", "sha-1", paracetamolSections())

	summary, err := fixture.store.Index(ctx, embedder, rag.IndexOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Documents)
	assert.Equal(t, 2, summary.Chunks, "two short sections should make two chunks")

	count, err := fixture.store.CountChunks(ctx, embedder.Model())
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)

	chunks, err := fixture.store.ChunksForProduct(ctx, productID, embedder.Model(), 10)
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	assert.Equal(t, "4.2", *chunks[0].SectionCode)
	assert.Equal(t, "4.4", *chunks[1].SectionCode)
	assert.NotContains(t, chunks[0].ChunkText, "Paracetamol Tablets BP 500 mg",
		"the embedding header must not leak into the quoted text")

	// A second pass is a no-op: nothing is re-embedded.
	again, err := fixture.store.Index(ctx, embedder, rag.IndexOptions{Limit: 10})
	require.NoError(t, err)
	assert.Zero(t, again.Documents)
	assert.Zero(t, again.Chunks)

	// Rewriting the same document with a new hash re-chunks only that document.
	_, err = fixture.pool.Exec(ctx, `UPDATE smpc_documents SET sha256 = 'sha-2' WHERE id = $1`, documentID)
	require.NoError(t, err)
	changed, err := fixture.store.Index(ctx, embedder, rag.IndexOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, changed.Documents)
	count, err = fixture.store.CountChunks(ctx, embedder.Model())
	require.NoError(t, err)
	assert.EqualValues(t, 2, count, "old chunks are replaced, not duplicated")
}

func TestIndexSkipsDocumentsThatAreNotReadable(t *testing.T) {
	fixture := newRAGFixture(t)
	ctx := context.Background()
	embedder := rag.NewFakeEmbedder(rag.VectorDimension, "fake-1024")

	productID, _ := fixture.seedLeaflet(t, "04-0859", "Ibuprofen Tablets", "sha-3", paracetamolSections())
	_, err := fixture.pool.Exec(ctx, `UPDATE smpc_documents SET status = 'scanned_needs_ocr' WHERE product_id = $1`, productID)
	require.NoError(t, err)

	summary, err := fixture.store.Index(ctx, embedder, rag.IndexOptions{Limit: 10})
	require.NoError(t, err)
	assert.Zero(t, summary.Documents)

	count, err := fixture.store.CountChunks(ctx, embedder.Model())
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestSearchIsScopedToOneProduct(t *testing.T) {
	fixture := newRAGFixture(t)
	ctx := context.Background()
	embedder := rag.NewFakeEmbedder(rag.VectorDimension, "fake-1024")

	paracetamolID, _ := fixture.seedLeaflet(t, "04-0858", "Paracetamol Tablets BP 500 mg", "sha-a", paracetamolSections())
	ibuprofenID, _ := fixture.seedLeaflet(t, "04-0860", "Ibuprofen Tablets BP 400 mg", "sha-b", []seedSection{
		{code: "4.4", heading: "Warnings", page: 1, text: "Do not take ibuprofen with other medicines without advice."},
	})

	_, err := fixture.store.Index(ctx, embedder, rag.IndexOptions{Limit: 10})
	require.NoError(t, err)

	hits, err := fixture.store.Search(ctx, embedder, rag.SearchRequest{
		ProductID: paracetamolID, Query: "paracetamol warnings liver", Limit: 5,
	})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	_ = ibuprofenID
	for _, hit := range hits {
		assert.NotContains(t, hit.Text, "ibuprofen", "cross-product text must never be returned")
		assert.NotEmpty(t, hit.SourceURL)
		assert.NotEmpty(t, hit.SectionCode)
	}
	assert.Equal(t, "4.4", hits[0].SectionCode, "the warnings section answers this question")
}

func TestSearchSupportsSectionFilterAndLexicalQueries(t *testing.T) {
	fixture := newRAGFixture(t)
	ctx := context.Background()
	embedder := rag.NewFakeEmbedder(rag.VectorDimension, "fake-1024")

	productID, _ := fixture.seedLeaflet(t, "04-0858", "Paracetamol Tablets BP 500 mg", "sha-a", paracetamolSections())
	_, err := fixture.store.Index(ctx, embedder, rag.IndexOptions{Limit: 10})
	require.NoError(t, err)

	filtered, err := fixture.store.Search(ctx, embedder, rag.SearchRequest{
		ProductID: productID, Query: "dose", SectionCode: "4.2", Limit: 5,
	})
	require.NoError(t, err)
	require.NotEmpty(t, filtered)
	for _, hit := range filtered {
		assert.Equal(t, "4.2", hit.SectionCode)
	}

	// An exact number is found by full-text rank even when the query has no
	// semantic overlap with the passage.
	lexical, err := fixture.store.Search(ctx, embedder, rag.SearchRequest{
		ProductID: productID, Query: "4000 mg", Limit: 5,
	})
	require.NoError(t, err)
	require.NotEmpty(t, lexical)
	assert.Equal(t, "4.2", lexical[0].SectionCode)
	assert.Positive(t, lexical[0].TextScore)
}

func TestSearchUsesOnlyTheActiveEmbeddingModel(t *testing.T) {
	fixture := newRAGFixture(t)
	ctx := context.Background()
	first := rag.NewFakeEmbedder(rag.VectorDimension, "fake-1024")
	second := rag.NewFakeEmbedder(rag.VectorDimension, "fake-1024-v2")

	productID, _ := fixture.seedLeaflet(t, "04-0858", "Paracetamol Tablets BP 500 mg", "sha-a", paracetamolSections())
	_, err := fixture.store.Index(ctx, first, rag.IndexOptions{Limit: 10})
	require.NoError(t, err)

	// The old model still sees its own rows...
	oldHits, err := fixture.store.Search(ctx, first, rag.SearchRequest{ProductID: productID, Query: "liver"})
	require.NoError(t, err)
	assert.NotEmpty(t, oldHits)

	// ...the new one sees nothing until it is indexed.
	newHits, err := fixture.store.Search(ctx, second, rag.SearchRequest{ProductID: productID, Query: "liver"})
	require.NoError(t, err)
	assert.Empty(t, newHits)

	_, err = fixture.store.Index(ctx, second, rag.IndexOptions{Limit: 10})
	require.NoError(t, err)
	newHits, err = fixture.store.Search(ctx, second, rag.SearchRequest{ProductID: productID, Query: "liver"})
	require.NoError(t, err)
	assert.NotEmpty(t, newHits)

	var models []string
	rows, err := fixture.pool.Query(ctx, `SELECT DISTINCT embedding_model FROM smpc_chunks ORDER BY embedding_model`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var model string
		require.NoError(t, rows.Scan(&model))
		models = append(models, model)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"fake-1024", "fake-1024-v2"}, models)
}

func TestCoverageAndProductResolution(t *testing.T) {
	fixture := newRAGFixture(t)
	ctx := context.Background()
	embedder := rag.NewFakeEmbedder(rag.VectorDimension, "fake-1024")

	productID, _ := fixture.seedLeaflet(t, "04-0858", "Paracetamol Tablets BP 500 mg", "sha-a", paracetamolSections())
	_, err := fixture.store.Index(ctx, embedder, rag.IndexOptions{Limit: 10})
	require.NoError(t, err)

	coverage, err := fixture.store.Coverage(ctx, embedder.Model())
	require.NoError(t, err)
	assert.EqualValues(t, 1, coverage.ProductsWithReadableLeaflet)
	assert.EqualValues(t, 1, coverage.ProductsIndexed)

	resolved, err := fixture.store.ResolveProduct(ctx, "04-0858")
	require.NoError(t, err)
	assert.Equal(t, productID, resolved)

	_, err = fixture.store.ResolveProduct(ctx, "not-an-nrn")
	require.Error(t, err)
}

func TestIndexRejectsAMismatchedDimension(t *testing.T) {
	fixture := newRAGFixture(t)
	ctx := context.Background()
	embedder := rag.NewFakeEmbedder(16, "small-model")

	_, err := fixture.store.Index(ctx, embedder, rag.IndexOptions{Limit: 10})
	require.ErrorIs(t, err, rag.ErrEmbeddingMismatch)
}

// The assistant's leaflet answer depends on reading three states apart, so the
// query that reads them must survive a real schema: no document row at all, a
// document that is stored but unindexed, and a document with chunks.
func TestLeafletStatusReadsTheDocumentStateOfOneProduct(t *testing.T) {
	fixture := newRAGFixture(t)
	ctx := context.Background()

	// A product with a published leaflet URL that has never been fetched.
	var pendingID int64
	require.NoError(t, fixture.pool.QueryRow(ctx, `
		INSERT INTO products (source_id, nrn, name, status, raw, payload_hash, fetched_at, updated_at, smpc_url)
		VALUES ('src-04-0900', '04-0900', 'Pending Tablets', 'Active', '{}'::jsonb, 'hash', now(), now(),
			'https://example.test/leaflets/04-0900.pdf')
		RETURNING id`).Scan(&pendingID))

	status, found, err := fixture.store.LeafletStatus(ctx, pendingID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, pendingID, status.ProductID)
	assert.Zero(t, status.DocumentID, "an unfetched leaflet has no document row")
	assert.Equal(t, "https://example.test/leaflets/04-0900.pdf", status.Link())
	assert.False(t, status.Readable())
	assert.True(t, status.Known())

	// The same product after a download: the document row wins as the link and
	// an unindexed document is still unreadable.
	productID, documentID := fixture.seedLeaflet(t, "04-0901", "Stored Tablets", "sha-0901", nil)
	_, err = fixture.store.Index(ctx, rag.NewFakeEmbedder(rag.VectorDimension, "fake-1024"), rag.IndexOptions{Limit: 10})
	require.NoError(t, err)
	status, found, err = fixture.store.LeafletStatus(ctx, productID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, documentID, status.DocumentID)
	assert.Equal(t, "ok", status.DocumentStatus)
	assert.Equal(t, "https://example.test/leaflets/04-0901.pdf", status.DocumentURL)
	// Sections were empty, so nothing was chunked: the leaflet is on file but
	// cannot be quoted, which is a different answer from "no leaflet".
	assert.False(t, status.Readable())

	_, found, err = fixture.store.LeafletStatus(ctx, 999999)
	require.NoError(t, err)
	assert.False(t, found, "an unknown product must not be reported as a leaflet gap")
}
