-- +goose Up
-- Greenbook registry tables (phase 02). The shape is driven by the spike in
-- docs/phases/MedVerify NG - 02-spike-report.md: NRN is nullable, source_id is
-- the upsert key, and every row keeps its raw payload.

CREATE TABLE products (
  id bigserial PRIMARY KEY,
  source_id text NOT NULL UNIQUE,
  nrn text UNIQUE,
  name text NOT NULL,
  category text,
  status text NOT NULL,
  applicant text,
  manufacturer text,
  manufacturer_source_id text,
  composition_raw text,
  form text,
  route text,
  strength_raw text,
  pack_size text,
  atc_code text,
  approved_at timestamptz,
  expires_at timestamptz,
  smpc_url text,
  raw jsonb NOT NULL,
  payload_hash text NOT NULL,
  fetched_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX products_name_trgm ON products USING gin (name gin_trgm_ops);

CREATE TABLE product_ingredients (
  id bigserial PRIMARY KEY,
  product_id bigint NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  name_raw text NOT NULL,
  name_norm text NOT NULL,
  strength_value double precision,
  strength_unit text,
  per_unit text,
  strength_raw text,
  UNIQUE NULLS NOT DISTINCT (product_id, name_norm, strength_value, strength_unit)
);

CREATE INDEX product_ingredients_name_norm_idx ON product_ingredients (name_norm);

CREATE TABLE sync_runs (
  id bigserial PRIMARY KEY,
  started_at timestamptz NOT NULL,
  finished_at timestamptz,
  status text NOT NULL,
  rows_seen integer NOT NULL DEFAULT 0,
  rows_added integer NOT NULL DEFAULT 0,
  rows_changed integer NOT NULL DEFAULT 0,
  rows_failed integer NOT NULL DEFAULT 0,
  error text
);

CREATE INDEX sync_runs_status_finished_idx ON sync_runs (status, finished_at DESC);

CREATE TABLE smpc_documents (
  id bigserial PRIMARY KEY,
  product_id bigint NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  source_url text NOT NULL,
  sha256 text,
  storage_key text,
  fetched_at timestamptz,
  extraction_method text,
  status text NOT NULL, -- ok | scanned_needs_ocr | failed | too_large
  error text,
  UNIQUE (product_id, source_url)
);

CREATE TABLE smpc_sections (
  id bigserial PRIMARY KEY,
  document_id bigint NOT NULL REFERENCES smpc_documents(id) ON DELETE CASCADE,
  section_code text,
  heading text,
  text text NOT NULL,
  page_start integer
);

CREATE INDEX smpc_sections_document_idx ON smpc_sections (document_id);

-- +goose Down
DROP TABLE IF EXISTS smpc_sections;
DROP TABLE IF EXISTS smpc_documents;
DROP TABLE IF EXISTS sync_runs;
DROP TABLE IF EXISTS product_ingredients;
DROP TABLE IF EXISTS products;
