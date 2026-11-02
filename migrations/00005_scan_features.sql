-- +goose Up
-- Registry verification features from phase 05. This is the explainable match
-- result: outcome, per-field comparisons, candidates, and dataset coverage. It
-- never contains a safety or authenticity claim.
ALTER TABLE scan_results
  ADD COLUMN features jsonb NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE scan_results
  DROP COLUMN IF EXISTS features;
