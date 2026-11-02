-- Cost and latency records for provider calls. No prompt or response text is
-- stored here.

-- name: InsertLLMCall :one
INSERT INTO llm_calls (
  scan_id, provider, model, prompt_version, kind, latency_ms,
  input_units, output_units, cost_usd, outcome, error_code
) VALUES (
  sqlc.narg('scan_id'), @provider, @model, @prompt_version, @kind, @latency_ms,
  sqlc.narg('input_units'), sqlc.narg('output_units'), sqlc.narg('cost_usd'),
  @outcome, sqlc.narg('error_code')
)
RETURNING *;
