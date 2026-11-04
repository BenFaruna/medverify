-- +goose Up
-- Sandbox scans return a fixed result and never call a vision or language
-- provider. The marker is stored on the row rather than inferred from the
-- request, because the worker processes the scan in another request, later, and
-- must still know not to spend money on it.

ALTER TABLE scans ADD COLUMN sandbox boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE scans DROP COLUMN IF EXISTS sandbox;
