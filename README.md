# MedVerify NG

Photograph a medicine pack, read the printed details, check them against NAFDAC
Greenbook records and a curated watchlist, and return an explainable verdict.
Users can then ask questions about that pack and get answers drawn only from
NAFDAC data and leaflets, with citations.

The system never says a medicine is safe, genuine, or authentic. See
[docs/scoring-philosophy.md](docs/scoring-philosophy.md) and the product rules in
[the build tracker](docs/phases/MedVerify%20NG%20-%2000%20Build%20Tracker%20and%20Engineering%20Conventions.md).

For the problem statement, architecture, AI components, design decisions, and
known gaps, see the [functions overview](FUNCTIONS_OVERVIEW.md).

## Getting Started

MedVerify runs as four pieces: an HTTP API that also serves the web client, a
background worker, Postgres with pgvector, and S3-compatible object storage.
Docker Compose starts all of them with one command. The default configuration
uses deterministic fake model providers, so the stack works offline with no
accounts, API keys, or NAFDAC network access.

### What you need

| Dependency | Used for | Check |
| --- | --- | --- |
| Go 1.26 or newer | Building and running the binaries | `go version`; the version is pinned in `go.mod` |
| Docker Engine 24+ with Compose v2 | Postgres, object storage, and the services | `docker compose version` |
| `make` and `curl` | Starting the stack and checking readiness | `make --version` |
| Free ports 8080, 5432, and 9000 | API and web client, Postgres, S3 gateway | change `HTTP_PORT`, `POSTGRES_PORT`, or `STORAGE_PORT` in `.env` if they are taken |

Do not install Postgres, pgvector, or an S3 server yourself: the Compose stack
provides them. The web client is plain HTML, CSS, and JavaScript with no build
step, so Node.js is not required. A vision or assistant provider key is
optional; only real extraction and generated answers need one.

### 1. Clone and configure

```bash
git clone https://github.com/benfaruna/medverify.git
cd medverify
cp .env.example .env
```

The copied defaults are enough for local work. `.env` is git-ignored; never
commit it or put a real provider key in a tracked file. `API_LEGACY_KEY=dev-api-key`
is a development-only shared key the bundled web page sends; keys issued with
`admin apikey create` work at the same time. `VISION_PROVIDER=fake` returns a
deterministic reading instead of calling a model.

### 2. Start the stack

```bash
make run
```

This builds the images, applies migrations, creates the storage bucket, starts
the API and worker, and waits until `GET /readyz` answers. The first run pulls
images and compiles the binaries, so allow a few minutes; later runs are quick.
Follow service output with `make logs`.

### 3. Verify and open the app

```bash
curl localhost:8080/healthz
curl localhost:8080/readyz
```

Then open <http://localhost:8080> in a browser. The API serves the web client
from `web/` at the root; the page shrinks each photo in the browser, uploads
one batch, and renders the verdict with reasons, flags, sources, and follow-up
questions. Set `WEB_DIR=` to disable serving the page when it is hosted
elsewhere.

`/healthz` proves the process is alive. `/readyz` proves it can reach Postgres
and object storage, so only the second means the stack can take a scan.

Interactive API documentation is at <http://localhost:8080/docs> (Swagger UI),
and the raw OpenAPI contract is at <http://localhost:8080/openapi.yaml>. Both
are served by the API binary and need no extra tooling, but the endpoints still
require an API key. See the
[API guide](api/README.md#interactive-documentation) for how to authorize and
try a request.

### 4. Try a scan through the API

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

### 5. Create an account and manage your API key

Open <http://localhost:8080/register>. Creating an account also creates its
first API key and signs the browser in. The key is shown once, in that
response; only its SHA-256 hash and public prefix are stored, so it cannot be
displayed again.

A registered person *is* an API client: registration writes one `api_clients`
row, so the key it issues works on `/v1/scans` exactly like one from
`admin apikey create`, with the same scopes, rate limit, and daily quotas.

<http://localhost:8080/settings> lists the account's keys and rotates them.
Rotation revokes every active key on the account immediately and returns the
replacement once. Anything still sending the old key gets `401` until it is
updated.

| Endpoint | Purpose |
| --- | --- |
| `POST /v1/auth/register` | Create the account, its first key, and a session |
| `POST /v1/auth/login` | Start a session |
| `POST /v1/auth/logout` | Revoke the session and clear the cookie |
| `GET /v1/account` | The signed-in account |
| `GET /v1/account/api-keys` | Key metadata: prefix, scopes, state, timestamps |
| `POST /v1/account/api-keys/rotate` | Revoke active keys, issue one replacement |

The account routes use a session cookie, never `X-API-Key`. The cookie is
`HttpOnly`, `SameSite=Lax`, and `Secure` in production; cross-site writes are
rejected server-side. Sign-in is rate limited per address. `AUTH_SESSION_TTL`
(default `720h`) bounds how long a session lasts. Passwords are stored as
PBKDF2-HMAC-SHA256 derives with 600,000 iterations and a per-account salt.

The chat page at <http://localhost:8080/> holds no key at all. It calls `/v1`
with that cookie, and the API accepts either credential on every `/v1` route:
`X-API-Key` first when present, then the session. A browser session acts with
the plan on its client row, so the page and the account's key share one rate
limit and one set of daily counters. A signed-out browser is sent to `/login`
before the chat renders. The key exists for code the person runs themselves
(their own script, a CI job), and is never placed in a page, browser storage,
or a URL.

### What is running

| Service | Address | Role |
| --- | --- | --- |
| `api` | <http://localhost:8080> | Public API, `/healthz`, `/readyz`, `/metrics`, `/docs`, and the web client |
| `worker` | no port | River jobs: extraction, verification, verdict, indexing, retention |
| `postgres` | localhost:5432 | Primary database with the `pgvector` extension |
| `storage` | localhost:9000 | SeaweedFS S3 gateway for private image storage |
| `init` | exits after setup | One-shot migrations and bucket creation |

### Run the binaries on the host instead

```bash
docker compose up -d postgres storage
make migrate-up
make storage-init
go run ./cmd/api      # in one terminal
go run ./cmd/worker   # in another
```

`.env` already points at the published ports (localhost:5432 and
localhost:9000), so the host processes and the containers agree.

### Common commands

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

`make tools` installs the CLI tools CI pins: goose, sqlc, golangci-lint, and
govulncheck.

### Troubleshooting

| Symptom | Fix |
| --- | --- |
| `make run` ends with "stack did not become ready" | Run `make logs`. Usually a port is already in use, Docker is not running, or the `init` container failed a migration. |
| `401 unauthorized` from the API | For programmatic calls, send `X-API-Key: dev-api-key` (the development-only `API_LEGACY_KEY`), or a key issued with `admin apikey create` or at registration. In a browser, the session has expired: sign in again. |
| The chat page bounces to `/login` | That is the gate working. Sign in; the `next` parameter returns you to the page you asked for. |
| The `api` container exits mentioning `serve web client` | `WEB_DIR` points at a missing directory. Keep the default (the image ships `/app/web`) or set `WEB_DIR=` to serve the API only. |
| `connection refused` on 5432 or 9000 while running the binaries | Start the containers first: `docker compose up -d postgres storage`. |
| Integration tests cannot start containers on rootless Docker | `TESTCONTAINERS_RYUK_DISABLED=true make test-integration`. Test containers are still removed by their cleanup hooks. |
| You want a clean database and bucket | `make clean` deletes the volumes; the next `make run` migrates from scratch. It is destructive, so use it only for local data. |

Target: a healthy stack from a clean clone in under 15 minutes, including image
pulls.

## Current state

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

## Model providers

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

## Operations

Health checks, kill switches, retention and erasure commands, API-key rotation,
webhook replay, and the incident process are in [ops/runbook.md](ops/runbook.md).
What data is stored and for how long is in
[docs/data-inventory.md](docs/data-inventory.md); known attack paths are in
[docs/threat-model.md](docs/threat-model.md).

`GET /metrics` serves Prometheus text with aggregate request counters and
durations labelled by route pattern.

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
api/         OpenAPI description, contract tests, and embedded Swagger UI
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
