# MedVerify NG

Photograph a medicine pack, read the printed details, check them against NAFDAC
Greenbook records and a curated watchlist, and return an explainable verdict.
Users can then ask questions about that pack and get answers drawn only from
NAFDAC data and leaflets, with citations.

The system never says a medicine is safe, genuine, or authentic. See
[docs/scoring-philosophy.md](docs/scoring-philosophy.md) and the product rules in
[the build tracker](docs/phases/MedVerify%20NG%20-%2000%20Build%20Tracker%20and%20Engineering%20Conventions.md).

Current state: **phase 8 (leaflet indexing and retrieval), in review**. Uploads are
validated, re-encoded to strip EXIF and GPS, stored privately, processed by a
River job, and exposed through POST/GET /v1/scans. Each image goes through a
configurable vision provider; the default is a deterministic fake, and
production can use a pinned Claude model. Model output is schema-constrained and
then passed through deterministic validators and multi-image merge, so missing
or ambiguous fields are reported as unreadable rather than guessed. Provider
calls are recorded in `llm_calls` with cost and latency only.

After extraction the worker compares the label with the local Greenbook copy
and stores a typed, explainable result in `scan_results.features`: one of
`matched_consistent`, `matched_with_mismatch`, `candidate_only`, `ambiguous`,
`not_found_in_dataset`, or `unreadable_input`, with per-field outcomes, stable
reason codes, candidates, and dataset coverage. `not_found_in_dataset` means
our copy has no match, never that a pack is fake.

Phase 6 adds a curated watchlist with database-enforced provenance: an entry
cannot exist without a source document (issuing body, URL, issue date), and
`reviewed_by` is required. `admin watchlist import` loads a reviewed CSV
atomically and idempotently; `list` and `deactivate` manage the rest. The worker
runs a safety checker after verification and stores typed flags with a source
snapshot: watchlist product or ingredient matches, undeclared or missing
ingredients (only when the label's ingredient list was read with enough
confidence), strength deviation, and expired or withdrawn registrations. Flag
wording comes from a reviewed catalog keyed by reason code, not from generated
text; the catalog is still marked draft pending a pharmacist. Matching rules and
thresholds live in `config/safety-v1.yaml` (`SAFETY_CONFIG_FILE`). Nothing on
the watchlist is added automatically: only the admin import writes entries, and
the process is documented in
[docs/watchlist-process.md](docs/watchlist-process.md).

Phase 7 turns that evidence into one deterministic verdict stored in typed
columns on `scan_results`: `consistent_with_records`, `mismatch_found`,
`not_found_in_records`, `flagged`, `needs_review`, or `unreadable`. The engine is
a pure function of the recorded signals and a versioned YAML policy
(`config/verdict-v1.yaml`, `VERDICT_CONFIG_FILE`); it uses no LLM, no clock, and
no database. A 0-100 score exists for sorting but is never a probability and is
never displayed as one. Reasons are stable codes rendered from a reviewed
catalog, still marked draft pending a pharmacist. Every result stores the
signals and rules version that produced it, and
`admin verdict replay --rules <file>` re-scores stored scans under a candidate
policy without writing anything. The verdict meanings, the 50-case scenario
table, and the weights are a proposed policy awaiting sign-off; see
[docs/scoring.md](docs/scoring.md).

Phase 8 chunks each leaflet section, embeds the chunks, and retrieves passages
for one product only, combining vector and full-text rank. Chunks store the
embedding model and document hash, so indexing is idempotent and re-embeds only
changed documents. The embeddings provider and vector dimension are still an
open decision: the default is a deterministic fake for local work, and the HTTP
client targets any OpenAI-compatible endpoint. `admin rag index|chunks|coverage|eval`
manage the corpus, and the worker registers an opt-in scheduled pass
(`EMBEDDINGS_INDEX_INTERVAL`). Retrieval scope is enforced by product in SQL and
tested for cross-product and cross-model leaks. There is no labelled retrieval
eval set yet, so recall has not been measured. See the
[phase 8 notes](docs/phases/MedVerify%20NG%20-%2008%20Leaflet%20Indexing%20(RAG).md#11-blockers-and-open-items).

The phase 2 live-ingestion acceptance run, the consented 100+ photo extraction
eval set, the live provider acceptance run, the data-handling decision, and the
written NAFDAC data-sharing agreement are still open; see the
[phase 2 notes](docs/phases/MedVerify%20NG%20-%2002%20Greenbook%20and%20Leaflet%20Ingestion.md#10-implementation-notes),
the [phase 3 notes](docs/phases/MedVerify%20NG%20-%2003%20Scan%20Pipeline%20Skeleton.md#9-implementation-notes),
the [phase 4 notes](docs/phases/MedVerify%20NG%20-%2004%20Label%20Extraction.md#9-implementation-notes),
the [phase 5 notes](docs/phases/MedVerify%20NG%20-%2005%20Registry%20Verification.md#11-blockers-and-open-items),
the [phase 6 notes](docs/phases/MedVerify%20NG%20-%2006%20Ingredient%20Safety%20Checks.md#11-blockers-and-open-items),
the [phase 7 notes](docs/phases/MedVerify%20NG%20-%2007%20Verdict%20Engine.md#11-blockers-and-open-items),
and the [phase 8 notes](docs/phases/MedVerify%20NG%20-%2008%20Leaflet%20Indexing%20(RAG).md#11-blockers-and-open-items).

## Quickstart

Requirements: Docker with Compose, and Go (see `go.mod` for the pinned version).

```bash
cp .env.example .env
make run          # builds images, migrates, waits for /readyz
curl localhost:8080/healthz
curl localhost:8080/readyz
```

Upload a pack photo (the default vision provider is a deterministic fake; set
`VISION_PROVIDER=claude` or `openai` with a key and pinned model for a real
extraction):

```bash
curl -i -X POST localhost:8080/v1/scans \
  -H "X-API-Key: dev-api-key" \
  -H "Idempotency-Key: demo-1" \
  -F "images=@testdata/images/normal.jpg" \
  -F "latitude=6.45" -F "longitude=3.39" -F "language=en"
# 201 with status=completed, or 202 with a Location header; then:
curl -H "X-API-Key: dev-api-key" localhost:8080/v1/scans/<id>
```

A result never says a medicine is safe, genuine, or authentic. `unreadable`
means the photo could not be read confidently, not that the product is fake.

### Model providers

Vision and assistant providers are selected independently:

| Value | Wire format | Typical endpoints |
| --- | --- | --- |
| `fake` | deterministic local stub | development and CI only; rejected in production |
| `claude` | Anthropic Messages API | `https://api.anthropic.com` |
| `openai` | OpenAI chat completions | OpenAI, Gemini compatibility API, Azure OpenAI, OpenRouter, Ollama, vLLM |

`*_BASE_URL` is the API root including the version path, so the `openai`
provider works with any compatible server by pointing it at, for example,
`https://generativelanguage.googleapis.com/v1beta/openai` or
`http://localhost:11434/v1`. An empty value selects the provider default. Model
identifiers must be pinned: a floating alias is accepted by the provider but not
by review. A `VISION_FALLBACK_MODEL` of the same provider is tried only when the
primary is unavailable, never when an image is unreadable.

Target: a healthy stack from a clean clone in under 15 minutes, including image
pulls.

Other useful targets:

```bash
make test              # unit tests, race detector on
make test-integration  # testcontainers: pgvector Postgres and S3 storage per package
make lint              # golangci-lint
make migrate-status    # against the database in .env
make job-noop          # enqueue a no-op job; the worker logs it
make sync-greenbook    # run a full Greenbook product sync now
make sync-leaflets     # download and extract pending leaflets
make registry-report   # counts, OCR needs, unknown ingredients, recent runs
make logs              # follow service logs
make down              # stop; `make clean` also deletes volumes
```

Run the binaries on the host instead of in Compose with `go run ./cmd/api` and
`go run ./cmd/worker`; `.env` already points at the published ports.

## Layout

```
cmd/api      HTTP server
cmd/worker   River background workers
cmd/admin    migrations, storage bootstrap, manual job enqueue, syncs, report
cmd/eval     extraction eval runner (extended with assistant evals in phase 10)
internal/
  platform/  config, logging, http helpers, db, jobs, clock, auth, ratelimit
  storage/   S3-compatible object storage (SeaweedFS locally)
  scan/ vision/ registry/ safety/ verdict/ rag/ assistant/ llm/
migrations/  goose migrations, embedded into the binary
api/         OpenAPI description
docs/        product rules, conventions, and phase documents
testdata/    fixtures and golden files (added with the first parser)
evals/       versioned eval datasets (phase 10)
```

Packages not yet in use hold only a `doc.go` that names the phase that fills
them in.

## Conventions

- Configuration is environment variables only, parsed once by
  `internal/platform/config`, which reports every problem at once.
- SQL lives in `.sql` files next to its package and is compiled by sqlc
  (`make sqlc`). No string-built SQL.
- Migrations are goose SQL embedded in the binary; every migration is
  reversible and the down path is tested.
- Errors are RFC 7807 problem details with a stable `code` (see
  `internal/platform/httpx`).
- Logs are JSON via `log/slog`. Configured secrets are registered with the
  log redactor at startup. See [docs/data-handling.md](docs/data-handling.md).
- Time is read through `clock.Clock`, never `time.Now` in business logic.

## Tooling

`make tools` installs goose, sqlc, golangci-lint, and govulncheck at the pinned
versions. CI runs gofmt/goimports checks, vet (including `integration`-tagged
files), `go test -race`, integration tests, `govulncheck`, a sqlc drift check,
and gitleaks.

## Testing levels

| Tag | Command | Runs when |
| --- | --- | --- |
| none | `go test ./...` | every commit; no network, no DB |
| `integration` | `go test -tags=integration ./...` | every PR; real Postgres and S3 storage via testcontainers |
| `contract` | `go test -tags=contract ./...` | every PR; recorded provider responses (phase 4) |
| `eval` | `make eval` | nightly and before release (phase 10) |

CI never calls a real vision or language model provider.

On a rootless Docker daemon, testcontainers' reaper cannot bind its random
port. Run integration tests with `TESTCONTAINERS_RYUK_DISABLED=true make
test-integration`; test containers are still terminated by their cleanup
hooks.
