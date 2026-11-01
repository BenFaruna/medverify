# Data sources

This page records where MedVerify NG's reference data comes from, what we are
allowed to do with it, and how to contact the source. It is part of the
Definition of Done for phase 02.

## 1. NAFDAC Greenbook

| Item | Value |
| --- | --- |
| Public site | <https://greenbook.nafdac.gov.ng/> |
| Product list endpoint | `GET https://greenbook.nafdac.gov.ng/` with DataTables server-side query parameters |
| Leaflet (SmPC) host | `https://admin.greenbook.nafdac.gov.ng/` |
| `robots.txt` (checked 2026-11-01) | `User-agent: *` / `Disallow:` (no paths disallowed) |
| Terms of use | Not stated on the Greenbook pages we captured. Treat reuse as an open legal question. |

### Permission status

**Open, owned by the project lead.** No written data-sharing agreement with
NAFDAC has been recorded in this repository yet. This is a flagged risk, not a
silent assumption:

- We fetch only public pages, at a deliberately low rate (default 1 request per
  second), with an honest `User-Agent` that names the project.
- We do not bypass logins, CAPTCHAs, rate limits, or access controls.
- No Greenbook content is redistributed to end users beyond short factual
  fields and cited excerpts; full leaflets stay in our private object storage.
- Phase 12 cannot go live until the project lead records an agreement or an
  explicit decision to use the public data as-is.

If NAFDAC asks us to stop, set `GREENBOOK_ENABLED=false` and redeploy. The
fetcher refuses all outbound calls immediately; no code change is needed.

### Politeness rules

- One shared rate limiter, default 1 request/second (`GREENBOOK_RPS`).
- Retry only network errors and 5xx, with exponential backoff and jitter.
- Never retry 4xx.
- Nightly sync only, never in CI, never in unit tests (tests use `httptest`).
- Cached response bodies expire after `GREENBOOK_CACHE_TTL` (default 1 hour) so
  a retry or a repeated manual run does not hammer the site.
- Leaflet downloads are restricted to hosts in `GREENBOOK_ALLOWED_HOSTS`.

## 2. What the greenbook actually returns

The site is a Laravel application. The home page is a DataTables client that
queries the same URL with server-side parameters:

```
GET /?draw=1&start=0&length=25
    &search[value]=&search[regex]=false
    &search_ingredient=<active ingredient filter>
    &columns[3][search][value]=<product category id>
    &columns[11][search][value]=<status>
    &order[0][column]=0&order[0][dir]=asc
```

The JSON response is:

```json
{
  "draw": 1,
  "recordsTotal": 8966,
  "recordsFiltered": 7471,
  "data": [ { ... product row ... } ]
}
```

A product row has these fields (all observed in the captured fixtures):

| Field | Type / example | Notes |
| --- | --- | --- |
| `product_id` | number | Stable Greenbook primary key. Use as `source_id`. |
| `product_name` | string | Contains display artefacts (`#`, `*`, `$`, `_`, `##`) and editorial notes such as `(no NRN)`. Keep raw. |
| `NAFDAC` | string | The NRN, e.g. `03-1450`, `A11-0551`, `A7-2373L`. May be empty or a placeholder (`00-0000`). May have leading/trailing spaces. |
| `status` | string | `Active` or `Inactive` in the capture. Do not assume a closed set. |
| `product_category_id` / `category_name` | number / string | `1` Drugs, `2` Vaccines and Biologics, `5` Medical devices, `6` Veterinary, `7` Herbals and Nutraceuticals. Category `12` (Disinfectants) is offered by the UI but returned no rows on capture day. |
| `marketing_category_id` | number | Marketing class, not used yet. |
| `composition` | string | Free text, sometimes empty, contains HTML entities and `\r\n`. Always keep raw. |
| `ingredient_name` | string | Semicolon-separated active ingredients, may contain commas inside a name (`Copper, Nicotinamide`). |
| `strength` | string | Semicolon-separated strengths aligned with `ingredient_name` when counts match. Examples: `500 mg`, `120 mg/5 mL`, `0.05%`, `150,000,000 IU`, `see Composition`, `NA`. |
| `form_name`, `route_name` | string | `NA` when unknown. |
| `form_id`, `route_id` | number **or** numeric string | Do not decode into `int` directly. |
| `applicant_name` | string | Also nested under `applicant.name`. |
| `manufacturer_id` | number | Manufacturer name is **not** in the list payload; it is on the product detail page and in the manufacturer directory. |
| `approval_date`, `expiry_date` | `YYYY-MM-DD` | Dates, no time zone. |
| `smpc` | URL or empty | PDF on `admin.greenbook.nafdac.gov.ng`. Empty for many devices. |
| `atc` | string | Often empty. |
| `pack_size` | string | HTML-escaped, e.g. `1 x 60&#039;s (in HDPE bottle)`. |
| `product_description` | string | Free text, HTML-escaped. |
| `biosimilar`, `country_id`, `deleted_at` | null in capture | Do not rely on them. |

Counts and coverage from the capture on 2026-11-01:

| Slice | `recordsFiltered` |
| --- | --- |
| All products | 8,966 |
| Drugs | 7,471 |
| Medical devices | 1,113 |
| Herbals and Nutraceuticals | 125 |
| Vaccines and Biologics | 152 |
| Veterinary | 93 |
| Disinfectants | 0 |
| Active | 5,926 |
| Inactive | 3,040 |

The category counts sum to 8,954; 12 rows are in categories not exposed by the
home-page filter. A full sync must page the unfiltered list, not the category
lists.

### NRN formats found

Observed patterns after removing surrounding whitespace:

```
NN-NNNN       03-1450
NN-NNNNNN     04-0858
A N-NNNNNN    A11-0551
A N-NNNNN     A4-101970
A NN-NNNN     A11-0551
A N-NNNNA     A7-2373L
```

The common shape is `[A-Z]{0,2}[0-9]{1,3}-[0-9]{3,6}[A-Z]?`, with optional
stray whitespace. This regex lives in one place
(`registry.NRNPattern`) and is derived from these fixtures, not guessed.

### Leaflets

- PDF URLs are on `admin.greenbook.nafdac.gov.ng`, a different host from the
  main site, so both hosts are allowlisted by default.
- The sample leaflets captured on 2026-11-01 are scanned images: `pdftotext`
  produced zero characters from an 8-page, 7-page, and 26-page file. This is
  normal for the dataset and is why `scanned_needs_ocr` exists.
- Files can be several megabytes. The downloader enforces a byte cap before and
  while reading, and a page cap before extraction.

## 3. Fixtures

Raw responses are committed under `testdata/greenbook/raw/` with the exact
query URLs in `queries.txt`. They are public data captured once for parser
tests; CI never calls the live site. The scanned PDF sample is not committed
(too large); tests synthesise PDFs, and the real-file equivalent is recorded in
`docs/phases/MedVerify NG - 02-spike-report.md`.
