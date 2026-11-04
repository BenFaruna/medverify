# 12 Hardening and Pilot Readiness

**Goal:** make the system safe, observable, resilient, and legally defensible enough to run a real pilot, and define how the pilot will be measured and rolled back.

**Depends on:** all earlier phases. **Blocks:** pilot launch. **Read first:** every other document, especially 04, 09, 10, 11.

## 1. Deliverables checklist

**Privacy and legal**

- [x] Data inventory: every kind of data stored, where, why, and for how long (see [data-inventory.md](../data-inventory.md))
- [ ] Retention jobs: images, questions, and logs deleted on schedule, with an exception path for flagged cases — image and conversation retention implemented and tested; container/platform log retention is not controlled by this application and remains open
- [ ] Consent and privacy notice shown in the web client and described in the API terms
- [ ] Data protection review under Nigeria's data protection law, including any cross-border transfer of images and questions to model providers. Obtain qualified legal advice. Record the decisions
- [ ] Data processing agreements with each provider, and a written position on whether providers may retain or train on submitted data
- [ ] Wording review of verdicts, flags, refusals, and disclaimer by legal and a pharmacist
- [ ] Terms of use for integrators

**Security**

- [x] Threat model (what an attacker, a counterfeiter, or an abusive integrator could do) — first version written, not independently reviewed (see [threat-model.md](../threat-model.md))
- [ ] Dependency and container scanning in CI, secrets in a secret manager, key rotation procedure — govulncheck and gitleaks run in CI, and rotation is documented in the runbook; a secret manager and container scanning are missing
- [ ] Abuse tests of the scan endpoint (probing, replay, resource exhaustion)
- [ ] Independent security test or a structured internal penetration test

**Reliability and operations**

- [ ] Metrics, traces, dashboards, and alerts (see section 4) — `/metrics` exposes aggregate HTTP counters and durations; no tracing, dashboards, scrapers, or alert routing
- [ ] Degraded modes for provider outages — retry/fallback and feature switches exist, but the outage drill has not been run
- [ ] Backups with a tested restore, and a rollback procedure for deploys and migrations
- [x] Runbooks and an incident process — draft runbook written (see [ops/runbook.md](../../ops/runbook.md)); no on-call rota or named incident owner
- [ ] Cost dashboard and spending alerts

**Pilot**

- [ ] Accuracy trial protocol agreed with NAFDAC or an independent lab
- [ ] Pilot plan: sites, partners (pharmacies, health workers), training, support channel
- [ ] Go or no-go checklist signed by the project lead

## 2. Tools needed

| Tool | Purpose |
| --- | --- |
| govulncheck, a container scanner (for example Trivy), gitleaks | Security scanning |
| Prometheus-compatible metrics and OpenTelemetry tracing | Observability |
| A dashboard tool (for example Grafana) and an alerting channel | Monitoring |
| k6 or vegeta | Load tests |
| Chaos helpers: a fake provider that fails, slows, or returns garbage | Resilience tests |
| pg\_dump or managed backup tooling, restore environment | Backup drills |

## 3. Setup required for success

1. A staging environment that mirrors production (same image, same extensions, realistic data volume) with provider sandbox keys.
2. Named owners for: security, data protection, on-call, the watchlist, and pilot partner relations.
3. A budget for providers, hosting, and testing, with alert thresholds.
4. Legal counsel engaged early. Reviews take time and may change designs.
5. Agreement with NAFDAC on how suspected counterfeits found during the pilot are handled and reported, and on how packs can be sent for lab confirmation.

## 4. Observability and service levels

| Signal | Metric | Starting target (agree before launch) |
| --- | --- | --- |
| Latency | Time from upload to completed result, p50 and p95 | p95 under 10 seconds, matching the proposal |
| Availability | Successful responses over total | An agreed monthly target |
| Extraction quality | Share of scans ending unreadable | Tracked, with an alert on sudden change |
| Provider health | Error and timeout rate, circuit breaker state | Alert on sustained errors |
| Cost | Spend per scan and per day, by provider | Alert above budget |
| Safety | Refusal rate, advice-leak review queue size, verifier rejection rate | Reviewed weekly |
| Data freshness | Age of the last successful Greenbook sync, watchlist review age | Alert beyond agreed age |
| Queue | Job lag and retry counts | Alert on growth |

Add a request ID to every log line and trace so a single scan can be followed end to end. Never include images or raw user text in metrics or traces.

## 5. Degraded modes

| Failure | Behavior |
| --- | --- |
| Vision provider down | Scans return a retryable error. Optionally fall back to the second provider. Never produce a verdict without extraction |
| LLM provider down | Scan verdicts still work (they do not need the LLM). The question panel shows a clear temporary message |
| Embedding provider down | Existing indexed leaflets still searchable (queries need embeddings, so fall back to full-text only if tested) |
| Greenbook sync failing | Keep serving the last good data and show its date. Alert the owner |
| Database failover | Clients retry with idempotency keys. No duplicate scans |

## 6. Tests for this phase

| Test | Level | What it proves |
| --- | --- | --- |
| Retention job deletes images and questions past the limit and leaves flagged cases per policy | integration | Privacy |
| Deleting a scan removes its images, results, conversation, and derived data | integration | Right to erasure path |
| Logs, metrics, and traces scanned for image data, API keys, and raw coordinates in a full e2e run | integration | No leakage |
| Load test at the target concurrency: p95 within budget, error rate under the agreed limit | load | Capacity |
| Soak test for several hours: no memory growth, no connection leaks, queue stable | load | Stability |
| Provider outage drill (fake returns errors, then timeouts, then garbage): every degraded mode behaves as in section 5 | integration | Resilience |
| Circuit breaker and retry storms do not exhaust the database pool | integration | Back-pressure |
| Backup restore drill into a clean environment, then an e2e smoke test | drill | Recoverability |
| Migration rollback drill on a copy of production-size data | drill | Safe deploys |
| Abuse tests: oversized uploads, rapid-fire scans, malformed multipart, replayed idempotency keys, key brute force | security | Hardening |
| Cross-tenant access attempts on every endpoint | security | Isolation |
| Dependency, container, and secret scans clean (or documented exceptions) | CI | Supply chain |
| Alerts fire: simulate each alert condition in staging and confirm the page arrives | drill | Monitoring works |
| Full eval suite on staging with production configuration meets all release gates (document 10) | eval | Quality gate |
| Accuracy trial dry run: process 100 packs with known ground truth and produce the pilot report format | trial | Pilot readiness |

## 7. Pilot design

Pilot sites (proposed in the original document: Lagos, Kano, Onitsha). Measure the targets from the proposal, treating them as proposals to be agreed:

| Indicator | How measured | Notes |
| --- | --- | --- |
| Detection quality | Compare verdicts with NAFDAC lab results on sampled packs | Define precisely what counts as correct (for example, flagged or needs\_review when lab-confirmed bad) |
| Response time | Upload to result | From metrics |
| Active users | Monthly users completing a scan | Count by anonymous client IDs |
| User satisfaction | Short in-app rating | Optional and non-identifying |
| Enforcement follow-up | Flagged cases passed to NAFDAC and acted on | Needs the NAFDAC process in setup item 5 |

Hotspot mapping from the original proposal is out of scope for this build. If added later, it must use coarse grid cells only (phase 03 already stores no precise location).

## 8. Pitfalls and guards

- **Launching without a way to turn things off.** Have feature flags for the question assistant, the vision provider choice, and the whole service, plus a tested rollback.
- **Alert noise.** Too many alerts train people to ignore them. Start with a few that matter.
- **Ignoring provider data terms.** Sending health-related images and text abroad can create legal exposure. Resolve before launch, not after.
- **Pilot without ground truth.** Without lab-confirmed samples, accuracy claims are unsupported. Secure the lab arrangement first.
- **Success measured by volume.** Users and scans are not outcomes. Report quality and follow-up as well.
- **Unowned operations.** If nobody is on call, outages last until Monday.
- **Data accumulating forever.** Retention jobs must run and be monitored.
- **Overclaiming.** Marketing and onboarding text must match the verdict wording rules. Never promise protection.
- **One-way migrations.** Always rehearse rollback.

## 9. Go or no-go checklist

- [ ] All release gates in document 10 are met on staging
- [ ] Legal and pharmacist reviews signed
- [ ] Provider terms and data transfer position documented
- [ ] Backup and rollback drills completed within the last 30 days
- [ ] On-call rota, runbooks, and support channel live
- [ ] Kill switches tested
- [ ] NAFDAC handling process for flagged cases agreed
- [ ] Pilot measurement plan and baseline recorded

## 10. Definition of Done

- Every checklist item ticked or explicitly waived by the project lead with a reason in this document.
- Go or no-go decision recorded with date and signatories. Tracker updated.

## 11. Notes for agents

- Do not weaken retention, redaction, or isolation tests to meet a deadline.
- Treat every drill as code: scripts under /ops, reviewed and repeatable, with the results written into this document.
- If you find sensitive data in logs or traces, stop and report it immediately.

## 12. Implementation status and blockers

Phase 12 is code-in-progress, not a pilot sign-off. The parts that can be built
without an external decision are in; the legal, operational, and independent
review parts are not.

### What exists

- `internal/retention`: a scheduled sweep that deletes scan images after the
  ordinary window, keeps flagged cases longer, deletes expired conversations,
  and never deletes a row whose object delete failed. Erasure removes images
  first and then the scan, so a failed delete leaves a retryable state.
- `cmd/admin retention sweep` and `cmd/admin scan delete <id> --confirm` for
  manual runs and data-subject requests.
- `internal/platform/metrics` and `GET /metrics`: aggregate HTTP counters and
  durations labelled by route pattern, never by concrete path.
- Config kill switches: whole API, scan submission, assistant, Greenbook, and
  the retention schedule.
- `cmd/admin webhook list|replay` so dead deliveries are reachable and can be
  requeued deliberately.
- `docs/data-inventory.md`, `docs/threat-model.md`, and `ops/runbook.md`.
- Provider choice is no longer binary: `claude` and the OpenAI-compatible
  `openai` wire format are selectable independently for vision and assistant.

### Verification run

`go build ./...`, `go vet ./...`, `golangci-lint run ./...`, `go test ./... -short`,
testcontainers integration tests for `internal/retention`, `internal/webhook`,
`internal/platform/auth`, and `internal/scan`, and the API tests for metrics and
kill switches. No load, soak, abuse, backup, restore, or log-leakage run has been
performed.

### Blockers that block a pilot

- Legal and pharmacist review of verdict, flag, refusal, and disclaimer wording.
- Data-protection review and provider data-processing terms, including
  cross-border transfer of images and questions.
- An independent security test or structured penetration test.
- A signed accuracy-trial protocol and lab ground truth.
- Named owners for security, data protection, on-call, watchlist, and pilot
  partners.
- Backup, restore, and migration-rollback drills on production-size data.
- Dashboards, alert routing, and a spending alert with an agreed budget.

### Blockers that do not block continued development

- Webhook signing-secret encryption at rest (plaintext today; needed before
  external customers, not before local testing).
- Container scanning and a secret manager.
- Log retention owned by the hosting platform.
- A load-test budget and p95 target.
- Whether ordinary, unflagged verdict records should expire.
- The embedding provider/dimension decision, which also leaves RAG eval recall
  unmeasurable.
