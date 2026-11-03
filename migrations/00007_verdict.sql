-- +goose Up
-- Phase 07 replaces the unused nullable verdict placeholder with the typed
-- output of the verdict engine, plus the normalized signals it was computed
-- from. Storing signals is what makes a result replayable under a new rules
-- version without the images, the database, or the clock.
ALTER TABLE scan_results
  DROP COLUMN verdict,
  ADD COLUMN verdict_code text,
  ADD COLUMN verdict_score smallint,
  ADD COLUMN verdict_confidence text,
  ADD COLUMN verdict_rules_version text,
  ADD COLUMN signals jsonb;

-- A result is either fully scored or not scored at all. A half-written verdict
-- would be impossible to audit, so the database refuses it.
ALTER TABLE scan_results
  ADD CONSTRAINT scan_results_verdict_code_check
    CHECK (verdict_code IS NULL OR verdict_code IN (
      'consistent_with_records',
      'mismatch_found',
      'not_found_in_records',
      'flagged',
      'needs_review',
      'unreadable'
    )),
  ADD CONSTRAINT scan_results_verdict_score_check
    CHECK (verdict_score IS NULL OR verdict_score BETWEEN 0 AND 100),
  ADD CONSTRAINT scan_results_verdict_confidence_check
    CHECK (verdict_confidence IS NULL OR verdict_confidence IN ('low', 'medium', 'high')),
  ADD CONSTRAINT scan_results_verdict_complete_check
    CHECK (
      (verdict_code IS NULL AND verdict_score IS NULL AND verdict_confidence IS NULL
        AND verdict_rules_version IS NULL AND signals IS NULL)
      OR
      (verdict_code IS NOT NULL AND verdict_score IS NOT NULL AND verdict_confidence IS NOT NULL
        AND verdict_rules_version IS NOT NULL AND signals IS NOT NULL)
    );

-- Replay and analytics read stored verdicts by version and recency.
CREATE INDEX scan_results_verdict_idx ON scan_results (verdict_code, created_at DESC);
CREATE INDEX scan_results_rules_version_idx ON scan_results (verdict_rules_version);

-- +goose Down
DROP INDEX IF EXISTS scan_results_rules_version_idx;
DROP INDEX IF EXISTS scan_results_verdict_idx;

ALTER TABLE scan_results
  DROP CONSTRAINT IF EXISTS scan_results_verdict_complete_check,
  DROP CONSTRAINT IF EXISTS scan_results_verdict_confidence_check,
  DROP CONSTRAINT IF EXISTS scan_results_verdict_score_check,
  DROP CONSTRAINT IF EXISTS scan_results_verdict_code_check,
  DROP COLUMN IF EXISTS signals,
  DROP COLUMN IF EXISTS verdict_rules_version,
  DROP COLUMN IF EXISTS verdict_confidence,
  DROP COLUMN IF EXISTS verdict_score,
  DROP COLUMN IF EXISTS verdict_code,
  ADD COLUMN verdict jsonb;
