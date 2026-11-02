-- +goose Up
-- Scan pipeline tables (phase 03). Images are re-encoded before storage, so
-- these rows never point at an original upload. Precise user coordinates are
-- not stored; only the coarse grid cell in scans.location_cell.

CREATE TABLE api_clients (
  id bigserial PRIMARY KEY,
  name text NOT NULL UNIQUE,
  key_hash text NOT NULL UNIQUE,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE scans (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  -- client_key is the placeholder auth principal from phase 03. Phase 11
  -- replaces it with a foreign key to api_clients.
  client_key text NOT NULL,
  status text NOT NULL DEFAULT 'queued'
    CHECK (status IN ('queued', 'processing', 'completed', 'unreadable', 'failed')),
  language text,
  location_cell text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);

CREATE INDEX scans_client_created_idx ON scans (client_key, created_at DESC);
CREATE INDEX scans_status_updated_idx ON scans (status, updated_at);

CREATE TABLE scan_images (
  id bigserial PRIMARY KEY,
  scan_id uuid NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
  position smallint NOT NULL CHECK (position BETWEEN 1 AND 4),
  storage_key text NOT NULL UNIQUE,
  content_type text NOT NULL,
  byte_size integer NOT NULL CHECK (byte_size > 0),
  width integer NOT NULL CHECK (width > 0),
  height integer NOT NULL CHECK (height > 0),
  sha256 text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (scan_id, position)
);

CREATE INDEX scan_images_scan_idx ON scan_images (scan_id);

CREATE TABLE scan_results (
  scan_id uuid PRIMARY KEY REFERENCES scans(id) ON DELETE CASCADE,
  -- verdict and extracted stay NULL until phases 07 and 04 fill them.
  verdict jsonb,
  reasons jsonb NOT NULL DEFAULT '[]'::jsonb,
  extracted jsonb,
  provider text,
  error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE idempotency_keys (
  client_key text NOT NULL,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  scan_id uuid NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (client_key, idempotency_key)
);

-- +goose Down
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS scan_results;
DROP TABLE IF EXISTS scan_images;
DROP TABLE IF EXISTS scans;
DROP TABLE IF EXISTS api_clients;
