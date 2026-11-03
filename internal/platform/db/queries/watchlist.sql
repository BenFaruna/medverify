-- Watchlist sources and entries (phase 06). Only the admin import command
-- writes these rows; the worker and API only read them.

-- name: UpsertWatchlistSource :one
INSERT INTO watchlist_sources (issuing_body, title, source_url, issued_at)
VALUES (@issuing_body, @title, @source_url, @issued_at)
ON CONFLICT (source_url, issued_at) DO UPDATE SET
  issuing_body = EXCLUDED.issuing_body,
  title = EXCLUDED.title
-- xmax is 0 only for a freshly inserted row, so a re-import can report how
-- many source documents it actually added.
RETURNING id, (xmax = 0) AS inserted;

-- name: InsertWatchlistEntry :execrows
-- A repeated import of the same reviewed file inserts nothing new.
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
ON CONFLICT DO NOTHING;

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
-- Nearest-neighbour search: the inner query uses the GiST trigram index and
-- stops after @row_limit names, so a large watchlist is never scored in full.
-- The similarity threshold is applied to those candidates, which can return
-- fewer than @row_limit rows.
WITH nearest AS (
  SELECT
    e.id,
    (e.product_name_norm <-> @query::text)::double precision AS distance
  FROM watchlist_entries e
  WHERE e.active
    AND (e.effective_until IS NULL OR e.effective_until >= @today)
    AND e.product_name_norm IS NOT NULL
  ORDER BY e.product_name_norm <-> @query::text
  LIMIT @row_limit
)
SELECT
  sqlc.embed(e),
  s.issuing_body AS source_issuing_body,
  s.title AS source_title,
  s.source_url AS source_url,
  s.issued_at AS source_issued_at,
  (1 - nearest.distance)::double precision AS score
FROM nearest
JOIN watchlist_entries e ON e.id = nearest.id
JOIN watchlist_sources s ON s.id = e.source_id
WHERE nearest.distance <= 1 - @min_score::double precision
ORDER BY nearest.distance, e.id;

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
