# Operations runbook

Day-two operations for MedVerify NG. Everything here is a documented command or
a known gap; nothing assumes an on-call rota that does not exist yet.

## 1. Health and readiness

- `GET /healthz` — the process is alive. It never checks a dependency, so a
  database outage cannot cause a restart loop.
- `GET /readyz` — Postgres and object storage answer within the readiness
  timeout. Use this for a load balancer.
- `GET /metrics` — Prometheus text. Only aggregate counters and durations,
  labelled by route pattern.

Starting the stack locally:

```bash
cp .env.example .env
make run
curl localhost:8080/readyz
```

## 2. Turning features off

These are environment switches read at startup, so changing one is a deploy.
That is deliberate: the rollback path is a tested deploy, not an ad-hoc change.

| Switch | Effect |
| --- | --- |
| `FEATURE_API_ENABLED=false` | Every `/v1` route answers `503 unavailable`; probes stay up |
| `FEATURE_SCANS_ENABLED=false` | New scan submissions are refused; existing scans stay readable |
| `FEATURE_ASSISTANT_ENABLED=false` | Question endpoints are refused; verdicts still work |
| `GREENBOOK_ENABLED=false` | The fetcher refuses every outbound call |
| `RETENTION_ENABLED=false` | The scheduled retention sweep stops; manual sweeps still work |

Switches answer before authentication, so a disabled feature spends no database
capacity.

## 3. Provider outage

- A vision provider failure marks the scan `failed` and the job is retried; the
  API never returns a verdict without extraction. A configured
  `VISION_FALLBACK_MODEL` is used only when the primary is unavailable.
- An assistant outage does not affect verdicts. The web client shows the error;
  the API returns `503 unavailable`.
- A missing or misconfigured provider fails at startup, not at the first scan.

There is no circuit breaker on the embedding provider; a retrieval outage makes
the question panel fail rather than answer from stale index. This is a gap.

## 4. Background worker

The worker runs scan jobs, webhook delivery, Greenbook sync, optional leaflet
indexing, and the retention sweep. If it stops:

1. Uploads still return `202` but never complete.
2. Check worker logs for the first fatal error; the process exits non-zero on a
   startup misconfiguration.
3. Restart it. Scan jobs are idempotent: the claim and finish updates make a
   duplicate job a no-op.

## 5. Retention sweep

Scheduled by the worker every `RETENTION_SWEEP_INTERVAL` (default 6h). Run it by
hand after changing policy or after a worker outage:

```bash
medverify-admin retention sweep
```

It prints images deleted, conversations deleted, and failed deletes. A failed
delete leaves the row in place so the next pass retries it. **Gap:** if the sweep
stops running, nothing alerts.

## 6. Erasing a scan

For a data-subject request or a support request:

```bash
medverify-admin apikey list                 # confirm the owning client first
medverify-admin scan delete <scan-id> --confirm
```

This deletes images, then the scan row and everything that cascades from it
(conversation, result, flags, deliveries, idempotency). The cost ledger keeps the
spend row with a null scan reference. If an object delete fails, nothing is
deleted and the command can be retried.

## 7. API keys

```bash
medverify-admin apikey create --client acme --scopes scans:write,scans:read,questions:write
medverify-admin apikey list
medverify-admin apikey revoke mv_abc123
```

The full key is printed once. A lost key is revoked and replaced; it cannot be
recovered. Plan flags apply only when the client is first created.

Rotation procedure (manual today): create a second key, deploy the integrator's
configuration with the new key, confirm traffic on the new prefix, then revoke
the old one. **Gap:** no scheduled rotation and no ownership recorded.

## 8. Webhooks

```bash
medverify-admin webhook list 50     # dead deliveries with client and URL
medverify-admin webhook replay <delivery-id>
```

Replay only accepts a dead delivery. The worker retries it on its next ticker;
a `4xx` other than `429` dead-letters it again immediately.

## 9. Deploys and rollback

- Migrations are goose files embedded in the binary and reversible. Apply with
  `medverify-admin migrate up`; review `migrate status` first.
- Roll back application code by deploying the previous image. Roll back a
  migration only with the matching down migration, on a copy first if data
  changed.
- **Gap:** no tested backup/restore drill and no blue/green procedure yet.

## 10. Incident process

There is no on-call rota. Until one exists, an incident is handled by the
project lead.

1. Contain: use the feature switches above; revoke a compromised key.
2. Preserve: capture the request ID from the error response and the matching log
   line. Never copy image bytes, questions, or keys into a ticket.
3. Assess: was any verdict produced without extraction? Was personal data
   exposed? If yes, stop and follow the data-protection decision, which is still
   **open**.
4. Recover and record what changed.

## 11. Known operational gaps

- No dashboards or alert routing; `/metrics` exists but nothing scrapes it.
- No tracing; only request IDs in logs.
- Webhook signing secrets are plaintext at rest.
- Backups, restore, and migration rollback drills are not run.
- Provider data terms and cross-border transfer are unresolved.
- Load, soak, abuse, and independent security tests are not run.
