-- +goose Up
-- Integrator API platform (phase 11). API keys are stored as SHA-256 hashes
-- with a public prefix for lookup; the full key is shown once at creation and
-- never persisted. Quotas are per client and day, stored in the database.
--
-- The phase 03 api_clients.key_hash column was a placeholder that could not
-- carry a prefix, scopes, or an expiry; replace it with api_keys rows.

ALTER TABLE api_clients
  ADD COLUMN sandbox boolean NOT NULL DEFAULT false,
  ADD COLUMN rate_per_min integer NOT NULL DEFAULT 60 CHECK (rate_per_min > 0),
  ADD COLUMN daily_request_quota integer NOT NULL DEFAULT 10000 CHECK (daily_request_quota > 0),
  ADD COLUMN daily_scan_quota integer NOT NULL DEFAULT 200 CHECK (daily_scan_quota > 0);

ALTER TABLE api_clients DROP COLUMN key_hash;

CREATE TABLE api_keys (
  id bigserial PRIMARY KEY,
  client_id bigint NOT NULL REFERENCES api_clients(id) ON DELETE CASCADE,
  key_prefix text NOT NULL UNIQUE,
  key_hash text NOT NULL UNIQUE,
  scopes text[] NOT NULL DEFAULT '{}',
  active boolean NOT NULL DEFAULT true,
  expires_at timestamptz,
  revoked_at timestamptz,
  last_used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX api_keys_client_idx ON api_keys (client_id, created_at DESC);

CREATE TABLE usage_counters (
  client_id bigint NOT NULL REFERENCES api_clients(id) ON DELETE CASCADE,
  day date NOT NULL,
  requests integer NOT NULL DEFAULT 0 CHECK (requests >= 0),
  scans integer NOT NULL DEFAULT 0 CHECK (scans >= 0),
  questions integer NOT NULL DEFAULT 0 CHECK (questions >= 0),
  cost_usd numeric(12, 6) NOT NULL DEFAULT 0 CHECK (cost_usd >= 0),
  PRIMARY KEY (client_id, day)
);

-- A scan now names the client row, not just a free-form key. The older
-- client_key column stays for existing local rows and idempotency records.
ALTER TABLE scans
  ADD COLUMN api_client_id bigint REFERENCES api_clients(id) ON DELETE SET NULL;

CREATE INDEX scans_api_client_created_idx ON scans (api_client_id, created_at DESC);

CREATE TABLE webhook_endpoints (
  id bigserial PRIMARY KEY,
  client_id bigint NOT NULL REFERENCES api_clients(id) ON DELETE CASCADE,
  url text NOT NULL,
  -- The per-endpoint signing secret must be available to sign deliveries. It
  -- is never returned by the API and must never be logged. Encryption at rest
  -- is a hardening item for phase 12.
  secret text NOT NULL,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX webhook_endpoints_client_idx ON webhook_endpoints (client_id, created_at DESC);

CREATE TABLE webhook_deliveries (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  endpoint_id bigint NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
  client_id bigint NOT NULL REFERENCES api_clients(id) ON DELETE CASCADE,
  scan_id uuid NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
  event_type text NOT NULL CHECK (event_type IN ('scan.completed')),
  payload jsonb NOT NULL,
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'delivered', 'dead')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_status_code integer,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  delivered_at timestamptz,
  dead_at timestamptz,
  UNIQUE (endpoint_id, scan_id, event_type)
);

CREATE INDEX webhook_deliveries_due_idx
  ON webhook_deliveries (status, next_attempt_at)
  WHERE status = 'pending';
CREATE INDEX webhook_deliveries_client_idx
  ON webhook_deliveries (client_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhook_endpoints;
DROP INDEX IF EXISTS scans_api_client_created_idx;
ALTER TABLE scans DROP COLUMN IF EXISTS api_client_id;
DROP TABLE IF EXISTS usage_counters;
DROP TABLE IF EXISTS api_keys;
ALTER TABLE api_clients
  DROP COLUMN IF EXISTS daily_scan_quota,
  DROP COLUMN IF EXISTS daily_request_quota,
  DROP COLUMN IF EXISTS rate_per_min,
  DROP COLUMN IF EXISTS sandbox;
ALTER TABLE api_clients ADD COLUMN key_hash text NOT NULL DEFAULT '';
ALTER TABLE api_clients ALTER COLUMN key_hash DROP DEFAULT;
