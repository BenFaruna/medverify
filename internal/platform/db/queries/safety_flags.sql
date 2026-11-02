-- Safety flags raised by registry verification (phase 06). Flags are replaced
-- atomically with each terminal scan result, so a retry never leaves stale
-- rows behind.

-- name: DeleteSafetyFlagsForScan :exec
DELETE FROM safety_flags WHERE scan_id = @scan_id;

-- name: InsertSafetyFlag :exec
INSERT INTO safety_flags (
  scan_id, flag_type, severity, reason_code, evidence, source, watchlist_entry_id
)
VALUES (
  @scan_id, @flag_type, @severity, @reason_code, @evidence, @source,
  sqlc.narg('watchlist_entry_id')
);

-- name: ListSafetyFlagsForScan :many
SELECT * FROM safety_flags WHERE scan_id = @scan_id ORDER BY id;
