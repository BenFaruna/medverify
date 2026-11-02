-- Watchlist sources and entries (phase 06). Only the admin import command
-- writes these rows; the worker and API only read them.

-- name: UpsertWatchlistSource :one
INSERT INTO watchlist_sources (issuing_body, title, source_url, issued_at)
VALUES (@issuing_body, @title, @source_url, @issued_at)
ON CONFLICT (source_url, issued_at) DO UPDATE SET
  issuing_body = EXCLUDED.issuing_body,
  title = EXCLUDED.title
RETURNING *;

-- name: InsertWatchlistEntry :one
INSERT INTO watchlist_entries (
  source_id, kind, nrn, product_name, product_name_norm, manufacturer,
  manufacturer_norm, ingredient_norm, reason, effective_until, reviewed_by,
  review_due_at
)
VALUES (
  @source_id, @kind, sqlc.narg('nrn'), sqlc.narg('product_name'),
  sqlc.narg('product_name_norm'), sqlc.narg('manufacturer'),
  sqlc.narg('manufacturer_norm'), sqlc.narg('ingredient_norm'),
  @reason, sqlc.narg('effective_until'), @reviewed_by,
  sqlc.narg('review_due_at')
)
RETURNING id;

-- name: ListWatchlistEntries :many
SELECT
  sqlc.embed(e),
  s.issuing_body AS source_issuing_body,
  s.title AS source_title,
  s.source_url AS source_url,
  s.issued_at AS source_issued_at
FROM watchlist_entries e
JOIN watchlist_sources s ON s.id = e.source_id
ORDER BY e.id;

-- name: ListLiveWatchlistEntriesByNRN :many
SELECT
  sqlc.embed(e),
  s.issuing_body AS source_issuing_body,
  s.title AS source_title,
  s.source_url AS source_url,
  s.issued_at AS source_issued_at
FROM watchlist_entries e
JOIN watchlist_sources s ON s.id = e.source_id
WHERE e.nrn = @nrn
  AND e.active
  AND (e.effective_until IS NULL OR e.effective_until >= @today);

-- name: SearchLiveWatchlistEntriesByName :many
SELECT
  sqlc.embed(e),
  s.issuing_body AS source_issuing_body,
  s.title AS source_title,
  s.source_url AS source_url,
  s.issued_at AS source_issued_at,
  similarity(e.product_name_norm, @query)::double precision AS score
FROM watchlist_entries e
JOIN watchlist_sources s ON s.id = e.source_id
WHERE e.product_name_norm % @query
  AND e.active
  AND (e.effective_until IS NULL OR e.effective_until >= @today)
ORDER BY score DESC, e.id
LIMIT @row_limit;

-- name: ListLiveWatchlistEntriesByIngredients :many
SELECT
  sqlc.embed(e),
  s.issuing_body AS source_issuing_body,
  s.title AS source_title,
  s.source_url AS source_url,
  s.issued_at AS source_issued_at
FROM watchlist_entries e
JOIN watchlist_sources s ON s.id = e.source_id
WHERE e.ingredient_norm = ANY(@ingredients::text[])
  AND e.active
  AND (e.effective_until IS NULL OR e.effective_until >= @today)
ORDER BY e.id;

-- name: DeactivateWatchlistEntry :execrows
UPDATE watchlist_entries SET active = false WHERE id = @id;

-- name: WatchlistCounts :one
SELECT
  count(*)::bigint AS total,
  count(*) FILTER (WHERE active)::bigint AS active
FROM watchlist_entries;

-- name: ListStaleWatchlistEntries :many
SELECT
  sqlc.embed(e),
  s.issuing_body AS source_issuing_body,
  s.title AS source_title,
  s.source_url AS source_url,
  s.issued_at AS source_issued_at
FROM watchlist_entries e
JOIN watchlist_sources s ON s.id = e.source_id
WHERE e.active
  AND e.review_due_at IS NOT NULL
  AND e.review_due_at < @today
ORDER BY e.review_due_at, e.id;
