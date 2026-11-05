# MedVerify NG — Functions Overview

This document explains what MedVerify NG is for, what it does, how the backend
is put together, where AI is used, and the decisions and gaps behind the code.
For setup commands, environment variables, and day-to-day operations, see the
[README](README.md), the [API guide](api/README.md), and the
[runbook](ops/runbook.md).

> Current state: the codebase implements the scan pipeline, registry ingestion,
> safety checks, verdict engine, leaflet retrieval, grounded assistant, evals,
> integrator API, web client, and hardening work described in phases 1–12.
> It is **not** pilot-ready: pharmacist/legal review, a NAFDAC data-sharing
> decision, live-provider acceptance, labelled evaluation sets, and operational
> drills are still open. See [docs/phases](docs/phases) for the status of each
> phase.

---

## 1. The problem

A medicine buyer in Nigeria can hold a pack and still have no practical way to
answer basic questions:

- Is the registration number on this pack present in NAFDAC's public Greenbook?
- Does the printed name, manufacturer, strength, and ingredient list agree with
  the registration record?
- Has the product, brand, or an ingredient appeared in a NAFDAC alert, recall,
  withdrawal, or a curated watchlist?
- What does the official leaflet actually say about indications, warnings, or
  storage?

The data exists, but it is spread across a web register, PDFs, and regulator
notices. Checking it manually requires knowing what to look for, reading a
dense table, and interpreting regulatory language. A general-purpose chatbot
makes the situation worse if it answers from memory: it may invent a
registration, reassure a user that a pack is "safe", or offer dosing advice.

MedVerify NG narrows the problem to one verifiable workflow: **photograph a
pack, read what is printed on it, compare it with records we actually hold, and
show the evidence behind the result** — then answer follow-up questions only
from that evidence.

## 2. The proposed solution

MedVerify NG is an API-first medicine-pack verification service with a small
static web client.

1. The user uploads one to four photos of a pack.
2. A vision model reads the printed fields: NRN, product name, manufacturer,
   batch number, manufacturing date, expiry date, and ingredients with strengths.
3. Deterministic code validates and merges the model output across images.
4. A matcher compares the label with a local copy of the NAFDAC Greenbook.
5. A safety checker compares the product and its ingredients with a curated,
   source-backed watchlist and the registered composition.
6. A pure verdict engine turns the recorded signals into one explainable grade.
7. The user can ask questions about that pack. A grounded assistant retrieves
   only that product's registry record, stored flags, and leaflet passages, then
   answers with citations — or returns a fixed refusal/fallback.

The service deliberately never says a medicine is **safe, genuine, or
authentic**, and never says a pack is **fake**. "Not found in NAFDAC records"
means our copy has no match; it is a statement about our data, not a conclusion
about the pack. These rules are in
[docs/scoring-philosophy.md](docs/scoring-philosophy.md).

### Key functionalities

| Capability | What it does | Main code |
| --- | --- | --- |
| Pack upload | Validates JPEG/PNG/WebP, rejects decompression bombs, re-encodes to strip EXIF/GPS, stores privately | `internal/scan/image.go`, `internal/scan/http.go` |
| Idempotent scans | Replays the same upload safely; same key with a different body is rejected | `internal/scan/service.go`, `internal/scan/store.go` |
| Label extraction | Schema-constrained vision call per image, multi-image merge, deterministic validation | `internal/vision/`, `internal/scan/service.go` |
| Greenbook ingestion | Polite paged sync, anomaly guard, duplicate-NRN handling, leaflet download | `internal/registry/` |
| Registry verification | NRN, name, manufacturer, ingredients, strength, expiry, registration status | `internal/verify/` |
| Watchlist and safety | Product/ingredient alerts, undeclared/missing ingredients, strength deviation, registration problems | `internal/safety/` |
| Verdict | Six graded outcomes, stable reason codes, confidence band, sort-only concern score | `internal/verdict/` |
| Leaflet retrieval | Section-aware chunking, embeddings, pgvector + full-text hybrid search, product-scoped SQL | `internal/rag/` |
| Grounded Q&A | Intent routing, policy gate, read-only tools, bounded tool loop, citation verifier, fixed templates | `internal/assistant/` |
| Integrator API | Hashed API keys, scopes, per-key/IP rate limits, daily quotas, tenant isolation | `internal/platform/auth/`, `cmd/api/` |
| API documentation | Embedded Swagger UI at `/docs` and raw spec at `/openapi.yaml` | `api/docs.go`, `api/openapi.yaml`, `api/swaggerui/` |
| Webhooks | HMAC-signed delivery, SSRF guard, retries, dead-lettering, replay | `internal/webhook/` |
| Retention and erasure | Scheduled image/conversation deletion, manual erasure | `internal/retention/`, `cmd/admin/` |
| Evaluation harness | Extraction, retrieval, assistant, verdict, and judge-calibration runners with release gates | `internal/eval/`, `cmd/eval/`, `evals/` |
| Operations | Health/readiness probes, metrics, kill switches, migrations, admin CLI | `cmd/admin/`, `ops/runbook.md` |

---

## 3. How the backend works

### 3.1 Runtime shape

```text
                         ┌─────────────────────────────┐
   browser / integrator  │  Static web client (web/)   │
        │                └──────────────┬──────────────┘
        │ HTTPS                         │
        ▼                               ▼
   ┌─────────────────────────────────────────────┐
   │ cmd/api — chi router, auth, quotas, scans,  │
   │ assistant endpoints, /healthz, /readyz,     │
   │ /metrics                                    │
   └───────────────┬───────────────┬─────────────┘
                   │               │
                   │ SQL           │ S3
                   ▼               ▼
            ┌────────────┐   ┌──────────────┐
            │ PostgreSQL │   │ Object store │
            │ + pgvector │   │ (SeaweedFS / │
            │            │   │  any S3)     │
            └─────┬──────┘   └──────────────┘
                  │ River jobs
                  ▼
   ┌─────────────────────────────────────────────┐
   │ cmd/worker — scan jobs, webhook delivery,   │
   │ Greenbook sync, leaflet indexing, retention │
   └─────────────────────────────────────────────┘
```

There are four binaries:

- `cmd/api` — HTTP surface.
- `cmd/worker` — River background jobs.
- `cmd/admin` — migrations, storage bootstrap, key management, manual syncs,
  reports, webhook replay, retention/erasure.
- `cmd/eval` — offline evaluation runner.

### 3.2 Scan request lifecycle

1. **Middleware.** Request ID, panic recovery, access logging, timeout, body
   limit, CORS, and metrics run first. Feature kill switches answer before
   authentication. Address-level rate limiting runs before the API key lookup.
2. **Authentication.** `X-API-Key` is parsed as `mv_<prefix>_<secret>`. The
   server looks the key up by prefix and compares a SHA-256 hash in constant
   time. Scope and daily-quota middleware follow.
3. **Upload validation.** Each image is size- and pixel-checked, its format is
   sniffed from bytes (never the filename), EXIF orientation is applied, and it
   is re-encoded as a metadata-free JPEG. Only decoded pixel data survives.
4. **Tenant and idempotency handling.** The scan row records the owning client
   key. An `Idempotency-Key` plus request hash makes retries safe; a reused key
   with a different body returns `422`.
5. **Storage.** Images go to private object storage under random keys; filenames
   are never used. The database stores dimensions, content type, byte size, and
   SHA-256.
6. **Queueing.** A River job is inserted with a bounded attempt count. The API
   waits briefly for a terminal result: `201` if it finishes in time, `202`
   with a `Location` header otherwise.
7. **Worker claim.** The worker marks the scan `processing` with a guarded
   update. A duplicate job is a no-op; a failed scan can be requeued.
8. **Extraction.** Each stored image is loaded, resized to a 1568 px long edge,
   and sent to the configured vision provider. Provider calls are recorded in
   `llm_calls` with latency, tokens, cost, model, prompt version, and outcome —
   never prompts or image bytes.
9. **Merge and validate.** Per-image labels are merged; the highest-confidence
   value wins, but two different values with similar confidence become a
   conflict and stay unreadable. Validators reject malformed NRNs and dates
   rather than repairing them.
10. **Verification.** The matcher compares the label with the local registry.
11. **Safety.** The checker produces sourced flags from the watchlist and the
    registered composition.
12. **Verdict.** A pure function maps the signals and versioned rules to a
    verdict, confidence, ordered reasons, and a sort-only score.
13. **Finish and notify.** The result is written once; an already-finished scan
    is not overwritten. Completion can emit a webhook, but a webhook failure
    never loses a result or fails a scan.

### 3.3 Public API

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/scans` | Upload 1–4 images and optional location/language |
| `GET` | `/v1/scans/{id}` | Current state and, when terminal, the result |
| `POST` | `/v1/scans/{id}/questions` | Ask one grounded question about the scan |
| `GET` | `/v1/scans/{id}/conversation` | Read the stored transcript |
| `GET` | `/healthz` | Process liveness; never checks dependencies |
| `GET` | `/readyz` | Postgres and object storage readiness |
| `GET` | `/metrics` | Prometheus text, labelled by route pattern |
| `GET` | `/docs` | Interactive Swagger UI, embedded in the API binary |
| `GET` | `/openapi.yaml` | Raw OpenAPI 3.1 contract |

A scan owned by another client is reported as `404`, exactly like a missing
scan, so IDs cannot be used to probe for other tenants. Errors are RFC 7807
problem documents with a stable `code`; clients branch on the code, never on
the human-readable detail.

The OpenAPI document and Swagger UI are embedded into the API binary. `/docs`
serves a same-origin Swagger UI with **Authorize** and **Try it out**, while
`/openapi.yaml` serves the raw contract that CI validates. The UI assets are
vendored under `api/swaggerui/`, so the docs work without a CDN, Node build, or
network access. The docs routes are public, but every `/v1` operation still
enforces its API key and scope.

### 3.4 Data model and persistence

- **PostgreSQL + pgvector** is the source of truth. Migrations are goose SQL
  embedded in the binary, reversible, and tested down as well as up.
- **sqlc** compiles `.sql` files into typed Go; no SQL is built by string
  concatenation.
- **River** uses Postgres as the job queue, so a scan, its job, and its result
  share one transactional store.
- **Object storage** holds scan images and leaflet PDFs. SeaweedFS provides an
  S3-compatible store locally; the client also works with S3 services.
- Key tables include `api_clients`, `api_keys`, `scans`, `scan_images`,
  `scan_results`, `safety_flags`, `products`, `product_ingredients`,
  `leaflet_documents`, `leaflet_sections`, `rag_chunks`, `conversations`,
  `messages`, `assistant_tool_calls`, `llm_calls`, `watchlist_entries`,
  `webhook_deliveries`, and `sync_runs`.

### 3.5 Greenbook ingestion

The fetcher is the only client for NAFDAC Greenbook traffic. It enforces a
shared rate limit (default one request/second), caches list pages, retries only
network errors and 5xx with exponential backoff and jitter, and refuses
non-allowlisted leaflet hosts or redirects. A full sync pages the unfiltered
list, parses rows defensively, and runs an anomaly guard before writing: if the
new dataset drops too many rows or has too many parse failures, the sync aborts
and leaves the previous dataset untouched. Duplicate NRNs are logged and
skipped rather than aborting the whole run. Leaflet PDFs are downloaded to
object storage and their text layer is extracted; scanned PDFs are recorded as
needing OCR instead of pretending they were read.

### 3.6 Security, privacy, and reliability

- Images are re-encoded to remove EXIF/GPS; raw coordinates are reduced to a
  coarse grid cell and never stored or logged.
- API keys are stored as SHA-256 hashes and shown once at creation. Logs record
  the client name and key prefix, never the secret.
- Prompts, questions, leaflet passages, and answers are not written to the cost
  ledger or logs. The assistant transcript is stored for review, with a TTL.
- Webhooks are HMAC-SHA256 signed over `"<timestamp>.<body>"`, hashed compare,
  and guarded against SSRF at save and connect time. Delivery retries back off
  to a dead-letter state that an operator can replay.
- Daily request/scan/question quotas are database counters. If the counter
  cannot be written, the request is refused rather than allowed, so an outage
  cannot become unbounded spend.
- Vision calls have a timeout, bounded retries, and a circuit breaker. A
  fallback model is tried only when the primary is unavailable — never when an
  image is simply unreadable.
- Retention deletes ordinary scan images after the configured window, keeps
  flagged cases longer, and removes conversations at expiry. Erasure deletes
  objects first, then the scan row, so a failed delete leaves a retryable state.
- Rollback for a feature is a deploy: kill switches are read at startup, not
  flipped at runtime.

---

## 4. AI components and how they are implemented

AI is used for **perception and language**, not for the final decision. The
verification, safety, and verdict layers are deterministic code so the same
evidence and rules always produce the same result.

### 4.1 Vision extraction

**Where:** `internal/vision/`, wired in `internal/scan/service.go`.

- **Providers:** a deterministic `fake` for development and CI, Anthropic
  `claude` (Messages API), and an OpenAI-compatible `openai` client that works
  with OpenAI, Gemini's compatibility endpoint, Azure OpenAI, OpenRouter,
  Ollama, or vLLM. Provider and model IDs are pinned; a floating alias is not
  acceptable for review.
- **Schema-constrained output:** the model returns a fixed JSON shape with a
  value, confidence, source image, and unreadable-field list. Nothing is trusted
  until validators run.
- **Per-image, not per-scan:** each image is extracted separately, then merged.
  The highest-confidence value wins; a near-tie between different values becomes
  a recorded conflict and the field is left unreadable.
- **Deterministic validation:** NRN format, date parsing (month-only dates never
  invent a day), ingredient normalization, strength parsing, confidence clamping,
  and required-field checks. Invalid values are dropped into `unreadable`, not
  guessed or auto-corrected.
- **Confusable NRN candidates:** the validator can generate variants that differ
  by one or two common OCR confusions (`O`/`0`, `I`/`1`, `S`/`5`, etc.). The
  matcher may use those candidates to find a *candidate-only* or *ambiguous*
  record; the original read value is never silently replaced.
- **Resilience:** timeout, retries, circuit breaker, optional fallback model.
  `ErrUnreadable` is a normal outcome and is never retried on the fallback.

**Deliberate limit:** the model does not decide whether a pack is genuine. It
only proposes text; code decides what can be used.

### 4.2 Leaflet retrieval (RAG)

**Where:** `internal/rag/`, `internal/registry/leaflet_*`, `internal/platform/jobs/rag.go`.

- Leaflet text is split into sections, then into bounded, overlapping chunks
  (target ~400 tokens, max 500, 50-token overlap). Chunks never merge two
  sections. The embedding text includes product name and NRN as context; the
  stored display text does not.
- Chunk hashes include the embedding model, so changing the model forces a
  re-embed instead of reusing vectors from a different space. Indexing is
  idempotent and transactional per document.
- The default embedder is a deterministic **fake lexical embedder** for local
  work and CI. It hashes words and word pairs; it is not semantic and must not
  build a production index. The HTTP embedder targets any OpenAI-compatible
  `/embeddings` endpoint and validates dimension and batch shape on every call.
- Search combines pgvector cosine similarity and PostgreSQL full-text rank with
  reciprocal-rank fusion (k = 60). **All retrieval is filtered by product ID
  inside SQL**, so a query cannot widen to another medicine. Cross-product and
  cross-model leaks are covered by integration tests.
- Coverage and chunk inspection are available through `admin rag
  coverage|chunks`; indexing can run on a schedule via
  `EMBEDDINGS_INDEX_INTERVAL`.

**Open decision:** the production embedding provider and dimension are not
chosen. The column is fixed at 1024 dimensions, and no labelled retrieval set
exists yet, so recall has not been measured.

### 4.3 Assistant: routing and policy

**Where:** `internal/assistant/router.go`, `policy.go`, `templates.go`.

- A **router** model sees only the user's question, never registry or leaflet
  content. It returns a closed-set intent and a language. The question is
  delimited as data, and the model is forced to answer through a tool schema.
- Malformed output, an unknown intent, or an unsupported language resolves to
  the safest policy row. The model cannot invent a new category.
- The **policy table** maps every intent to exactly one action:
  - `answer` for registration, ingredients, safety flags, leaflet, and
    indications (with an allowed tool list),
  - `refuse` for personal safety, dosing, diagnosis, comparison, and prompt
    injection,
  - `redirect` for off-topic,
  - `fallback` for unknown.
- Refusals, redirects, and fallbacks come from reviewed templates embedded in
  the binary. They are validated at startup; production startup fails until the
  template file is marked reviewed. Today only English exists and the file is
  marked unreviewed, so this remains a blocker.

### 4.4 Assistant: tools and orchestration

**Where:** `internal/assistant/tools.go`, `orchestrator.go`, `service.go`.

- Four read-only tools: `get_registration`, `get_ingredients`,
  `get_safety_flags`, and `search_leaflet`.
- **Scope is server-side.** The tool session carries the scan ID, product ID,
  stored match, and stored flags. Any `scan_id`, `product_id`, or NRN supplied
  by the model is ignored. This is what blocks cross-pack answers even if the
  model invents an identifier.
- The orchestrator enforces limits in code, not in the prompt: 4 steps,
  4096 output tokens, 8 tool calls. `submit_answer` is the only terminal action.
  Plain text, malformed JSON, an exhausted limit, or a missing submission
  returns a fixed fallback rather than a partial answer.
- Absence of evidence is reported as `found: false` — never as a clean bill of
  health. An unindexed leaflet is "no leaflet", not "no warning".
- Conversations are capped at 6 user turns and expire after 24 hours by
  default. Provider outages return `503` with a retry hint; a spent turn budget
  returns `429`; an expired conversation returns `410`.

### 4.5 Assistant: verification

**Where:** `internal/assistant/verifier.go`.

Every draft answer goes through two passes:

1. **Code rules:** every claim must have at least one citation ID from this
   turn; every number in a claim must appear in a cited source; and forbidden
   reassurance/authenticity wording ("safe", "genuine", "authentic", "fake",
   etc.) is rejected outside quoted leaflet text.
2. **Support check:** a second small-model pass must mark every claim supported
   by the cited text.

Any failure returns the fixed unsupported fallback. There is no repair attempt,
because a repaired answer is still an unverified answer.

### 4.6 Evaluation harness

**Where:** `internal/eval/`, `cmd/eval/`, `evals/`.

The harness can run assistant, extraction, retrieval, verdict, and
judge-calibration evaluations. Datasets are versioned (`dataset.json` +
`cases.jsonl`), split overlap is rejected, reports are deterministic JSON and
Markdown, and gate files are strict YAML: a missing metric fails the gate rather
than passing silently. A failing gate exits non-zero after writing the report.

The harness is real; the evidence is not yet. Fixtures and fake providers keep
CI working, but they are **not** quality, recall, safety, or judge-calibration
evidence. There is no consented 100+ pack-photo set, no labelled retrieval set,
no real multilingual red-team corpus, no approved judge, and no regulator/lab
verdict ground truth.

---

## 5. Technologies and tools

| Area | Choice | Why |
| --- | --- | --- |
| Language | Go 1.26 | Single static binaries, strong concurrency, small runtime image |
| HTTP | `go-chi/chi` | Lightweight router, standard middleware model |
| Database | PostgreSQL 16 + `pgvector` | Relational evidence, JSON features, and vector search in one store |
| DB access | `pgx` + `sqlc` | Typed queries generated from SQL; no string-built SQL |
| Migrations | `goose` (embedded SQL) | Reversible, testable, shipped inside the binary |
| Jobs | `River` on Postgres | Transactional queue; no extra broker to operate |
| Object storage | S3 API; SeaweedFS locally | Private images and leaflets; portable to a managed S3 |
| Vision / LLM | Anthropic Messages, OpenAI-compatible chat, deterministic fakes | Provider choice is independent; CI never calls a live model |
| Embeddings | Deterministic fake; any OpenAI-compatible endpoint | Production provider is still an open decision |
| Image handling | `disintegration/imaging`, `golang.org/x/image` | EXIF orientation, resize, re-encode, WebP decode |
| Validation | `kin-openapi` contract tests, JSON Schema tool calls | Machine-checkable API and model contracts |
| API docs | Embedded Swagger UI 5.33.0 (`api/swaggerui/`), `go:embed` | Interactive, same-origin docs with no CDN or Node build |
| Testing | `testing`, `testify`, `rapid`, `testcontainers` | Unit, property, and real Postgres/S3 integration tests |
| Observability | `log/slog` JSON logs, Prometheus text metrics, `/healthz`, `/readyz` | Simple, dependency-light operations |
| Configuration | Environment variables parsed once | Every config problem reported at startup |
| Packaging | Multi-stage Dockerfile, Docker Compose | Reproducible local stack; static runtime image |
| Web client | Static HTML/CSS/JS, no build step | Minimal surface, easy to host and audit |
| CI | gofmt/goimports, vet, race tests, integration tests, govulncheck, sqlc drift, gitleaks | Catch correctness, security, and generated-code drift |

---

## 6. Important technical decisions

1. **Grading, not a probability.** The verdict is one of six labels
   (`consistent_with_records`, `mismatch_found`, `not_found_in_records`,
   `flagged`, `needs_review`, `unreadable`). A 0–100 score exists only to sort
   results inside a band; it is never shown as a probability. This avoids the
   false impression that the system can certify authenticity.
2. **Deterministic core, AI at the edges.** Verification, safety, and verdict
   logic are pure functions over recorded signals. The LLM reads images and
   writes language; it does not decide the outcome. This makes results
   reproducible, auditable, and replayable.
3. **Fail closed, never guess.** Low confidence becomes `unreadable`, not a
   guess. Missing evidence becomes "not found in records", not "fake". A
   provider outage fails the scan or the question; it never produces a partial
   answer or a stale-index answer.
4. **Version everything that changes a result.** Verdict rules, safety
   thresholds, prompt versions, embedding model, and dataset hashes are stored
   with the result. `admin verdict replay` can re-score stored scans under a
   candidate policy without writing anything.
5. **Tenant isolation is enforced in code and SQL.** A scan belongs to one
   client key. Cross-tenant reads return `404`. Retrieval is filtered by product
   ID in the query, not by a post-filter in Go.
6. **Scope comes from the server, never the model.** Assistant tools read the
   scan/product from the session. Model-supplied IDs are ignored. This is a
   security boundary, not a prompt convention.
7. **Fakes are first-class.** `fake` vision, assistant, and embedding providers
   make CI deterministic and free. Real providers are selected independently
   and must be pinned.
8. **Postgres is enough until it is not.** River, pgvector, quotas, idempotency,
   and audit rows all live in one database. That reduces operational surface
   for a small pilot; it also concentrates load and is a scaling limit to watch.
9. **Privacy by construction.** EXIF/GPS is stripped before storage, raw
   coordinates are reduced to a coarse cell, API keys are hashed, and prompts,
   answers, and images never enter logs or the cost ledger.
10. **Every slow or external action is a job.** Extraction, webhook delivery,
    Greenbook sync, leaflet indexing, and retention run in the worker. The API
    stays responsive and jobs are retryable and idempotent.

---

## 7. Challenges and gaps

### Data and extraction

- Pack photos are noisy: glare, angles, small print, multiple languages, and
  handwriting. Multi-image merge helps, but conflicts correctly degrade to
  unreadable rather than improving coverage.
- Greenbook data is imperfect: duplicate NRNs, placeholder numbers, missing
  manufacturer names in the list payload, inconsistent ingredient separators,
  and free-text strengths. The matcher is deliberately conservative, which
  means some real products land in `candidate_only` or `ambiguous`.
- Batch numbers are extracted but not verified against the registry, because
  the registry has no batch-level data.
- No consented 100+ pack-photo set exists, so extraction quality has not been
  measured.

### Grounding and medical safety

- General-purpose LLMs can invent registrations, reassure users, or relay
  dosing advice. The policy table, server-side tool scope, bounded loop, number
  check, wording check, and second-pass verifier are layered defences — but the
  templates and policy still need pharmacist/legal sign-off.
- Prompt injection is treated as an intent and refused, and tool results are
  delimited as data, but there is no held-out 150+ adversarial corpus yet.
- The dosing-relay decision is open. The safe default is to refuse and link to
  the leaflet; that default is not a signed-off product decision.

### Retrieval and evaluation

- The embedding provider and dimension are undecided, and the fake embedder is
  lexical, not semantic. Retrieval quality (recall@K) is unmeasured.
- The eval harness exists but the datasets, judge, and gate thresholds are
  proposed. Fixture numbers must never be cited as quality evidence.
- There is no lab- or regulator-confirmed verdict ground truth, so the verdict
  confusion table has only been exercised on synthetic signals.

### Legal, regulatory, and operations

- No written NAFDAC data-sharing agreement is recorded. The project fetches
  only public pages, politely, and can stop instantly with
  `GREENBOOK_ENABLED=false`, but the permission question is still owned by the
  project lead.
- Data-protection review and provider data-processing terms are not complete,
  including cross-border transfer of images and questions.
- No independent penetration test, backup/restore drill, load test, or
  production-size migration rollback has been run.
- Alerts, dashboards, and a spending cap are not in place. If the retention
  sweep stops, nothing alerts. Webhook signing secrets are stored in plaintext.
  There is no circuit breaker on the embedding provider.
- Live provider acceptance needs keys, pinned models, a budget, and
  data-handling approval. CI correctly uses fakes and fixtures only.

---

## 8. Possible improvements

### Near term

- Complete pharmacist/legal review of verdict, flag, refusal, and disclaimer
  wording; mark templates reviewed only after sign-off.
- Build the consented extraction set, labelled retrieval set, and multilingual
  red-team corpus; run the eval gates and publish real numbers.
- Decide the production embedding provider and dimension; re-index and measure
  recall@K.
- Run live provider acceptance with pinned models and a hard spending cap.
- Add alerting for worker failure, retention failure, provider error rate,
  queue depth, and spend.
- Encrypt webhook signing secrets at rest and move secrets to a manager.

### Medium term

- Add a circuit breaker and caching around embeddings; degrade the question
  panel with a clear "retrieval unavailable" state instead of a generic error.
- Improve matching with better manufacturer data (detail page/directory),
  richer NRN families, and explicit handling of batch/date edge cases.
- Add dashboards and service-level objectives; run load, soak, abuse, backup,
  restore, and migration-rollback drills.
- Add browser end-to-end, accessibility, and throttled-network tests for the
  web client.
- Expand templates and review for Pidgin, Hausa, Yoruba, and Igbo.

### Longer term

- Add a reviewed product/alert timeline so users can see when a flag was
  issued, superseded, or withdrawn.
- Explore pharmacy/pharmacist escalation workflows for `needs_review` cases.
- Consider a human-in-the-loop queue for ambiguous matches and watchlist
  changes.
- If volume grows, split read paths or move retrieval to a dedicated service
  while keeping Postgres as the evidence store.

---

## 9. Where to look next

- [README](README.md) — quickstart, providers, targets, layout, conventions.
- [API guide](api/README.md) and [OpenAPI contract](api/openapi.yaml).
- [Scoring philosophy](docs/scoring-philosophy.md) — allowed verdict language.
- [Scoring model](docs/scoring.md) — verdict meanings, weights, and scenario table.
- [Data sources](docs/data-sources.md) — Greenbook access and politeness rules.
- [Watchlist process](docs/watchlist-process.md) — curation and provenance.
- [Data inventory](docs/data-inventory.md) and [threat model](docs/threat-model.md).
- [Operations runbook](ops/runbook.md) — health, kill switches, erasure, keys,
  webhooks, deploys.
- [Phase documents](docs/phases) — deliverables, implementation notes, and open
  blockers for each phase.
