-- name: Ping :one
-- Used by readiness probes and by smoke tests. It keeps the sqlc pipeline
-- exercised until phase 02 adds real tables and queries.
SELECT 1::bigint AS ok;
