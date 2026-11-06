package rag

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
)

// VectorDimension is the pgvector column width. It must match the migration and
// the configured embedding model; a mismatch fails at startup, not mid-write.
const VectorDimension = 1024

// Search defaults.
const (
	DefaultSearchLimit = 5
	// rrfK damps the contribution of low-ranked hits. 60 is the value from the
	// original reciprocal-rank-fusion paper and is used for both lists.
	rrfK = 60
)

// Store persists chunks and searches them. All retrieval is filtered by product
// inside SQL, so no caller can widen a search to another medicine.
type Store struct {
	pool    *pgxpool.Pool
	queries *dbgen.Queries
}

// NewStore wraps a connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, queries: dbgen.New(pool)}
}

// LeafletStatus is what the assistant may say about one product's leaflet. It
// separates three states a question has to tell apart: no leaflet URL at all,
// a URL that has not been downloaded yet, and a downloaded leaflet that has not
// been chunked for retrieval.
type LeafletStatus struct {
	ProductID      int64
	URL            string
	DocumentID     int64
	DocumentURL    string
	DocumentStatus string
	Chunks         int64
}

// Readable reports whether passages can be searched right now. A document that
// is downloaded but not yet chunked is not readable, so the caller can ask for
// indexing instead of claiming the leaflet does not exist.
func (s LeafletStatus) Readable() bool {
	return s.DocumentStatus == "ok" && s.Chunks > 0
}

// Known reports whether any leaflet location is on file, fetched or not.
func (s LeafletStatus) Known() bool { return s.Link() != "" }

// Refetchable reports whether downloading the leaflet again could change what is
// on file: there is no document row yet, or the last attempt failed. A leaflet
// that is stored but has no text layer (a scan), or is too large, would come
// back byte for byte the same, so a question must not start another download of
// it.
func (s LeafletStatus) Refetchable() bool {
	return s.DocumentStatus == "" || s.DocumentStatus == "failed"
}

// Link returns the best URL to show a person, preferring the document row over
// the product record's column because the document row is what was fetched.
func (s LeafletStatus) Link() string {
	if s.DocumentURL != "" {
		return s.DocumentURL
	}
	return s.URL
}

// IndexOptions bounds one indexing pass.
type IndexOptions struct {
	// Limit caps documents handled in this pass. The default is 50.
	Limit int
	// Delay is applied between documents to stay under a provider rate limit.
	Delay  time.Duration
	Logger *slog.Logger
}

// IndexSummary reports what one pass did.
type IndexSummary struct {
	Documents int
	Chunks    int
	Skipped   int // readable documents with no usable sections
}

// Index chunks and embeds every document that has not been indexed for this
// model and file hash. Each document is replaced in one transaction, so a crash
// leaves either the old chunks or the new ones, never a half-written document.
// Completed documents drop out of the selection query, which makes the pass
// resumable and idempotent.
func (s *Store) Index(ctx context.Context, embedder Embedder, opts IndexOptions) (IndexSummary, error) {
	if err := validateEmbedder(embedder); err != nil {
		return IndexSummary{}, err
	}
	if opts.Limit <= 0 {
		opts.Limit = 50
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	documents, err := s.queries.ListDocumentsForRAGIndex(ctx, dbgen.ListDocumentsForRAGIndexParams{
		EmbeddingModel: embedder.Model(),
		RowLimit:       int32(opts.Limit),
	})
	if err != nil {
		return IndexSummary{}, fmt.Errorf("rag: list documents to index: %w", err)
	}

	var summary IndexSummary
	for i, document := range documents {
		if i > 0 && opts.Delay > 0 {
			select {
			case <-ctx.Done():
				return summary, ctx.Err()
			case <-time.After(opts.Delay):
			}
		}
		chunks, err := s.indexDocument(ctx, embedder, document)
		if err != nil {
			return summary, err
		}
		if chunks == 0 {
			summary.Skipped++
			logger.WarnContext(ctx, "leaflet has no indexable sections",
				slog.Int64("document_id", document.DocumentID),
				slog.Int64("product_id", document.ProductID),
			)
			continue
		}
		summary.Documents++
		summary.Chunks += chunks
	}
	return summary, nil
}

func (s *Store) indexDocument(ctx context.Context, embedder Embedder, document dbgen.ListDocumentsForRAGIndexRow) (int, error) {
	sections, err := s.queries.ListRAGDocumentSections(ctx, document.DocumentID)
	if err != nil {
		return 0, fmt.Errorf("rag: list sections for document %d: %w", document.DocumentID, err)
	}
	if len(sections) == 0 {
		return 0, nil
	}

	doc := Document{
		ProductID:   document.ProductID,
		DocumentID:  document.DocumentID,
		ProductName: document.ProductName,
		NRN:         document.Nrn,
		SourceURL:   document.SourceUrl,
	}
	inputs := make([]Section, 0, len(sections))
	for _, section := range sections {
		inputs = append(inputs, Section{
			ID:        section.ID,
			Code:      valueOrEmpty(section.SectionCode),
			Heading:   valueOrEmpty(section.Heading),
			Text:      section.Text,
			PageStart: int(valueOrZero(section.PageStart)),
		})
	}

	chunks := ChunkDocument(doc, inputs, Options{Model: embedder.Model()})
	if len(chunks) == 0 {
		return 0, nil
	}

	texts := make([]string, len(chunks))
	for i, chunk := range chunks {
		texts[i] = chunk.EmbeddingText
	}
	vectors, err := embedder.EmbedDocuments(ctx, texts)
	if err != nil {
		return 0, fmt.Errorf("rag: embed document %d: %w", document.DocumentID, err)
	}
	if len(vectors) != len(chunks) {
		return 0, fmt.Errorf("%w: document %d asked for %d vectors, got %d",
			ErrEmbeddingMismatch, document.DocumentID, len(chunks), len(vectors))
	}
	for i, vector := range vectors {
		if len(vector) != VectorDimension {
			return 0, fmt.Errorf("%w: vector %d has %d dimensions, want %d",
				ErrEmbeddingMismatch, i, len(vector), VectorDimension)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("rag: begin document transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)

	if err := queries.DeleteRAGChunksForDocumentModel(ctx, dbgen.DeleteRAGChunksForDocumentModelParams{
		DocumentID:     document.DocumentID,
		EmbeddingModel: embedder.Model(),
	}); err != nil {
		return 0, fmt.Errorf("rag: clear old chunks for document %d: %w", document.DocumentID, err)
	}

	sha := valueOrEmpty(document.Sha256)
	for i, chunk := range chunks {
		params := dbgen.InsertRAGChunkParams{
			ProductID:      document.ProductID,
			DocumentID:     document.DocumentID,
			SectionCode:    optional(chunk.SectionCode),
			Heading:        optional(chunk.Heading),
			ChunkIndex:     int32(chunk.Index),
			ChunkText:      chunk.Text,
			EmbeddingText:  chunk.EmbeddingText,
			ChunkHash:      chunk.Hash,
			TokenCount:     int32(chunk.Tokens),
			Embedding:      pgvector.NewVector(vectors[i]),
			EmbeddingModel: embedder.Model(),
			DocumentSha256: sha,
		}
		if chunk.SectionID != 0 {
			params.SectionID = pgtype.Int8{Int64: chunk.SectionID, Valid: true}
		}
		if chunk.PageStart > 0 {
			page := int32(chunk.PageStart)
			params.PageStart = &page
		}
		if err := queries.InsertRAGChunk(ctx, params); err != nil {
			return 0, fmt.Errorf("rag: insert chunk %d of document %d: %w", chunk.Index, document.DocumentID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("rag: commit document %d: %w", document.DocumentID, err)
	}
	return len(chunks), nil
}

// SearchRequest describes one retrieval. ProductID is required and is the only
// scope the query can see.
type SearchRequest struct {
	ProductID   int64
	Query       string
	SectionCode string
	Limit       int
}

// Hit is one retrieved passage with everything a citation needs.
type Hit struct {
	ChunkID     int64   `json:"chunk_id"`
	DocumentID  int64   `json:"document_id"`
	SectionID   int64   `json:"section_id,omitempty"`
	SectionCode string  `json:"section_code,omitempty"`
	Heading     string  `json:"heading,omitempty"`
	PageStart   int     `json:"page_start,omitempty"`
	ChunkIndex  int     `json:"chunk_index"`
	Text        string  `json:"text"`
	SourceURL   string  `json:"source_url"`
	VectorScore float64 `json:"vector_score"`
	TextScore   float64 `json:"text_score"`
	Score       float64 `json:"score"`
}

// Search embeds the question and runs hybrid retrieval for one product.
func (s *Store) Search(ctx context.Context, embedder Embedder, req SearchRequest) ([]Hit, error) {
	if err := validateEmbedder(embedder); err != nil {
		return nil, err
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, fmt.Errorf("rag: search query is required")
	}
	vector, err := embedder.EmbedQuery(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("rag: embed query: %w", err)
	}
	return s.search(ctx, embedder.Model(), req, query, vector)
}

// SearchVector runs hybrid retrieval with an already-computed query vector. Eval
// harnesses and unit tests use it to avoid a provider call.
func (s *Store) SearchVector(ctx context.Context, model string, req SearchRequest, queryVector []float32) ([]Hit, error) {
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("rag: embedding model is required")
	}
	if len(queryVector) != VectorDimension {
		return nil, fmt.Errorf("%w: query vector has %d dimensions, want %d",
			ErrEmbeddingMismatch, len(queryVector), VectorDimension)
	}
	return s.search(ctx, model, req, strings.TrimSpace(req.Query), queryVector)
}

func (s *Store) search(ctx context.Context, model string, req SearchRequest, query string, vector []float32) ([]Hit, error) {
	if req.ProductID <= 0 {
		return nil, fmt.Errorf("rag: product id is required")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	perList := max(limit*3, 10)
	var section *string
	if code := strings.TrimSpace(req.SectionCode); code != "" {
		section = &code
	}

	hits := make(map[int64]*Hit, perList)
	add := func(rowID int64) *Hit {
		hit, ok := hits[rowID]
		if !ok {
			hit = &Hit{ChunkID: rowID}
			hits[rowID] = hit
		}
		return hit
	}

	vectorRows, err := s.queries.SearchRAGChunksByVector(ctx, dbgen.SearchRAGChunksByVectorParams{
		Embedding:      pgvector.NewVector(vector),
		ProductID:      req.ProductID,
		EmbeddingModel: model,
		SectionCode:    section,
		RowLimit:       int32(perList),
	})
	if err != nil {
		return nil, fmt.Errorf("rag: vector search: %w", err)
	}
	for rank, row := range vectorRows {
		hit := add(row.ID)
		hit.DocumentID = row.DocumentID
		hit.SectionID = row.SectionID.Int64
		hit.SectionCode = valueOrEmpty(row.SectionCode)
		hit.Heading = valueOrEmpty(row.Heading)
		hit.PageStart = int(valueOrZero(row.PageStart))
		hit.ChunkIndex = int(row.ChunkIndex)
		hit.Text = row.ChunkText
		hit.SourceURL = row.SourceUrl
		hit.VectorScore = row.Score
		hit.Score += 1 / float64(rrfK+rank+1)
	}

	if query != "" {
		textRows, err := s.queries.SearchRAGChunksByText(ctx, dbgen.SearchRAGChunksByTextParams{
			Query:          query,
			ProductID:      req.ProductID,
			EmbeddingModel: model,
			SectionCode:    section,
			RowLimit:       int32(perList),
		})
		if err != nil {
			return nil, fmt.Errorf("rag: full-text search: %w", err)
		}
		for rank, row := range textRows {
			hit := add(row.ID)
			hit.DocumentID = row.DocumentID
			hit.SectionID = row.SectionID.Int64
			hit.SectionCode = valueOrEmpty(row.SectionCode)
			hit.Heading = valueOrEmpty(row.Heading)
			hit.PageStart = int(valueOrZero(row.PageStart))
			hit.ChunkIndex = int(row.ChunkIndex)
			hit.Text = row.ChunkText
			hit.SourceURL = row.SourceUrl
			hit.TextScore = row.Score
			hit.Score += 1 / float64(rrfK+rank+1)
		}
	}

	out := make([]Hit, 0, len(hits))
	for _, hit := range hits {
		out = append(out, *hit)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ChunkID < out[j].ChunkID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Coverage reports how much of the corpus is searchable.
func (s *Store) Coverage(ctx context.Context, model string) (dbgen.RAGCoverageRow, error) {
	return s.queries.RAGCoverage(ctx, model)
}

// CountChunks reports stored chunks for one model.
func (s *Store) CountChunks(ctx context.Context, model string) (int64, error) {
	return s.queries.CountRAGChunksByModel(ctx, model)
}

// ChunksForProduct returns stored chunks in document order, for inspection.
func (s *Store) ChunksForProduct(ctx context.Context, productID int64, model string, limit int) ([]dbgen.ListRAGChunksForProductRow, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.queries.ListRAGChunksForProduct(ctx, dbgen.ListRAGChunksForProductParams{
		ProductID:      productID,
		EmbeddingModel: model,
		RowLimit:       int32(limit),
	})
}

// LeafletStatus reports the leaflet state for one product. found=false means
// the product is not in the local registry copy at all; an error means the
// lookup could not run. The caller must not turn either into "no leaflet".
func (s *Store) LeafletStatus(ctx context.Context, productID int64) (LeafletStatus, bool, error) {
	row, err := s.queries.GetProductLeafletStatus(ctx, productID)
	if errors.Is(err, pgx.ErrNoRows) {
		return LeafletStatus{}, false, nil
	}
	if err != nil {
		return LeafletStatus{}, false, fmt.Errorf("rag: leaflet status for product %d: %w", productID, err)
	}
	return LeafletStatus{
		ProductID:      row.ProductID,
		URL:            row.SmpcUrl,
		DocumentID:     row.DocumentID,
		DocumentURL:    row.DocumentUrl,
		DocumentStatus: row.DocumentStatus,
		Chunks:         row.ChunkCount,
	}, true, nil
}

// FindProductID returns the local product id for a registry source id or NRN.
// found=false means the record is not present; an error means the lookup could
// not run. The assistant uses this to pin retrieval without treating a database
// outage as "no leaflet".
func (s *Store) FindProductID(ctx context.Context, sourceID, nrn string) (int64, bool, error) {
	sourceID = strings.TrimSpace(sourceID)
	nrn = strings.TrimSpace(nrn)
	if sourceID == "" && nrn == "" {
		return 0, false, nil
	}
	row := s.pool.QueryRow(ctx, `
		SELECT id FROM products
		WHERE ($1 <> '' AND source_id = $1) OR ($2 <> '' AND nrn = $2)
		ORDER BY id
		LIMIT 1`, sourceID, nrn)
	var id int64
	err := row.Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// ResolveProduct turns an NRN, a source id, or a numeric product id into a
// product id. Names are deliberately not matched: a typo must not silently
// inspect a different medicine.
func (s *Store) ResolveProduct(ctx context.Context, reference string) (int64, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return 0, errors.New("rag: product reference is required")
	}
	numeric := numericOrNil(reference)
	row := s.pool.QueryRow(ctx, `
		SELECT id FROM products
		WHERE nrn = $1 OR source_id = $1 OR ($2::bigint IS NOT NULL AND id = $2)
		LIMIT 1`, reference, numeric)
	var id int64
	err := row.Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("rag: no product matches %q", reference)
	}
	return id, err
}

func validateEmbedder(embedder Embedder) error {
	if embedder == nil {
		return fmt.Errorf("rag: an embedder is required")
	}
	if embedder.Dim() != VectorDimension {
		return fmt.Errorf("%w: embedder %q has %d dimensions, database column has %d",
			ErrEmbeddingMismatch, embedder.Model(), embedder.Dim(), VectorDimension)
	}
	if strings.TrimSpace(embedder.Model()) == "" {
		return fmt.Errorf("rag: embedder model name is required")
	}
	return nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func valueOrZero(value *int32) int32 {
	if value == nil {
		return 0
	}
	return *value
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func numericOrNil(value string) any {
	if value == "" {
		return nil
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return nil
		}
	}
	return value
}
