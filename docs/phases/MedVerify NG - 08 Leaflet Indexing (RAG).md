# 08 Leaflet Indexing (RAG)

**Goal:** turn the leaflet sections from phase 02 into searchable chunks with embeddings and full-text indexes, and provide a retrieval function that returns the best passages for one specific product, with source references.

**Depends on:** 02. **Blocks:** 09. **Read first:** 00 Build Tracker, 02 Greenbook and Leaflet Ingestion.

## 1. Deliverables checklist

- [x] Embedder interface and one provider implementation, plus a deterministic fake for tests
- [x] Chunker: section-aware, size-bounded, with overlap and context headers
- [x] Table smpc\_chunks with vector, tsvector, and metadata columns
- [x] Backfill job: idempotent, resumable, rate limited
- [x] Incremental job: re-chunk and re-embed only documents whose sha256 changed
- [x] Retrieval function: filter by product, hybrid ranking (vector plus full text), section filters
- [x] Embedding model versioning and a re-embed procedure
- [ ] Retrieval evaluation set and recall scoring command (see document 10) — scoring command and format exist; the 100+ labelled questions do not
- [x] Admin command to inspect chunks for a product

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

## 10. Implementation notes

- **Schema.** Migration `00008` adds `smpc_chunks`. Every row carries
  `product_id`, `document_id`, `section_id`, the clean `chunk_text`, the
  `embedding_text` that was embedded, `chunk_hash`, `token_count`, the vector,
  `embedding_model`, and `document_sha256`. The generated `tsv` column feeds
  full-text rank. The vector column is `vector(1024)`; retrieval never mixes
  models because every query filters on `embedding_model`. Indexes cover
  `(product_id, section_code)`, `tsv`, `(embedding_model, product_id)`, and
  `document_id`. No approximate vector index is created: with a few dozen rows
  per product an exact scan is faster and exact, and ANN plus a filter silently
  returns fewer rows than asked for.
- **Chunking.** `rag.ChunkDocument` splits inside one section only, prefers
  paragraph boundaries, then sentence boundaries. A single sentence longer than
  a chunk (a flattened table) is split on word boundaries as a last resort. The
  defaults are 400 target tokens, 500 maximum, and 50 tokens of sentence-aligned
  overlap; the overlap is dropped rather than exceed the maximum. Token counts
  are estimated from word count (4/3 per word) and are only used for sizing, not
  billing. The context header (`product name (NRN)`) goes only into
  `embedding_text`; `chunk_text` is what a citation quotes.
- **Hashes and re-embedding.** The chunk hash covers the embedding text and the
  model name, so a model change invalidates every hash. The document hash is
  `smpc_documents.sha256`. The selection query returns only readable documents
  that have no chunks for the current model and document hash, so the same pass
  is the backfill and the incremental job, and a crashed run resumes from the
  first unfinished document.
- **Embedder.** `rag.Embedder` has separate document and query methods. The
  deterministic `FakeEmbedder` hashes words and word pairs into the vector space
  and is the default locally; it is explicitly not a production index. The
  generic `HTTPEmbedder` posts to any OpenAI-compatible `/embeddings` endpoint,
  batches requests, validates the returned count and dimension, and never logs
  the key. The provider and model remain an open decision (tracker row 08);
  `EMBEDDINGS_*` config fails at startup when the dimension differs from 1024 or
  an HTTP embedder is missing its URL or model.
- **Indexing.** `rag.Store.Index` processes a bounded number of documents
  sequentially, applies an optional inter-document delay, and replaces one
  document's chunks in a transaction after all its vectors are computed. A
  provider failure therefore leaves the old chunks intact and makes the job
  retryable; no document is ever half-written. Documents with no usable sections
  are counted as skipped and logged by ID.
- **Retrieval.** `rag.Store.Search` embeds the question and runs two SQL queries
  for one product: vector distance and full-text rank, each with an optional
  exact `section_code` filter. Results are merged with reciprocal rank fusion
  (k = 60). `SearchVector` exists for eval so a harness can supply a vector and
  use the real query string for full-text rank. Product scope and model scope
  are `WHERE` clauses, not caller checks, so no argument can widen a search.
- **CLI and job.** `admin rag index` runs a pass; `admin rag chunks <nrn>` shows
  stored chunks and their hashes; `admin rag coverage` prints corpus counts;
  `admin rag eval --file <yaml>` scores recall and correct-no-answer. The worker
  registers a `rag_index` job that can be enqueued explicitly, with an opt-in
  `EMBEDDINGS_INDEX_INTERVAL` schedule (default off).
- **Eval.** `testdata/rag/eval-draft.yaml` is a five-case format fixture with
  synthetic NRNs, not a quality measurement. `rag.Evaluate` scores recall at K
  per expected section and treats an unanswerable case as correct when every
  returned hit is below the similarity floor (0.35). The required 100+ labelled
  questions do not exist; neither does a real corpus or provider key.
- **Tests.** Unit tests cover chunk boundaries, sentence integrity, overlap,
  determinism, header separation, fake vectors, HTTP batching and dimension
  errors, and eval scoring. Integration tests cover idempotent re-indexing,
  changed-hash re-indexing, skipping OCR documents, cross-product isolation,
  section filters, lexical-only queries, model isolation, coverage, product
  resolution, and startup rejection of a mismatched dimension. No retrieval
  latency benchmark was added because there is no real corpus to measure.

## 11. Blockers and open items

All four external blockers remain open. None is resolved by this phase's code:

1. **Consented, double-labelled 100+ photo eval set.** The extraction harness
   is ready; the dataset is absent.
2. **Live provider acceptance run.** Needs `VISION_API_KEY`, pinned model ids,
   a provider spending cap, and data-handling approval.
3. **Data-handling decision** for sending pack photos to a foreign provider.
4. **NAFDAC data-sharing agreement** from phase 02.

Open items specific to this phase:

- **Embedding provider and dimension** are not decided. The schema fixes 1024
  and the generic HTTP client is provider-neutral; changing the dimension needs
  a migration and a full re-embed, so decide before the first production index.
- **No labelled retrieval eval set.** The scoring command and file format exist,
  but the 100+ questions (English first, then other languages) do not, so the
  agreed recall-at-5 gate has not been measured.
- **No real corpus or corpus coverage report.** Only synthetic fixtures and
  whatever a development database happens to hold. Products with OCR-only
  leaflets are excluded until text exists, and their share is unknown.
- **No production embedding run.** The fake embedder is the default, so the
  local index is lexical only and its search quality says nothing about the
  chosen provider.
- **Tables and flattened PDFs.** The chunker falls back to word splitting for
  very long sentences. A real leaflet sample has not been inspected to see how
  often table-heavy sections degrade.
- **Phase 09 dependency still open:** whether the assistant may relay leaflet
  dosage text (default: no, link only) is a pharmacist and project-lead decision.
