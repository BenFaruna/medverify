-- +goose Up
-- Grounded assistant transcripts (phase 09). Every row is tied to one scan, so
-- no conversation can reach another pack's records. The transcript is kept for
-- the review window; tool arguments and results are summarized in the audit
-- table rather than duplicated here.
CREATE TABLE conversations (
  id uuid PRIMARY KEY,
  scan_id uuid NOT NULL UNIQUE REFERENCES scans(id) ON DELETE CASCADE,
  language text NOT NULL DEFAULT 'en',
  rules_version text NOT NULL,
  prompt_version text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_turn_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);

CREATE INDEX conversations_expires_idx ON conversations (expires_at);

CREATE TABLE messages (
  id bigserial PRIMARY KEY,
  conversation_id uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  scan_id uuid NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
  turn integer NOT NULL CHECK (turn > 0),
  role text NOT NULL CHECK (role IN ('user', 'assistant')),
  content text NOT NULL,
  intent text,
  language text,
  tool_names text[] NOT NULL DEFAULT '{}',
  source_ids text[] NOT NULL DEFAULT '{}',
  verifier text,
  refusal_reason text,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (conversation_id, turn, role)
);

CREATE INDEX messages_conversation_idx ON messages (conversation_id, turn, id);

-- One row per tool invocation, for audit and cost review. Leaflet passages are
-- not copied here; the message row names the source IDs and the chunk text
-- remains in the RAG store.
CREATE TABLE assistant_tool_calls (
  id bigserial PRIMARY KEY,
  conversation_id uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  scan_id uuid NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
  turn integer NOT NULL CHECK (turn > 0),
  tool_name text NOT NULL,
  arguments jsonb NOT NULL DEFAULT '{}',
  outcome text NOT NULL CHECK (outcome IN ('ok', 'error', 'rejected')),
  source_ids text[] NOT NULL DEFAULT '{}',
  latency_ms integer NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX assistant_tool_calls_conversation_idx
  ON assistant_tool_calls (conversation_id, turn);

-- +goose Down
DROP TABLE IF EXISTS assistant_tool_calls;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS conversations;
