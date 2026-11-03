-- name: ListDocumentsForRAGIndex :many
-- Documents that are readable and have not yet been indexed for this model and
-- this exact file hash. Completed documents drop out, so a crashed backfill
-- resumes without re-embedding everything.
SELECT
  d.id AS document_id,
  d.product_id,
  d.source_url,
  d.sha256,
  COALESCE(p.nrn, '') AS nrn,
  p.name AS product_name
FROM smpc_documents d
JOIN products p ON p.id = d.product_id
WHERE d.status = 'ok'
  AND d.sha256 IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM smpc_chunks c
    WHERE c.document_id = d.id
      AND c.embedding_model = @embedding_model
      AND c.document_sha256 = d.sha256
  )
ORDER BY d.id
LIMIT @row_limit;

-- name: ListRAGDocumentSections :many
SELECT id, section_code, heading, text, page_start
FROM smpc_sections
WHERE document_id = @document_id
ORDER BY id;

-- name: DeleteRAGChunksForDocumentModel :exec
DELETE FROM smpc_chunks
WHERE document_id = @document_id AND embedding_model = @embedding_model;

-- name: InsertRAGChunk :exec
INSERT INTO smpc_chunks (
  product_id, document_id, section_id, section_code, heading, page_start,
  chunk_index, chunk_text, embedding_text, chunk_hash, token_count, embedding,
  embedding_model, document_sha256
) VALUES (
  @product_id, @document_id, sqlc.narg('section_id'), sqlc.narg('section_code'),
  sqlc.narg('heading'), sqlc.narg('page_start'), @chunk_index, @chunk_text,
  @embedding_text, @chunk_hash, @token_count, sqlc.arg('embedding')::vector,
  @embedding_model, @document_sha256
)
ON CONFLICT (document_id, chunk_index, embedding_model) DO UPDATE SET
  section_id = EXCLUDED.section_id,
  section_code = EXCLUDED.section_code,
  heading = EXCLUDED.heading,
  page_start = EXCLUDED.page_start,
  chunk_text = EXCLUDED.chunk_text,
  embedding_text = EXCLUDED.embedding_text,
  chunk_hash = EXCLUDED.chunk_hash,
  token_count = EXCLUDED.token_count,
  embedding = EXCLUDED.embedding,
  document_sha256 = EXCLUDED.document_sha256;

-- name: SearchRAGChunksByVector :many
SELECT
  c.id,
  c.document_id,
  c.section_id,
  c.section_code,
  c.heading,
  c.page_start,
  c.chunk_index,
  c.chunk_text,
  c.chunk_hash,
  c.embedding_model,
  d.source_url,
  (1 - (c.embedding <=> sqlc.arg('embedding')::vector))::double precision AS score
FROM smpc_chunks c
JOIN smpc_documents d ON d.id = c.document_id
WHERE c.product_id = @product_id
  AND c.embedding_model = @embedding_model
  AND (sqlc.narg('section_code')::text IS NULL OR c.section_code = sqlc.narg('section_code'))
ORDER BY c.embedding <=> sqlc.arg('embedding')::vector
LIMIT @row_limit;

-- name: SearchRAGChunksByText :many
SELECT
  c.id,
  c.document_id,
  c.section_id,
  c.section_code,
  c.heading,
  c.page_start,
  c.chunk_index,
  c.chunk_text,
  c.chunk_hash,
  c.embedding_model,
  d.source_url,
  ts_rank_cd(c.tsv, plainto_tsquery('english', @query))::double precision AS score
FROM smpc_chunks c
JOIN smpc_documents d ON d.id = c.document_id
WHERE c.product_id = @product_id
  AND c.embedding_model = @embedding_model
  AND c.tsv @@ plainto_tsquery('english', @query)
  AND (sqlc.narg('section_code')::text IS NULL OR c.section_code = sqlc.narg('section_code'))
ORDER BY score DESC, c.id
LIMIT @row_limit;

-- name: CountRAGChunksByModel :one
SELECT count(*)::bigint AS count
FROM smpc_chunks
WHERE embedding_model = @embedding_model;

-- name: RAGCoverage :one
SELECT
  (SELECT count(*) FROM products WHERE smpc_url <> '')::bigint AS products_with_leaflet,
  (SELECT count(DISTINCT product_id) FROM smpc_chunks WHERE embedding_model = @embedding_model)::bigint AS products_indexed,
  (SELECT count(DISTINCT product_id) FROM smpc_documents WHERE status = 'scanned_needs_ocr')::bigint AS products_needing_ocr,
  (SELECT count(DISTINCT product_id) FROM smpc_documents WHERE status = 'ok')::bigint AS products_with_readable_leaflet;

-- name: ListRAGChunksForProduct :many
SELECT
  c.id,
  c.document_id,
  c.section_id,
  c.section_code,
  c.heading,
  c.page_start,
  c.chunk_index,
  c.chunk_text,
  c.chunk_hash,
  c.embedding_model,
  d.source_url
FROM smpc_chunks c
JOIN smpc_documents d ON d.id = c.document_id
WHERE c.product_id = @product_id
  AND c.embedding_model = @embedding_model
ORDER BY c.document_id, c.chunk_index
LIMIT @row_limit;
