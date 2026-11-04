# 11 Integrator API and Web MVP

**Goal:** turn the internal pipeline into a public, versioned, secure API that third parties can integrate with, and ship a thin web client that uses only that public API.

**Depends on:** 07, 09. **Blocks:** 12. **Read first:** 00 Build Tracker, 03 Scan Pipeline Skeleton, 07 Verdict Engine, 09 Assistant.

## 1. Deliverables checklist

**API platform**

- [x] Tables: api\_clients, api\_keys, usage\_counters, webhook\_endpoints, webhook\_deliveries
- [x] API key issuance: random key shown once, stored only as a hash, prefix kept for identification (admin CLI)
- [x] Scopes (for example scans:write, scans:read, questions:write) and per-client quotas
- [x] Rate limiting per key and per IP
- [x] Webhooks: scan.completed, with HMAC signature, timestamp, retries, dead-letter state
- [x] Final OpenAPI 3 document, validated in CI, with examples
- [x] Standard error model, pagination rules, versioning policy under /v1, deprecation policy
- [x] Sandbox mode with fixed responses and test keys
- [x] Developer docs: quickstart, auth, errors, webhooks, example in curl, Go, and JavaScript

**Web MVP**

- [x] Mobile-first web client calling only the public API
- [x] Capture or upload up to 4 photos with guidance (front, back, side with ingredients)
- [x] Client-side downscale before upload
- [x] Progress, result card (verdict, reasons, flags, sources), retake flow
- [x] Question panel with suggested questions and citations
- [x] Persistent disclaimer: this tool compares packs to NAFDAC records and is not medical advice
- [x] Language selector, starting with English and Pidgin (Pidgin falls back to the reviewed English wording)
- [ ] Accessibility and low-bandwidth support — markup, live regions, keyboard flow, and retry-safe uploads are implemented; axe, keyboard, and throttled-network runs are blocked on missing tooling and budget (see section 9)

## 2. Tools needed

| Tool | Purpose |
| --- | --- |
| kin-openapi (or similar) | Validate requests and responses against the spec in tests |
| golang.org/x/time/rate or a Redis token bucket | Rate limiting |
| crypto/hmac, crypto/subtle | Webhook signing, constant-time compares |
| A frontend stack the team knows (a small TypeScript SPA or server-rendered pages) | Web client |
| Playwright | End-to-end browser tests |
| axe-core | Accessibility checks |
| k6 or vegeta | Load tests |

## 3. Setup required for success

1. Decide key format and storage: a prefix (for lookup and display) plus a long random secret. Store a SHA-256 hash of the secret. Show the full key once at creation.
2. Decide quotas and rate limits per plan (requests per minute, scans per day), stored in the database, not code.
3. Decide CORS policy: the web client origin is allowlisted. No wildcard for authenticated routes.
4. Prepare the sandbox: fixed responses for known test images, so integrators can build without cost.
5. Choose the web stack and keep it deliberately small. The web client is a consumer of the public API and must not call internal packages or private endpoints.
6. Prepare copy review: result wording comes from the reasons catalog (phase 07) and the policy templates (phase 09).

## 4. Design notes

**Authentication.** Authorization: Bearer with the API key. Look up by prefix, compare the hash in constant time, then load scopes and quotas. Failed attempts are rate limited and logged without the key.

**Rate limiting and quotas.** Token bucket per key for bursts, daily counters for quotas. Responses include limit headers and Retry-After on 429.

**Webhooks.** Payload signed with a per-endpoint secret: HMAC-SHA256 over timestamp plus body, sent in headers. Receivers reject old timestamps. Delivery retried with backoff for a bounded period, then marked dead with a way to replay. Webhook URLs must be HTTPS and must not resolve to private or internal addresses.

**Result schema.** The public result contains: scan ID, status, verdict, confidence band, reasons (code, localized text, parameters), flags with sources, extracted fields with per-field confidence, registry reference (NRN, link to the Greenbook entry), and disclaimer text. It never contains a numeric score or the word safe.

**Versioning.** Additive changes only within /v1. Breaking changes need /v2 and a deprecation window.

**Web client.** Use a plain file input with the camera capture attribute so phones open the camera. Resize to a modest long edge and compress before upload to suit poor connections. Retry uploads with the same Idempotency-Key. Poll with backoff when the server returns 202. Keep the JavaScript bundle small and avoid heavy dependencies.

## 5. Tests for this phase

| Test | Level | What it proves |
| --- | --- | --- |
| Key creation: full key shown once, only the hash is stored | integration | Key hygiene |
| Valid, invalid, revoked, and expired keys | integration | Auth |
| Scope enforcement: key without scans:write cannot create scans | integration | Authorization |
| Constant-time comparison used (code review check plus unit test on helper) | unit | Timing safety |
| Rate limit returns 429 with Retry-After, and resets | integration | Limits |
| Daily quota exhaustion returns the documented error | integration | Quotas |
| One client cannot read another client's scans | integration | Tenant isolation (critical) |
| Every endpoint response validates against the OpenAPI spec | contract | Spec fidelity |
| Every error response matches the error model | contract | Consistency |
| Webhook signature verifies with the correct secret, fails with a wrong one or an old timestamp | unit | Signing |
| Webhook retries on 500, stops after the limit, dead-letters, replay works | integration | Reliability |
| Webhook URL pointing at localhost or a private range is rejected | unit | SSRF guard |
| Sandbox keys return fixed responses and never call real providers | integration | Cost safety |
| CORS: allowlisted origin works, other origins are blocked | integration | Browser security |
| Public result never contains a score, the word safe, or internal IDs | contract | Output rules |
| Web e2e: upload fixture photos, see a result, ask a suggested question, see citations | e2e | Happy path |
| Web e2e: refusal for an advice question is shown with the fixed template | e2e | Policy in the UI |
| Web e2e on throttled network (slow 3G profile): upload succeeds or retries cleanly | e2e | Connectivity |
| axe checks: no serious violations; keyboard navigation works; contrast passes | e2e | Accessibility |
| Load test: target throughput at p95 latency within the budget from document 12 | load | Capacity |

## 6. Pitfalls and guards

- **Key leakage.** Never log Authorization headers or full keys. Redact at the logging layer.
- **Missing tenant checks.** Every query for scans and conversations must include the client ID. Write a test that tries cross-client access on every endpoint.
- **Webhook SSRF.** Customers choose URLs. Block private addresses, resolve and re-check at connect time, and set short timeouts.
- **Overly permissive CORS.** Do not use a wildcard with credentials or keys.
- **Breaking the public contract.** Use contract tests in CI and a changelog.
- **Large uploads from phones.** Resize client-side, but still enforce limits server-side.
- **Showing too much.** Numeric scores or technical codes on the user screen cause misreadings. Use the verdict wording and reasons.
- **Disclaimer fatigue.** Keep it short, always visible, and consistent.
- **Poor connectivity.** Make every request retry-safe and show clear progress and recovery.
- **Web client reaching into internals.** It may use only documented endpoints, or the API is not truly integrable.

## 7. Definition of Done

- A third party can follow the quickstart and get a result using a sandbox key in under 30 minutes.
- All contract, isolation, and e2e tests pass. Accessibility checks pass.
- Checklist ticked, tracker updated.

## 8. Notes for agents

- Update openapi.yaml first, then implement, then run contract tests.
- Every new endpoint needs: scope, rate limit class, tenant isolation test, error cases in the spec.
- Do not add fields to public responses without checking the output rules (no score, no safe, no internal IDs).

## 9. Implementation status and blockers

The platform, contract, and web client are implemented and covered by unit,
contract, and testcontainers integration tests. The end-to-end, accessibility,
and load evidence is not.

### What exists

- `internal/platform/auth`: `mv_<prefix>_<secret>` key generation, SHA-256
  hashing, prefix lookup with a constant-time compare, scopes, `DBStore`,
  `SingleKeyStore` for dev, and `Principal.TenantKey()` so the raw key is never
  stored on a scan.
- `internal/platform/ratelimit` and `auth.DBUsageStore`: per-key and per-IP
  token buckets, daily request/scan/question counters reserved in one
  transaction, failing closed with `503` when the counter cannot be written.
- `cmd/admin apikey create|list|revoke`: the full key is printed once and never
  readable again; plan flags apply only when a client is first created.
- `internal/scan`: `GetForTenant` returns `404` for another client's scan, the
  public view drops the score and gains `disclaimer`, `sandbox`, and rendered
  `reasons[].text`, and sandbox scans finish with a fixed needs-review result
  without touching a provider.
- `internal/webhook`: HMAC-SHA256 over `"<timestamp>.<body>"`, an SSRF guard
  enforced at save and connect time, backoff with dead-lettering, and a worker
  dispatcher.
- `api/`: OpenAPI 3 document with scopes, `401`/`403`/`429`, rate-limit headers,
  and examples; contract tests validate the document and real response bodies.
- `web/`: three static files with no build step and no dependencies. The client
  resizes to a 1568px long edge, uploads at most four photos, retries with the
  same idempotency key, renders reasons and sourced flags, and asks suggested
  questions. The key lives in memory only.

### Verification run

`go build ./...`, `go vet ./...`, `go test ./... -short`, the full
`internal/scan` and `internal/platform/auth` integration suites (real Postgres
via testcontainers), and `node --check web/assets/app.js`. The web client has
not been exercised in a browser, and the API + Postgres + S3 + worker stack has
not been run together.

### Blockers kept open

- No agreed CORS origin list, so production refuses to start without one and no
  origin has been confirmed with the web hosting decision.
- No agreed client plans: the defaults in migration 00010 are engineering
  placeholders, not commercial limits.
- No sandbox test-image contract from the product side. The implemented
  behaviour is a fixed result for any accepted upload, not a fixture catalogue.
- Webhook signing secrets are stored in plaintext because signing needs them.
  Encryption at rest (envelope or KMS) is a phase 12 hardening item.
- Playwright and axe-core are not available in this environment, so the web e2e,
  refusal-template, throttled-network, and accessibility rows in section 5
  cannot be run or claimed.
- No load-test budget exists, so the p95 target row in section 5 cannot be run.
- No live provider, Greenbook, or NAFDAC acceptance run was performed; CI
  continues to use fakes and fixtures only.
