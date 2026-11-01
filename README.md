# MedVerify NG

Photograph a medicine pack, read the printed details, check them against NAFDAC
Greenbook records and a curated watchlist, and return an explainable verdict.
Users can then ask questions about that pack and get answers drawn only from
NAFDAC data and leaflets, with citations.

The system never says a medicine is safe, genuine, or authentic. See
[docs/scoring-philosophy.md](docs/scoring-philosophy.md) and the product rules in
[the build tracker](docs/phases/MedVerify%20NG%20-%2000%20Build%20Tracker%20and%20Engineering%20Conventions.md).

Current state: **phase 1 (foundations)**. The skeleton, health checks,
migrations, job wiring, and CI exist; no domain logic does yet.

## Quickstart

Requirements: Docker with Compose, and Go (see `go.mod` for the pinned version).

```bash
cp .env.example .env
make run          # builds images, migrates, waits for /readyz
curl localhost:8080/healthz
curl localhost:8080/readyz
```

Target: a healthy stack from a clean clone in under 15 minutes, including image
pulls.

Other useful targets:

```bash
make test              # unit tests, race detector on
make test-integration  # testcontainers: pgvector Postgres and S3 storage per package
make lint              # golangci-lint
make migrate-status    # against the database in .env
make job-noop          # enqueue a no-op job; the worker logs it
make logs              # follow service logs
make down              # stop; `make clean` also deletes volumes
```

Run the binaries on the host instead of in Compose with `go run ./cmd/api` and
`go run ./cmd/worker`; `.env` already points at the published ports.

## Layout

```
cmd/api      HTTP server
cmd/worker   River background workers
cmd/admin    migrations, storage bootstrap, manual job enqueue
cmd/eval     evaluation runner (phase 10)
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

Packages without behaviour in phase 1 hold only a `doc.go` that names the phase
that fills them in.

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
| `contract` | `go test -tags=contract ./...` | every PR; recorded provider responses (added in phase 4) |
| `eval` | `make eval` | nightly and before release (phase 10) |

CI never calls a real vision or language model provider.

On a rootless Docker daemon, testcontainers' reaper cannot bind its random
port. Run integration tests with `TESTCONTAINERS_RYUK_DISABLED=true make
test-integration`; test containers are still terminated by their cleanup
hooks.
