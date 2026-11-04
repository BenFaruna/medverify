-- Queries for phase 12 data retention and erasure. Retention never deletes the
-- scan row itself: the result, flags, and audit trail stay, while the images
-- (the personal data) go. Flagged scans keep their images for a longer review
-- window, which is the documented exception path.

-- name: ListRetentionImages :many
SELECT si.id, si.storage_key, si.scan_id
FROM scan_images si
JOIN scans s ON s.id = si.scan_id
WHERE s.created_at < sqlc.arg(flagged_cutoff)::timestamptz
   OR (s.created_at < sqlc.arg(plain_cutoff)::timestamptz
       AND NOT EXISTS (SELECT 1 FROM safety_flags f WHERE f.scan_id = s.id))
ORDER BY si.id
LIMIT sqlc.arg(batch)::integer;

-- name: DeleteScanImageByID :exec
DELETE FROM scan_images WHERE id = @id;

-- name: ListScanImageKeys :many
SELECT storage_key FROM scan_images WHERE scan_id = @scan_id;

-- name: DeleteScanByID :execrows
DELETE FROM scans WHERE id = @scan_id;
