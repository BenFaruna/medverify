-- Queries for the Greenbook registry. Hand-written SQL only; sqlc compiles it.

-- name: UpsertProduct :one
-- Insert or update a product when its payload hash changed. The xmax trick
-- reports whether the row was inserted (true) or updated (false). No row is
-- returned when the hash is unchanged, which callers treat as a no-op.
INSERT INTO products (
  source_id, nrn, name, category, status, applicant, manufacturer,
  manufacturer_source_id, composition_raw, form, route, strength_raw,
  pack_size, atc_code, approved_at, expires_at, smpc_url, raw, payload_hash,
  fetched_at
) VALUES (
  @source_id,
  sqlc.narg('nrn'),
  @name,
  sqlc.narg('category'),
  @status,
  sqlc.narg('applicant'),
  sqlc.narg('manufacturer'),
  sqlc.narg('manufacturer_source_id'),
  sqlc.narg('composition_raw'),
  sqlc.narg('form'),
  sqlc.narg('route'),
  sqlc.narg('strength_raw'),
  sqlc.narg('pack_size'),
  sqlc.narg('atc_code'),
  sqlc.narg('approved_at'),
  sqlc.narg('expires_at'),
  sqlc.narg('smpc_url'),
  @raw,
  @payload_hash,
  @fetched_at
)
ON CONFLICT (source_id) DO UPDATE SET
  nrn = EXCLUDED.nrn,
  name = EXCLUDED.name,
  category = EXCLUDED.category,
  status = EXCLUDED.status,
  applicant = EXCLUDED.applicant,
  manufacturer = EXCLUDED.manufacturer,
  manufacturer_source_id = EXCLUDED.manufacturer_source_id,
  composition_raw = EXCLUDED.composition_raw,
  form = EXCLUDED.form,
  route = EXCLUDED.route,
  strength_raw = EXCLUDED.strength_raw,
  pack_size = EXCLUDED.pack_size,
  atc_code = EXCLUDED.atc_code,
  approved_at = EXCLUDED.approved_at,
  expires_at = EXCLUDED.expires_at,
  smpc_url = EXCLUDED.smpc_url,
  raw = EXCLUDED.raw,
  payload_hash = EXCLUDED.payload_hash,
  fetched_at = EXCLUDED.fetched_at,
  updated_at = now()
WHERE products.payload_hash IS DISTINCT FROM EXCLUDED.payload_hash
RETURNING id, (xmax = 0)::boolean AS inserted;

-- name: DeleteProductIngredients :exec
DELETE FROM product_ingredients WHERE product_id = @product_id;

-- name: InsertProductIngredient :exec
INSERT INTO product_ingredients (
  product_id, name_raw, name_norm, strength_value, strength_unit, per_unit,
  strength_raw
) VALUES (
  @product_id, @name_raw, @name_norm, sqlc.narg('strength_value'),
  sqlc.narg('strength_unit'), sqlc.narg('per_unit'), sqlc.narg('strength_raw')
)
ON CONFLICT DO NOTHING;

-- name: ListProductIngredients :many
SELECT * FROM product_ingredients
WHERE product_id = @product_id
ORDER BY id;

-- name: StartSyncRun :one
INSERT INTO sync_runs (started_at, status)
VALUES (@started_at, 'running')
RETURNING *;

-- name: FinishSyncRun :exec
UPDATE sync_runs
SET finished_at = @finished_at,
    status = @status,
    rows_seen = @rows_seen,
    rows_added = @rows_added,
    rows_changed = @rows_changed,
    rows_failed = @rows_failed,
    error = sqlc.narg('error')
WHERE id = @id;

-- name: LastSuccessfulSyncRun :one
SELECT * FROM sync_runs
WHERE status = 'succeeded'
ORDER BY finished_at DESC NULLS LAST
LIMIT 1;

-- name: LatestSyncRun :one
SELECT * FROM sync_runs
ORDER BY started_at DESC
LIMIT 1;

-- name: GetProductByNRN :one
SELECT * FROM products WHERE nrn = @nrn;

-- name: GetProductBySourceID :one
SELECT * FROM products WHERE source_id = @source_id;

-- name: SearchProductsByName :many
-- Trigram search ranked by similarity. The pg_trgm % operator keeps the index
-- usable; similarity() orders the results so typos and word-order changes rank
-- below exact matches.
SELECT sqlc.embed(products), similarity(products.name, @query)::double precision AS score
FROM products
WHERE products.name % @query
ORDER BY score DESC, products.name
LIMIT @row_limit;

-- name: ListProductsByIngredient :many
SELECT DISTINCT products.*
FROM products
JOIN product_ingredients ON product_ingredients.product_id = products.id
WHERE product_ingredients.name_norm = @name_norm
ORDER BY products.name
LIMIT @row_limit;

-- name: GetSMPCDocument :one
SELECT * FROM smpc_documents
WHERE product_id = @product_id AND source_url = @source_url;

-- name: UpsertSMPCDocument :one
INSERT INTO smpc_documents (
  product_id, source_url, sha256, storage_key, fetched_at, extraction_method,
  status, error
) VALUES (
  @product_id, @source_url, sqlc.narg('sha256'), sqlc.narg('storage_key'),
  sqlc.narg('fetched_at'), sqlc.narg('extraction_method'), @status,
  sqlc.narg('error')
)
ON CONFLICT (product_id, source_url) DO UPDATE SET
  sha256 = EXCLUDED.sha256,
  storage_key = EXCLUDED.storage_key,
  fetched_at = EXCLUDED.fetched_at,
  extraction_method = EXCLUDED.extraction_method,
  status = EXCLUDED.status,
  error = EXCLUDED.error
RETURNING *;

-- name: DeleteSMPCSections :exec
DELETE FROM smpc_sections WHERE document_id = @document_id;

-- name: InsertSMPCSection :exec
INSERT INTO smpc_sections (document_id, section_code, heading, text, page_start)
VALUES (
  @document_id, sqlc.narg('section_code'), sqlc.narg('heading'), @text,
  sqlc.narg('page_start')
);

-- name: ListSMPCSections :many
SELECT * FROM smpc_sections
WHERE document_id = @document_id
ORDER BY id;

-- name: ListProductsForLeafletSync :many
-- Products whose leaflet is missing, failed, or older than the product record.
-- Successful, scanned, and oversized documents are not re-downloaded every
-- night unless the product itself changed.
SELECT products.id, products.source_id, products.smpc_url
FROM products
LEFT JOIN smpc_documents
  ON smpc_documents.product_id = products.id
 AND smpc_documents.source_url = products.smpc_url
WHERE products.smpc_url <> ''
  AND (
    smpc_documents.id IS NULL
    OR smpc_documents.status = 'failed'
    OR (smpc_documents.status = 'ok' AND products.updated_at > smpc_documents.fetched_at)
    OR (smpc_documents.status = 'scanned_needs_ocr' AND products.updated_at > smpc_documents.fetched_at)
  )
ORDER BY products.id
LIMIT @row_limit;

-- name: CountProducts :one
SELECT count(*)::bigint AS count FROM products;

-- name: CountProductIngredients :one
SELECT count(*)::bigint AS count FROM product_ingredients;

-- name: CountProductsWithLeafletURL :one
SELECT count(*)::bigint AS count FROM products WHERE smpc_url <> '';

-- name: CountSMPCDocumentsByStatus :many
SELECT status, count(*)::bigint AS count
FROM smpc_documents
GROUP BY status
ORDER BY status;

-- name: CountSMPCSections :one
SELECT count(*)::bigint AS count FROM smpc_sections;

-- name: ListRecentSyncRuns :many
SELECT * FROM sync_runs
ORDER BY started_at DESC
LIMIT @row_limit;
