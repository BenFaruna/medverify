-- Queries for self-service accounts. A user owns one api_clients row, so these
-- sit beside the key queries in the same tenant model.

-- name: CreateUser :one
INSERT INTO users (email, name, password_hash, client_id)
VALUES (@email, @name, @password_hash, @client_id)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower(@email);

-- name: GetUserByID :one
SELECT * FROM users WHERE id = @id;

-- name: TouchUserLogin :exec
UPDATE users
SET last_login_at = now(), updated_at = now()
WHERE id = @id;

-- name: CreateUserSession :one
INSERT INTO user_sessions (user_id, token_hash, expires_at)
VALUES (@user_id, @token_hash, @expires_at)
RETURNING *;

-- name: GetUserBySessionToken :one
SELECT u.*
FROM user_sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = @token_hash
  AND s.revoked_at IS NULL
  AND s.expires_at > now()
  AND u.active;

-- name: TouchUserSession :exec
UPDATE user_sessions SET last_seen_at = now() WHERE token_hash = @token_hash;

-- name: RevokeUserSession :execrows
UPDATE user_sessions
SET revoked_at = now()
WHERE token_hash = @token_hash AND revoked_at IS NULL;

-- name: RevokeUserSessionsForUser :execrows
UPDATE user_sessions
SET revoked_at = now()
WHERE user_id = @user_id AND revoked_at IS NULL;

-- name: ListAPIKeysForClient :many
SELECT id, client_id, key_prefix, scopes, active, expires_at, revoked_at,
       last_used_at, created_at
FROM api_keys
WHERE client_id = @client_id
ORDER BY created_at DESC, id DESC;

-- name: RotateAPIKeysForClient :execrows
UPDATE api_keys
SET active = false, revoked_at = now()
WHERE client_id = @client_id AND revoked_at IS NULL;
