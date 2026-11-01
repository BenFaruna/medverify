# Phase 02 spike report: Greenbook and leaflet payloads

**Captured:** 2026-11-01, manually, at 2-second intervals, 15 requests total.
**Fixtures:** `testdata/greenbook/raw/` (query URLs in `queries.txt`).
**Source terms and permission:** see [../data-sources.md](../data-sources.md).

The purpose of the spike was to stop trusting the phase document's guessed field
names and replace them with what the live site actually returns. This report is
the input to the migration, parser, normalizer, and fetcher.

## 1. How the product list works

The Greenbook home page is a DataTables grid backed by a server-side endpoint on
the same URL. The browser sends the usual DataTables parameters; the response is
JSON with `draw`, `recordsTotal`, `recordsFiltered`, and `data`.

Two non-standard parameters matter:

- `search_ingredient=<text>` filters by active ingredient. It is applied to the
  row's ingredient, not as a column search.
- `columns[3][search][value]=<category id>` filters by product category.
- `columns[11][search][value]=<status>` filters by status (`Active`, `Inactive`).

Paging is `start`/`length`. `recordsTotal` was 8,966 on capture day. Category
filters do **not** change `recordsTotal`; they only change `recordsFiltered`.

**Decision:** the full sync pages the *unfiltered* list with `start += length`.
Category and status filters are a supplementary fetch used by the spike and by
future targeted refreshes, not the primary path. Filtering by category would
miss the 12 rows in categories the UI does not expose.

## 2. Fields that surprised us

1. **Manufacturer name is absent from the list JSON.** There is only
   `manufacturer_id`. The detail page (`/products/details/{id}`) renders the
   name and country server-side; the manufacturer directory
   (`/manufacturers?page=N`) renders 30-odd names per page with links to
   `/manufacturer/products/{id}`. Importing the directory is cheap (29 pages)
   and avoids 8,966 detail requests. Phase 02 stores `manufacturer_source_id`
   and leaves `manufacturer` empty until the directory join is enabled; the raw
   payload is always kept so no data is lost.
2. **Some products have no NRN.** One captured product has `NAFDAC=""`; another
   has the obvious placeholder `00-0000`; several names contain `(no NRN)`.
   The table therefore keys on `source_id` (Greenbook `product_id`) and treats
   `nrn` as nullable and unique-when-present.
3. **NRNs have stray whitespace and occasional trailing letters.** Examples:
   `' A11-0361'`, `'A11-0237 '`, `A7-2373L`, `A4-4479L`. `NormalizeNRN` trims,
   uppercases, unifies dashes, and removes internal whitespace.
4. **`form_id`/`route_id` change JSON type.** They arrived as both numbers and
   numeric strings. The parser ignores them; it reads the flattened
   `form_name`/`route_name` strings.
5. **Text is HTML-escaped.** `&#039;`, `&amp;`, `&quot;`, and `\r\n` appear in
   names, compositions, descriptions, and pack sizes. The parser decodes
   entities for the domain fields and keeps the original JSON in `raw`.
6. **Product names contain display markers.** `#`, `*`, `$`, `_`, `##`, and
   editorial fragments such as `(incomplete smpc, strength format)` are part of
   the published name. Store them unchanged; presentational cleanup belongs to
   the UI, not ingestion.
7. **Composition can be empty** (8 of 194 unique sampled rows). Never drop a
   row because composition is missing.
8. **Strength is free text and not always parseable.** `see Composition` (33 of
   194), `NA` (69), `100 g/sachet`, `10,000 IU/1 mL`, `0.05%`, `5 mg/5 mL`, and
   `150,000,000 IU` all appear. Unparseable values keep the raw string with
   null parsed fields.
9. **Ingredient and strength lists are positionally aligned but not guaranteed
   to match in length.** `ingredient_name` is semicolon-separated; `strength`
   is semicolon-separated. When counts match, pair by index. When they do not,
   pair as far as possible and leave the extra ingredients unpaired. Raw
   composition is authoritative for later phases.
10. **Only `Active` and `Inactive` statuses appeared.** `Expired` returned zero
    rows. Status is stored as free text; phase 05 decides how to treat unknown
    statuses.
11. **Leaflets are mostly scans.** All four sampled PDFs produced zero text
    characters through `pdftotext`. `scanned_needs_ocr` is the common path, not
    an edge case. Text-bearing fixtures for the splitter are therefore
    synthesised from the standard SmPC heading structure.
12. **The Disinfectants category returned no rows**, even though the UI offers
    it. Coverage gaps are expected and will be surfaced as a distinct
    "not in dataset" state in phase 05.

## 3. Schema changes forced by the spike

The phase document's data model was adjusted as follows (migration
`00002_registry.sql` is the source of truth):

| Change | Why |
| --- | --- |
| Add `source_id text NOT NULL UNIQUE` | NRN is not always present; Greenbook `product_id` is. Upserts use `source_id`. |
| Make `nrn text UNIQUE` nullable | Empty NRNs exist. Postgres allows multiple NULLs. |
| Add `manufacturer_source_id text` | Manufacturer name is not in the list payload. |
| Add `payload_hash text NOT NULL` | Skip unchanged rows without comparing every column. |
| `strength_value double precision` | Avoid sqlc `pgtype.Numeric` conversion noise; strengths are not money. |
| `approved_at`/`expires_at timestamptz` | Postgres `date` maps to `pgtype.Date`; timestamps keep the store layer simple. Values are midnight UTC. |
| `smpc_documents UNIQUE (product_id, source_url)` | One document row per product/URL; sha256 detects content changes. |
| `product_ingredients UNIQUE NULLS NOT DISTINCT` | Prevent duplicate unparsed ingredients where parsed strength is NULL. |

## 4. Fetcher design resulting from the spike

- Base URL and leaflet hosts are configuration. The two hosts observed are
  allowlisted by default; anything else is rejected before a socket is opened.
- The client sends an honest `User-Agent`, one request at a time by default,
  and a shared `golang.org/x/time/rate` limiter.
- Retry only network errors and 5xx (3 attempts, exponential backoff plus
  jitter). 4xx stops immediately.
- Responses are cached on disk by URL hash with a short TTL (1 hour) so a
  crashed sync can resume without re-fetching pages that have not changed.
- `GREENBOOK_ENABLED=false` is checked before every request and every retry.

## 5. Leaflet pipeline resulting from the spike

1. Validate the URL scheme is `https` and the host is allowlisted.
2. Reject a `Content-Length` above `GREENBOOK_MAX_PDF_BYTES`; read through a
   `LimitReader` as a second guard.
3. Stream to a temporary file while hashing; store under
   `leaflets/<source_id>/<sha256>.pdf`; skip if the product's latest document has
   the same hash.
4. Read the page count and extract text with `pdftotext` under a timeout.
5. If the extracted text is empty or near-empty, mark `scanned_needs_ocr` and
   stop. Do not attempt OCR here; phase 04 owns the vision path.
6. Split text into SmPC sections by numbered headings. If there are no
   headings, store one section with a null code.

## 6. Acceptance targets proposed to the project lead

These are the numbers phase 02's full sync should report before moving on:

| Measure | Target |
| --- | --- |
| Products ingested | at least 95% of `recordsTotal` on sync day |
| Ingredient parse rate (rows with >=1 parsed ingredient) | at least 90% of rows with non-empty composition |
| Unknown ingredient names | listed, with a plan to extend `synonyms.yaml` |
| Leaflets downloaded | 100% of products with an `smpc` URL, subject to size cap |
| Leaflets needing OCR | report the share; no target until phase 04 |
| Leaflets with no sections | under 5% of text-bearing leaflets |
| Sync duration | under 2 hours at 1 request/second |

The project lead has not yet signed these off; the tracker records this as an
open item.

## 7. Open questions

1. Written permission or data-sharing arrangement with NAFDAC (project lead).
2. Whether to import the manufacturer directory as part of phase 02 or defer the
   join to phase 05. Current implementation stores the ID and raw payload.
3. Minimum useful coverage: the Greenbook does not list every category or every
   historical product. Phase 05 must show "not in dataset" differently from
   "mismatch".
4. Whether placeholder NRNs such as `00-0000` should be treated as absent during
   verification. Recommendation: normalize but flag in phase 05.
