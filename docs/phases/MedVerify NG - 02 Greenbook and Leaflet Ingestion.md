# 02 Greenbook and Leaflet Ingestion

**Goal:** a local, searchable, regularly refreshed copy of NAFDAC registered products, their ingredients, and their leaflet text, split into sections.

**Depends on:** 01. **Blocks:** 05, 06, 08. **Read first:** 00 Build Tracker, 01 Foundations.

**Status:** implementation complete; the one-off live acceptance run and the
written NAFDAC permission are still open. See the spike report and
[data sources](../data-sources.md#permission-status).

## 1. Deliverables checklist

- [x] Spike report: captured sample payloads and a written description of the real field names and formats
- [x] Greenbook fetcher with throttling, retries, caching, and a kill switch
- [x] Parser from raw payload to domain structs
- [x] NormalizeNRN, NormalizeIngredient, ParseStrength functions with a synonym table
- [x] Tables: products, product\_ingredients, sync\_runs, smpc\_documents, smpc\_sections
- [x] Idempotent upsert by source\_id (NRN where present), with the raw payload kept as jsonb
- [x] Nightly River job plus a manual admin command to run a sync
- [x] Anomaly guard that aborts a sync when the result looks wrong
- [x] Leaflet downloader, text extractor, and section splitter
- [x] Lookup queries: by NRN, by trigram name search, by ingredient
- [x] Ingestion report command: counts, failures, leaflets needing OCR

## 2. Tools needed

| Tool | Purpose |
| --- | --- |
| net/http plus golang.org/x/time/rate | Polite fetching |
| poppler-utils (pdftotext) installed in the worker image | Reliable PDF text extraction |
| PostgreSQL pg\_trgm | Fuzzy name search |
| River | Scheduled and retried jobs |
| httptest | Fake Greenbook server in tests |
| rapid | Property tests for normalizers |

## 3. Setup required for success

1. **Permission first.** Record in the repo (docs/data-sources.md) what agreement exists with NAFDAC. Until a written arrangement exists, scraping is a risk to flag, not hide. Respect the site terms and robots rules, identify the client honestly in the User-Agent, and keep the request rate low.
2. **Do the spike before designing the parser.** Spend the first days capturing 20 to 30 real raw responses (different categories, statuses, long compositions) into testdata/greenbook/. Write down which fields exist, their formats, and the pagination mechanism. Do not trust this document or any third-party description for field names. Update the schema below to match reality.
3. **Allowlist the leaflet host.** Only download PDFs from hosts you have explicitly allowlisted in config.
4. Provide a worker image that contains pdftotext.
5. Create config for: base URL, requests per second, max concurrency, max PDF size, allowlisted hosts, sync schedule, kill switch.

## 4. Data model (adjusted after the spike)

The spike changed the model: NRN is not always present, so `source_id`
(Greenbook `product_id`) is the key and `nrn` is nullable. See the
[spike report](MedVerify%20NG%20-%2002-spike-report.md#3-schema-changes-forced-by-the-spike)
for the full list of changes.

```sql
CREATE TABLE products (
  id bigserial PRIMARY KEY,
  source_id text NOT NULL UNIQUE,          -- Greenbook product_id
  nrn text UNIQUE,                          -- nullable: some products have no NRN
  name text NOT NULL,
  category text,
  status text NOT NULL,
  applicant text,
  manufacturer text,
  manufacturer_source_id text,
  composition_raw text,
  form text, route text, strength_raw text, pack_size text,
  atc_code text,
  approved_at timestamptz, expires_at timestamptz,
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
  strength_unit text, per_unit text, strength_raw text,
  UNIQUE NULLS NOT DISTINCT (product_id, name_norm, strength_value, strength_unit)
);
CREATE INDEX ON product_ingredients (name_norm);

CREATE TABLE sync_runs (
  id bigserial PRIMARY KEY, started_at timestamptz, finished_at timestamptz,
  status text, rows_seen int, rows_added int, rows_changed int, rows_failed int,
  error text
);

CREATE TABLE smpc_documents (
  id bigserial PRIMARY KEY, product_id bigint REFERENCES products(id),
  source_url text NOT NULL, sha256 text, storage_key text,
  fetched_at timestamptz, extraction_method text,
  status text NOT NULL,  -- ok | scanned_needs_ocr | failed | too_large
  error text,
  UNIQUE (product_id, source_url)
);

CREATE TABLE smpc_sections (
  id bigserial PRIMARY KEY, document_id bigint NOT NULL REFERENCES smpc_documents(id) ON DELETE CASCADE,
  section_code text, heading text, text text NOT NULL, page_start int
);
```

## 5. Design notes

**Fetcher.** Single client with a rate limiter, per-request timeout, retry with exponential backoff and jitter on 5xx and network errors, no retry on 4xx. A config kill switch stops all outbound calls immediately.

**Normalization (the most bug-prone part).**

- NormalizeNRN: trim, uppercase, unify dash characters, remove stray spaces. Idempotent. The NRN regex must be derived from real data found in the spike, kept in one place.
- NormalizeIngredient: lowercase, remove salt and hydrate suffixes into a separate field, apply a synonym table kept in a YAML file (for example paracetamol and acetaminophen map to one name). Unknown names pass through unchanged and are counted in the report so the table can grow.
- ParseStrength: value, unit (mg, mcg, g, ml, IU, percent), and per-unit expressions such as per 5 ml. Unparseable strings are stored raw with null parsed fields, never dropped.

**Upsert.** Compare a hash of the raw payload to skip unchanged rows. Changed rows replace their ingredient rows inside one transaction.

**Anomaly guard.** Abort and mark the run failed if the row count drops by more than a configured percentage versus the last successful run, or if more than a configured share of rows fail to parse. A human can override with an admin flag.

**Leaflet pipeline.** Download to object storage, compute sha256, skip if unchanged. Extract text with pdftotext. If the text layer is empty or near empty, set status scanned\_needs\_ocr and stop (OCR is handled later through the vision provider). Split into sections by numbered headings; if no headings are found, store a single section with a null code.

## 6. Tests for this phase

| Test | Level | What it proves |
| --- | --- | --- |
| Parser golden tests: each testdata payload parses to an expected JSON file | unit | Field mapping |
| Parser: unknown fields ignored, missing optional fields yield nulls | unit | Robustness |
| NormalizeNRN table tests plus property test: normalize(normalize(x)) equals normalize(x) | unit | Idempotency |
| NormalizeIngredient: 50 or more real compositions including salts, synonyms, multi-ingredient | unit | Quality |
| ParseStrength: mg, mcg, per 5 ml, IU, percent, garbage input | unit | Parsing |
| Fetcher against httptest server: obeys rate limit, retries 503, stops on 404, honors kill switch | unit | Politeness |
| Fetcher rejects a leaflet URL on a non-allowlisted host | unit | SSRF guard |
| Upsert twice with the same data changes nothing and rows\_changed is 0 | integration | Idempotency |
| Upsert with one changed field updates exactly one product and its ingredients | integration | Change detection |
| Anomaly guard aborts when the fake server returns 30 percent fewer rows | integration | Safety |
| Trigram search ranks the right product first for typos and word order changes | integration | Search quality |
| Section splitter on 3 fixture PDFs: well structured, odd headings, no headings | unit | Splitting |
| Scanned PDF fixture sets status scanned\_needs\_ocr | unit | Fallback |
| Oversized PDF is rejected without being fully read into memory | unit | Resource safety |

**Acceptance test.** Run a full sync against the real site once (manually, off CI). Report: products ingested, ingredient parse rate, leaflets downloaded, leaflets needing OCR, leaflets with no sections. Agree minimum targets with the project lead before moving on.

## 7. Pitfalls and guards

- **Site changes break everything quietly.** The anomaly guard and alerting on failed syncs are not optional.
- **Do not hammer the source.** Cache, throttle, and sync at night.
- **Dataset coverage.** The Greenbook may not cover every product category. Keep that knowledge in docs and surface it later as a distinct not-found-in-dataset state.
- **Composition text is free text.** Expect lists, nested brackets, per-unit strengths, and typos. Keep raw text always.
- **Encoding and punctuation.** Normalize Unicode (NFKC) and unusual dashes before comparing.
- **Duplicate or reissued NRNs.** Treat NRN as the key but log conflicts where the same NRN arrives with materially different names.
- **PDF risks.** Cap file size and page count, run extraction with a timeout, and never execute or render embedded content.
- **Memory.** Stream downloads to disk or object storage instead of loading large PDFs fully into memory.
- **Scope creep.** Do not build the embedding or chunking logic here. That is phase 08.

## 8. Definition of Done

- A nightly sync runs unattended and reports to sync\_runs.
- Lookup by NRN under 50 ms, trigram search under 200 ms on the full dataset.
- The ingestion report exists and has been reviewed with the project lead.
- Checklist ticked, tracker updated.

## 9. Notes for agents

- Start with the spike and commit the fixtures before writing the parser.
- Never hardcode field names from this document without confirming them in the fixtures.
- Do not run the live fetcher in tests or CI. All network access goes through the fake server.

## 10. Implementation notes

- Products are keyed by `source_id`; `nrn` is nullable and unique when present.
- Nightly sync: the worker registers the `greenbook_sync` periodic job when
  `GREENBOOK_ENABLED=true` and `GREENBOOK_SYNC_INTERVAL` is positive. A manual
  run is `medverify-admin sync greenbook [--override-anomaly]`.
- Leaflets are processed separately with
  `medverify-admin sync leaflets [--limit N]`, which downloads to object
  storage, records `ok`, `scanned_needs_ocr`, `too_large`, or `failed`, and
  splits extracted text into sections.
- The report is `medverify-admin registry report`.
- CI uses `httptest` and fixtures only; it never calls the live Greenbook or a
  model provider.
- Still open: a one-off full sync against the live site, reviewed with the
  project lead (products ingested, ingredient parse rate, leaflets downloaded,
  leaflets needing OCR, leaflets with no sections), and the written NAFDAC
  permission recorded in `docs/data-sources.md`.
