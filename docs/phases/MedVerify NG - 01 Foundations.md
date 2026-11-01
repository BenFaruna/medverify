# 01 Foundations

**Goal:** a repository where any developer or agent can clone, run everything with one command, and get a green health check and green CI.

**Depends on:** nothing. **Blocks:** every other phase. **Read first:** 00 Build Tracker and Engineering Conventions.

## 1. Deliverables checklist

- [x] Go module initialised, layout from document 00 created with placeholder packages
- [x] docker-compose.yml: Postgres (pgvector image), S3-compatible storage, api service
- [x] Makefile targets: run, test, test-integration, lint, migrate-up, migrate-down, sqlc, eval
- [x] Typed config loader (environment variables only) with validation at startup
- [x] Structured logging with request IDs
- [x] HTTP server skeleton: chi router, recovery, request ID, timeout middleware, graceful shutdown
- [x] Endpoints: GET /healthz (process alive), GET /readyz (DB and storage reachable)
- [x] goose migrations wired; first migration enables extensions pg\_trgm, vector, pgcrypto
- [x] sqlc configured and generating code
- [x] River installed, worker binary starts and processes a no-op job
- [x] CI pipeline: lint, vet, unit tests, integration tests, govulncheck, build
- [x] openapi.yaml skeleton with health endpoints and the error model
- [x] Short documents in /docs: scoring philosophy, data handling rules, glossary

**Note on object storage.** MinIO images are no longer publicly pullable (quay
returns 401; the Docker Hub repositories were removed). The stack uses a
SeaweedFS S3 gateway instead. `internal/storage` speaks S3 through the MinIO Go
client, so this is an image and endpoint change only.

## 2. Tools needed

| Tool | Purpose |
| --- | --- |
| Go (current stable) | Language |
| Docker and Docker Compose | Local Postgres and S3-compatible storage |
| goose CLI | Migrations |
| sqlc CLI | Generated query code |
| golangci-lint | Linting |
| govulncheck | Vulnerability scanning |
| make | Task runner |
| testcontainers-go | Throwaway Postgres in tests |

## 3. Setup required for success

1. Use the image pgvector/pgvector with the Postgres 16 tag so the vector extension is available. Confirm with SELECT extname FROM pg\_extension.
2. Create a .env.example with every variable and a comment for each. The real .env is git-ignored.
3. Decide and document the module path and Go version once. Pin Go in go.mod and in CI.
4. Decide the error response format now. Use RFC 7807 problem details: type, title, status, detail, instance, plus a stable machine code field.
5. Create a CODEOWNERS file and a pull request template that includes the Definition of Done from document 00.

## 4. Design notes

**Config.** One struct, populated once in main, passed down explicitly. No package reads os.Getenv outside the config package. Fail fast with a clear message listing every missing or invalid variable, not just the first.

**Process modes.** cmd/api serves HTTP. cmd/worker runs River workers. They share the same internal packages and database. cmd/admin holds one-off commands.

**Graceful shutdown.** On SIGTERM stop accepting requests, wait for in-flight requests up to a timeout, then close the DB pool. Workers finish or release their current job.

**Logging.** slog with a JSON handler. Every request logs method, path, status, duration, request ID. A redaction helper must exist from day one so later phases cannot log images, API keys, or raw user text by accident.

**Migrations.** One migration per change, always with a down section. Never edit a migration after it is merged.

**Database access.** All SQL lives in .sql files under each package and is compiled by sqlc. No string-built SQL.

## 5. Tests for this phase

| Test | Level | What it proves |
| --- | --- | --- |
| Config: valid env parses into the struct | unit | Happy path |
| Config: missing and malformed values return one error listing all problems | unit | Fail-fast behavior |
| Request ID middleware sets and propagates an ID | unit | Traceability |
| Recovery middleware turns a panic into a 500 problem response, process stays up | unit | Resilience |
| Error writer produces valid RFC 7807 JSON with content type application/problem+json | unit | Error model |
| /healthz returns 200 with no dependencies | unit | Liveness |
| /readyz returns 503 when Postgres is down and 200 when up | integration | Readiness |
| Migrations: up then down then up on an empty database | integration | Reversibility |
| Extensions pg\_trgm, vector, pgcrypto exist after migrate-up | integration | Setup correctness |
| River no-op job enqueued by API is executed by worker | integration | Queue wiring |
| Graceful shutdown completes an in-flight slow request | integration | Shutdown behavior |
| Redaction helper masks configured keys and long base64-like strings | unit | Log safety |

**Acceptance test.** On a clean machine: git clone, cp .env.example .env, make run, curl /readyz returns 200. Time it. Target: under 15 minutes including image pulls.

## 6. Pitfalls and guards

- **Shared test database.** Tests that share one Postgres leak state. Use testcontainers with a fresh schema per test package, or a transaction rolled back per test.
- **Extensions missing in CI.** If CI uses a plain Postgres image, the vector extension is absent and migrations fail. Use the same image locally and in CI.
- **Config sprawl.** Reading env vars all over the code makes tests hard. Enforce with a lint rule or a review check.
- **Skipping down migrations.** They will be needed during pilot rollbacks. Test them.
- **Secrets in the repo.** Add a secret scanner (for example gitleaks) to CI now.
- **Unbounded request bodies.** Set a global max body size in middleware. Later phases tighten it per route.
- **Time.** Inject a clock interface instead of calling time.Now directly in business logic. Later phases need deterministic tests for expiry and rate limits.
- **Over-engineering.** Do not add features, metrics, or abstractions that no later phase needs yet.

## 7. Definition of Done for this phase

- A new developer reaches a healthy local stack from a clean clone in under 15 minutes.
- CI is green on main and blocks merges when red.
- All checklist items ticked and the tracker in document 00 updated.

## 8. Notes for agents

- Build the skeleton exactly to the layout in document 00. Do not invent new top-level directories.
- Do not implement domain logic in this phase. Placeholder packages contain only a doc.go file.
- Keep every middleware independently testable with net/http/httptest.
