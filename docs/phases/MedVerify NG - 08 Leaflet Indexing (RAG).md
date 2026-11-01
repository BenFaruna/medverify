# 08 Leaflet Indexing (RAG)

**Goal:** turn the leaflet sections from phase 02 into searchable chunks with embeddings and full-text indexes, and provide a retrieval function that returns the best passages for one specific product, with source references.

**Depends on:** 02. **Blocks:** 09. **Read first:** 00 Build Tracker, 02 Greenbook and Leaflet Ingestion.

## 1. Deliverables checklist

- [ ] Embedder interface and one provider implementation, plus a deterministic fake for tests
- [ ] Chunker: section-aware, size-bounded, with overlap and context headers
- [ ] Table smpc\_chunks with vector, tsvector, and metadata columns
- [ ] Backfill job: idempotent, resumable, rate limited
- [ ] Incremental job: re-chunk and re-embed only documents whose sha256 changed
- [ ] Retrieval function: filter by product, hybrid ranking (vector plus full text), section filters
- [ ] Embedding model versioning and a re-embed procedure
- [ ] Retrieval evaluation set and recall scoring command (see document 10)
- [ ] Admin command to inspect chunks for a product

## 2. Tools needed

| Tool | Purpose |
| --- | --- |
| pgvector extension and the pgvector-go library | Vector storage and pgx integration |
| PostgreSQL full-text search (tsvector, GIN) | Lexical matching |
| An embedding provider with multilingual support | Query and passage embeddings |
| A tokenizer or approximate token counter | Chunk sizing |
| rapid, testify, testcontainers-go with the pgvector image | Tests |

## 3. Setup required for success

1. **Pick the embedding provider and dimension first** (open decision in document 00). The vector column dimension is fixed by the model, so changing models later means a migration and a full re-embed. Record the model name, version, and dimension in config and in every row.
2. Confirm leaflets have been ingested and sectioned (phase 02). Measure how many have no section codes, because those produce weaker retrieval.
3. Build a **retrieval eval set**: 100 or more realistic user questions (English first, then Pidgin, Hausa, Yoruba, Igbo), each labeled with the product and the section that contains the answer, or labeled unanswerable.
4. Set a budget for embedding cost and a backfill plan: batch size, concurrency, and rate limits.

## 4. Data model

```sql
CREATE TABLE smpc_chunks (
  id bigserial PRIMARY KEY,
  product_id bigint NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  document_id bigint NOT NULL REFERENCES smpc_documents(id) ON DELETE CASCADE,
  section_id bigint REFERENCES smpc_sections(id) ON DELETE CASCADE,
  section_code text,
  heading text,
  chunk_index int NOT NULL,
  chunk_text text NOT NULL,
  chunk_hash text NOT NULL,
  token_count int NOT NULL,
  embedding vector(1024),          -- dimension set by the chosen model
  embedding_model text NOT NULL,
  tsv tsvector GENERATED ALWAYS AS (to_tsvector('english', chunk_text)) STORED,
  UNIQUE (document_id, chunk_index, embedding_model)
);
CREATE INDEX ON smpc_chunks (product_id, section_code);
CREATE INDEX ON smpc_chunks USING gin (tsv);
```

**Index choice.** Retrieval always filters by one product, so each search scans a few dozen rows. An exact scan filtered by product\_id is fast and fully accurate. Do not add an approximate vector index (HNSW or IVFFlat) until you add cross-product search. Approximate indexes combined with filters can silently return fewer results than expected.

## 5. Design notes

**Chunking.**

- Split by section first, so chunks never cross section boundaries.
- Within long sections, split by paragraph groups to roughly 300 to 500 tokens with a small overlap.
- Prefix the text used for embedding with a context header: product name, NRN, section heading. Store the clean text separately for display and citation.
- Keep tables and lists intact where possible. Never split mid-sentence.
- Store chunk\_hash (hash of the text plus the embedding model) so unchanged chunks are skipped.

**Embedder interface.**

```go
type Embedder interface {
    EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
    EmbedQuery(ctx context.Context, text string) ([]float32, error)
    Model() string
    Dim() int
}
```

Some providers use different modes for documents and queries, which is why there are two methods.

**Hybrid retrieval.** Run two ranked queries for the product, one by vector distance and one by full-text rank, then merge with reciprocal rank fusion. Return the top K (default 5) with: chunk text, section code and heading, document source URL, page, and scores.

**Multilingual questions.** Embed the question in its own language with a multilingual model. Full-text search runs on English, so for non-English questions rely mainly on the vector side, or translate the query to English first (decided and measured in the eval).

**Versioning.** Every row stores embedding\_model. A re-embed writes new rows for the new model, then switches the active model in config, then deletes old rows. Never mix models in one search.

## 6. Tests for this phase

| Test | Level | What it proves |
| --- | --- | --- |
| Chunker: respects section boundaries, size limit, and overlap | unit | Chunking rules |
| Chunker: never splits mid-sentence on the fixture leaflets | unit | Quality |
| Chunker: same input gives identical chunks and hashes | property | Determinism |
| Context header is in the embedding text but not in the display text | unit | Separation |
| Fake embedder (hash based, fixed dimension) gives stable vectors for tests | unit | Test support |
| Backfill twice leaves the row count and hashes unchanged | integration | Idempotency |
| Changed leaflet (new sha256) re-chunks only that document | integration | Incremental |
| Backfill resumes after a simulated crash without duplicating chunks | integration | Resumability |
| Retrieval never returns chunks from another product | integration | Scope isolation (critical) |
| Retrieval returns section code, heading, and source URL for every hit | integration | Citation data |
| Hybrid ranking: a lexical-only query (a drug name or number) and a semantic-only query both find the right section | integration | Hybrid value |
| Mixed embedding models in the table: search uses only the active model | integration | Versioning |
| Dimension mismatch between model and column fails fast at startup | unit | Config safety |
| Retrieval eval: recall at 5 per section and rate of correct no-answer on unanswerable questions | eval | Quality |
| Retrieval latency under 150 ms excluding the query embedding call | benchmark | Performance |

**Acceptance gate (propose, then agree).** Recall at 5 of at least 85 percent on answerable questions in English, with no cross-product leaks, and the weakest language measured and documented.

## 7. Pitfalls and guards

- **Cross-product leakage.** A retrieval bug that mixes products would give an answer about the wrong medicine. The filter must be enforced inside the retrieval function, and tests must prove it.
- **Embedding model change.** Swapping models without re-embedding makes searches meaningless. Store the model per row and check it.
- **Dimension mismatch.** The database rejects vectors of the wrong size at write time. Catch this at startup rather than mid-backfill.
- **Garbage chunks from scanned leaflets.** Documents with status scanned\_needs\_ocr must be excluded until OCR text exists.
- **Numbers and units lost in embeddings.** Rely on full-text for exact strings and keep original text for citation.
- **Tables flattened badly by PDF extraction.** Inspect a sample of chunks by hand. Add table-aware handling only if the eval shows it matters.
- **Cost.** Embedding a large corpus is a one-off cost, but re-embedding on every sync is avoidable. Use chunk hashes.
- **Prompt injection inside leaflets.** Leaflet text is data. Store as is, and treat it as untrusted in phase 09.
- **Premature ANN indexes.** Skip them until you need cross-product search.

## 8. Definition of Done

- The corpus is chunked and embedded, with a coverage report (products with leaflets, products without, products needing OCR).
- Retrieval eval meets the agreed gate.
- Checklist ticked, tracker updated.

## 9. Notes for agents

- The product ID must be a required parameter of the retrieval function, not optional.
- Use the fake embedder in all unit and integration tests. Real embedding calls only in the nightly eval and the backfill command.
- Do not change the vector dimension without writing a migration and a re-embed plan in this document.
