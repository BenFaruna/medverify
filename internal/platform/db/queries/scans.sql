-- Queries for the scan pipeline (phase 03). Hand-written SQL only; sqlc
-- compiles it.

-- name: CreateScan :one
INSERT INTO scans (id, client_key, language, location_cell)
VALUES (@id, @client_key, sqlc.narg('language'), sqlc.narg('location_cell'))
RETURNING *;

-- name: InsertScanImage :one
INSERT INTO scan_images (
  scan_id, position, storage_key, content_type, byte_size, width, height, sha256
) VALUES (
  @scan_id, @position, @storage_key, @content_type, @byte_size, @width,
  @height, @sha256
)
RETURNING *;

-- name: GetScan :one
SELECT * FROM scans WHERE id = @id;

-- name: ClaimScan :execrows
-- The guarded update is the state machine: only a queued row moves to
-- processing, so two workers cannot process the same scan.
UPDATE scans
SET status = 'processing', updated_at = now()
WHERE id = @id AND status = 'queued';

-- name: RequeueFailedScan :execrows
-- River retries a failed job. Moving failed back to queued is the only
-- transition that lets the retry claim the scan again.
UPDATE scans
SET status = 'queued', updated_at = now(), completed_at = NULL
WHERE id = @id AND status = 'failed';

-- name: FinishScan :execrows
-- A terminal write only applies to a row this worker claimed.
UPDATE scans
SET status = @status,
    updated_at = now(),
    completed_at = @completed_at
WHERE id = @id AND status = 'processing';

-- name: UpsertScanResult :exec
INSERT INTO scan_results (scan_id, verdict, reasons, extracted, features, provider, error)
VALUES (
  @scan_id, sqlc.narg('verdict'), @reasons, sqlc.narg('extracted'),
  @features, sqlc.narg('provider'), sqlc.narg('error')
)
ON CONFLICT (scan_id) DO UPDATE SET
  verdict = EXCLUDED.verdict,
  reasons = EXCLUDED.reasons,
  extracted = EXCLUDED.extracted,
  features = EXCLUDED.features,
  provider = EXCLUDED.provider,
  error = EXCLUDED.error,
  updated_at = now();

-- name: GetScanResult :one
SELECT * FROM scan_results WHERE scan_id = @scan_id;

-- name: ListScanImages :many
SELECT * FROM scan_images WHERE scan_id = @scan_id ORDER BY position;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys
WHERE client_key = @client_key AND idempotency_key = @idempotency_key;

-- name: InsertIdempotencyKey :one
-- The primary key makes the race safe: one concurrent request wins and the
-- other receives no row, then reads the winner.
INSERT INTO idempotency_keys (client_key, idempotency_key, request_hash, scan_id)
VALUES (@client_key, @idempotency_key, @request_hash, @scan_id)
ON CONFLICT DO NOTHING
RETURNING *;
