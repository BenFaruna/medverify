-- +goose Up
-- Watchlist and safety flags (phase 06).
--
-- Provenance is a database rule, not an application convention: an entry
-- cannot exist without a source document that carries an issuing body, a URL,
-- and an issue date. Flags copy the source they were based on so a later
-- deactivation or edit never rewrites history.

CREATE TABLE watchlist_sources (
  id bigserial PRIMARY KEY,
  issuing_body text NOT NULL CHECK (btrim(issuing_body) <> ''),
  title text NOT NULL CHECK (btrim(title) <> ''),
  source_url text NOT NULL CHECK (source_url ~* '^https?://[^[:space:]]+$'),
  issued_at date NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (source_url, issued_at)
);

CREATE TABLE watchlist_entries (
  id bigserial PRIMARY KEY,
  source_id bigint NOT NULL REFERENCES watchlist_sources (id) ON DELETE RESTRICT,
  kind text NOT NULL CHECK (kind IN ('alert', 'withdrawal', 'banned_ingredient', 'restricted_ingredient')),
  nrn text,
  product_name text,
  product_name_norm text,
  manufacturer text,
  manufacturer_norm text,
  ingredient_norm text,
  reason text NOT NULL CHECK (btrim(reason) <> ''),
  effective_until date,
  active boolean NOT NULL DEFAULT true,
  reviewed_by text NOT NULL CHECK (btrim(reviewed_by) <> ''),
  review_due_at date,
  created_at timestamptz NOT NULL DEFAULT now(),
  -- A product entry names an NRN or a product; an ingredient entry names an
  -- ingredient. Never both, and never neither.
  CONSTRAINT watchlist_entries_shape CHECK (
    (
      kind IN ('banned_ingredient', 'restricted_ingredient')
      AND ingredient_norm IS NOT NULL AND btrim(ingredient_norm) <> ''
      AND nrn IS NULL AND product_name IS NULL
    )
    OR
    (
      kind IN ('alert', 'withdrawal')
      AND ingredient_norm IS NULL
      AND (nrn IS NOT NULL OR (product_name IS NOT NULL AND btrim(product_name) <> ''))
    )
  )
);

CREATE INDEX watchlist_entries_nrn_idx
  ON watchlist_entries (nrn) WHERE nrn IS NOT NULL;
CREATE INDEX watchlist_entries_ingredient_idx
  ON watchlist_entries (ingredient_norm) WHERE ingredient_norm IS NOT NULL;
-- GiST rather than GIN: fuzzy name lookup asks for the nearest names, so a
-- KNN scan can stop after the candidate limit instead of scoring every row.
CREATE INDEX watchlist_entries_name_trgm
  ON watchlist_entries USING gist (product_name_norm gist_trgm_ops);
CREATE INDEX watchlist_entries_live_idx
  ON watchlist_entries (active, effective_until);
-- One row per reviewed entry in a source document. Re-running an import must
-- not duplicate a row, so the natural key is built from the comparison columns
-- (which are nullable depending on the kind).
CREATE UNIQUE INDEX watchlist_entries_source_key
  ON watchlist_entries (
    source_id, kind,
    coalesce(nrn, ''),
    coalesce(product_name_norm, ''),
    coalesce(manufacturer_norm, ''),
    coalesce(ingredient_norm, '')
  );

CREATE TABLE safety_flags (
  id bigserial PRIMARY KEY,
  scan_id uuid NOT NULL REFERENCES scans (id) ON DELETE CASCADE,
  flag_type text NOT NULL CHECK (flag_type IN (
    'watchlist_product',
    'watchlist_ingredient',
    'undeclared_ingredient',
    'missing_ingredient',
    'strength_deviation',
    'registration_problem'
  )),
  severity text NOT NULL CHECK (severity IN ('high', 'medium', 'low')),
  reason_code text NOT NULL CHECK (btrim(reason_code) <> ''),
  evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- The source as it read when the flag was raised, so later edits to the
  -- watchlist cannot change what a past result cited.
  source jsonb NOT NULL,
  watchlist_entry_id bigint REFERENCES watchlist_entries (id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX safety_flags_scan_idx ON safety_flags (scan_id);

-- +goose Down
DROP TABLE safety_flags;
DROP TABLE watchlist_entries;
DROP TABLE watchlist_sources;
