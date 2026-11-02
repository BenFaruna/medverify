-- +goose Up
-- One row per provider call. This is the cost and latency ledger, not a place
-- for prompts, images, or extracted text.
CREATE TABLE llm_calls (
  id bigserial PRIMARY KEY,
  -- Keeping the scan is useful for support; removing a scan anonymizes its
  -- calls instead of losing spend history.
  scan_id uuid REFERENCES scans(id) ON DELETE SET NULL,
  provider text NOT NULL,
  model text NOT NULL,
  prompt_version text NOT NULL,
  kind text NOT NULL DEFAULT 'vision'
    CHECK (kind IN ('vision', 'chat')),
  latency_ms integer NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
  input_units integer,
  output_units integer,
  cost_usd numeric(12, 6),
  outcome text NOT NULL
    CHECK (outcome IN ('success', 'unreadable', 'error')),
  error_code text,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX llm_calls_scan_created_idx ON llm_calls (scan_id, created_at DESC);
CREATE INDEX llm_calls_provider_created_idx ON llm_calls (provider, created_at DESC);
CREATE INDEX llm_calls_created_idx ON llm_calls (created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS llm_calls;
