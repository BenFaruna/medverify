-- +goose Up
-- Leaflet chunks for retrieval (phase 08). Every row is tied to one product and
-- one document, and stores the model that produced its vector: a re-embed is a
-- new set of rows, never a mix of vector spaces.
--
-- The dimension is fixed by the embedding model. It is 1024 here because the
-- provider decision is still open and every candidate we are considering has a
-- 1024-dimension multilingual variant. Changing it needs a migration and a full
-- re-embed, as documented in the phase 08 notes.
CREATE TABLE smpc_chunks (
  id bigserial PRIMARY KEY,
  product_id bigint NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  document_id bigint NOT NULL REFERENCES smpc_documents(id) ON DELETE CASCADE,
  section_id bigint REFERENCES smpc_sections(id) ON DELETE CASCADE,
  section_code text,
  heading text,
  page_start integer,
  chunk_index integer NOT NULL,
  chunk_text text NOT NULL,
  embedding_text text NOT NULL,
  chunk_hash text NOT NULL,
  token_count integer NOT NULL,
  embedding vector(1024) NOT NULL,
  embedding_model text NOT NULL,
  document_sha256 text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  tsv tsvector GENERATED ALWAYS AS (to_tsvector('english', chunk_text)) STORED,
  UNIQUE (document_id, chunk_index, embedding_model)
);

-- Retrieval always filters by product, so a plain composite index is enough.
-- No approximate vector index yet: with a few dozen rows per product an exact
-- scan is both faster and fully accurate, and ANN indexes combined with filters
-- silently return fewer results than requested.
CREATE INDEX smpc_chunks_product_idx ON smpc_chunks (product_id, section_code);
CREATE INDEX smpc_chunks_tsv_idx ON smpc_chunks USING gin (tsv);
CREATE INDEX smpc_chunks_model_idx ON smpc_chunks (embedding_model, product_id);
CREATE INDEX smpc_chunks_document_idx ON smpc_chunks (document_id);

-- +goose Down
DROP TABLE IF EXISTS smpc_chunks;
