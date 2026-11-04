-- Queries for the integrator API platform (phase 11). Keys are looked up by
-- public prefix; the full key is never stored or logged.

-- name: CreateAPIClient :one
INSERT INTO api_clients (name, sandbox, rate_per_min, daily_request_quota, daily_scan_quota)
VALUES (@name, @sandbox, @rate_per_min, @daily_request_quota, @daily_scan_quota)
RETURNING *;

-- name: GetAPIClientByName :one
SELECT * FROM api_clients WHERE name = @name;

-- name: CreateAPIKey :one
INSERT INTO api_keys (client_id, key_prefix, key_hash, scopes, expires_at)
VALUES (@client_id, @key_prefix, @key_hash, @scopes, sqlc.narg('expires_at'))
RETURNING *;

-- name: GetAPIKeyByPrefix :one
SELECT
  k.id, k.client_id, k.key_prefix, k.key_hash, k.scopes, k.active,
  k.expires_at, k.revoked_at, k.last_used_at, k.created_at,
  c.name AS client_name, c.active AS client_active, c.sandbox, c.rate_per_min,
  c.daily_request_quota, c.daily_scan_quota
FROM api_keys k
JOIN api_clients c ON c.id = k.client_id
WHERE k.key_prefix = @key_prefix;

-- name: TouchAPIKey :exec
UPDATE api_keys SET last_used_at = now() WHERE id = @id;

-- name: RevokeAPIKey :execrows
UPDATE api_keys
SET active = false, revoked_at = now()
WHERE key_prefix = @key_prefix AND revoked_at IS NULL;

-- name: ListAPIKeys :many
SELECT
  k.id, k.client_id, k.key_prefix, k.scopes, k.active, k.expires_at,
  k.revoked_at, k.last_used_at, k.created_at,
  c.name AS client_name
FROM api_keys k
JOIN api_clients c ON c.id = k.client_id
ORDER BY k.created_at DESC;

-- name: EnsureUsageRow :exec
INSERT INTO usage_counters (client_id, day)
VALUES (@client_id, @day)
ON CONFLICT (client_id, day) DO NOTHING;

-- name: GetUsageForUpdate :one
SELECT * FROM usage_counters
WHERE client_id = @client_id AND day = @day
FOR UPDATE;

-- name: IncrementUsage :exec
UPDATE usage_counters
SET requests = requests + 1,
    scans = scans + @scan_inc,
    questions = questions + @question_inc
WHERE client_id = @client_id AND day = @day;

-- name: CreateWebhookEndpoint :one
INSERT INTO webhook_endpoints (client_id, url, secret)
VALUES (@client_id, @url, @secret)
RETURNING *;

-- name: ListWebhookEndpoints :many
SELECT * FROM webhook_endpoints
WHERE client_id = @client_id AND active
ORDER BY id;

-- name: ListAllWebhookEndpoints :many
SELECT * FROM webhook_endpoints
WHERE active
ORDER BY id;

-- name: InsertWebhookDelivery :one
INSERT INTO webhook_deliveries (
  endpoint_id, client_id, scan_id, event_type, payload
) VALUES (
  @endpoint_id, @client_id, @scan_id, @event_type, @payload
)
ON CONFLICT (endpoint_id, scan_id, event_type) DO NOTHING
RETURNING *;

-- name: ListDueWebhookDeliveries :many
SELECT d.*, e.url, e.secret
FROM webhook_deliveries d
JOIN webhook_endpoints e ON e.id = d.endpoint_id
WHERE d.status = 'pending' AND d.next_attempt_at <= now()
ORDER BY d.next_attempt_at
LIMIT @row_limit;

-- name: MarkWebhookDelivered :exec
UPDATE webhook_deliveries
SET status = 'delivered', delivered_at = now(), attempts = attempts + 1,
    last_status_code = @status_code, last_error = NULL
WHERE id = @id AND status = 'pending';

-- name: RetryWebhookDelivery :exec
UPDATE webhook_deliveries
SET attempts = attempts + 1, next_attempt_at = @next_attempt_at,
    last_status_code = @status_code, last_error = @last_error
WHERE id = @id AND status = 'pending';

-- name: DeadLetterWebhookDelivery :exec
UPDATE webhook_deliveries
SET status = 'dead', dead_at = now(), attempts = attempts + 1,
    last_status_code = @status_code, last_error = @last_error
WHERE id = @id AND status = 'pending';

-- name: ReplayWebhookDelivery :execrows
UPDATE webhook_deliveries
SET status = 'pending', next_attempt_at = now(), dead_at = NULL,
    delivered_at = NULL, last_error = NULL
WHERE id = @id AND status = 'dead';

-- name: ListDeadWebhookDeliveries :many
SELECT
  d.id, d.scan_id, d.event_type, d.attempts, d.last_status_code, d.last_error,
  d.created_at, d.dead_at, e.url, c.name AS client_name
FROM webhook_deliveries d
JOIN webhook_endpoints e ON e.id = d.endpoint_id
JOIN api_clients c ON c.id = d.client_id
WHERE d.status = 'dead'
ORDER BY d.dead_at DESC
LIMIT @max_rows;
