-- Queries for the grounded assistant (phase 09). All reads are scoped by the
-- conversation's scan; there is no query that accepts a product or NRN from a
-- caller in place of a scan.

-- name: GetConversationByScan :one
SELECT * FROM conversations WHERE scan_id = @scan_id;

-- name: CreateConversation :one
INSERT INTO conversations (
  id, scan_id, language, rules_version, prompt_version, expires_at
) VALUES (
  @id, @scan_id, @language, @rules_version, @prompt_version, @expires_at
)
ON CONFLICT (scan_id) DO NOTHING
RETURNING *;

-- name: TouchConversation :exec
UPDATE conversations SET last_turn_at = now() WHERE id = @id;

-- name: CountConversationUserMessages :one
SELECT count(*)::bigint AS count
FROM messages
WHERE conversation_id = @conversation_id AND role = 'user';

-- name: InsertMessage :one
INSERT INTO messages (
  conversation_id, scan_id, turn, role, content, intent, language,
  tool_names, source_ids, verifier, refusal_reason
) VALUES (
  @conversation_id, @scan_id, @turn, @role, @content, sqlc.narg('intent'),
  sqlc.narg('language'), @tool_names, @source_ids, sqlc.narg('verifier'),
  sqlc.narg('refusal_reason')
)
RETURNING *;

-- name: ListMessages :many
SELECT * FROM messages
WHERE conversation_id = @conversation_id
ORDER BY turn, id;

-- name: InsertAssistantToolCall :exec
INSERT INTO assistant_tool_calls (
  conversation_id, scan_id, turn, tool_name, arguments, outcome, source_ids,
  latency_ms
) VALUES (
  @conversation_id, @scan_id, @turn, @tool_name, @arguments, @outcome,
  @source_ids, @latency_ms
);

-- name: DeleteExpiredConversations :execrows
DELETE FROM conversations WHERE expires_at < now();
