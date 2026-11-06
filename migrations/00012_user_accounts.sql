-- +goose Up
-- Self-service accounts. A registered person owns exactly one api_clients row,
-- so the key issued at registration authenticates the same /v1 endpoints as a
-- key an operator makes with `admin apikey create`. The API key stays the only
-- secret the API checks for scans.
--
-- Passwords are stored as PBKDF2-HMAC-SHA256 derives; the full scheme travels
-- with the hash so the cost can be raised without breaking sign-in. Session
-- tokens are random and stored only as SHA-256 hashes, exactly like API keys,
-- so a database read cannot be replayed as a login.

CREATE TABLE users (
  id bigserial PRIMARY KEY,
  email text NOT NULL,
  name text NOT NULL DEFAULT '',
  password_hash text NOT NULL,
  -- One tenant per person. ON DELETE CASCADE removes the account's keys and
  -- scans (through api_keys and scans.api_client_id) with the user.
  client_id bigint NOT NULL UNIQUE REFERENCES api_clients(id) ON DELETE CASCADE,
  active boolean NOT NULL DEFAULT true,
  last_login_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- The writer stores a lower-cased address, and this index also blocks a
-- mixed-case duplicate from any other writer.
CREATE UNIQUE INDEX users_email_lower_uidx ON users (lower(email));

CREATE TABLE user_sessions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz
);

CREATE INDEX user_sessions_user_idx ON user_sessions (user_id, created_at DESC);
CREATE INDEX user_sessions_expiry_idx ON user_sessions (expires_at) WHERE revoked_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS user_sessions_expiry_idx;
DROP INDEX IF EXISTS user_sessions_user_idx;
DROP TABLE IF EXISTS user_sessions;
DROP INDEX IF EXISTS users_email_lower_uidx;
DROP TABLE IF EXISTS users;
