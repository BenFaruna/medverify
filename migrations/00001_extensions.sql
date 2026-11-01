-- +goose Up
-- pg_trgm: fuzzy product name matching against the Greenbook (phase 02, 05).
-- vector:  leaflet chunk embeddings (phase 08).
-- pgcrypto: gen_random_uuid for primary keys (phase 02 onwards).
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- +goose Down
DROP EXTENSION IF EXISTS pgcrypto;
DROP EXTENSION IF EXISTS vector;
DROP EXTENSION IF EXISTS pg_trgm;
