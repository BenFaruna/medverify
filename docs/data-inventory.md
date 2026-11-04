# Data inventory

What MedVerify NG stores, where it lives, why, and for how long. This is the
phase 12 privacy inventory; it describes the code that exists, not an intended
future design. Anything marked **open** has no agreed answer yet.

## 1. Scan images

| | |
| --- | --- |
| What | The JPEG the user uploads, resized server-side to a 1568px long edge |
| Where | S3-compatible object storage, key `scans/<scan-id>/<random>.jpg` |
| Why | Read the printed label; the original filename and EXIF metadata are discarded before storage |
| Retention | 30 days for an ordinary scan, 365 days when the scan has a safety flag (configurable) |
| Who else sees it | The configured vision provider receives the prepared JPEG. Provider data terms are **open** |
| Deletion | `retention` sweep in the worker, or `medverify-admin scan delete <id> --confirm` |

Raw uploads are never written to disk by the API process. Images are not logged,
not used as metric labels, and not stored in the cost ledger.

## 2. Scan, verdict, and flags

| | |
| --- | --- |
| What | Scan status, extracted label, registry comparison, verdict, confidence band, reason codes, safety flags with their cited source |
| Where | Postgres: `scans`, `scan_results`, `safety_flags`, `scan_images` (keys only after image deletion) |
| Why | Show a result, explain it, and keep an auditable record of a flagged case |
| Retention | Kept beyond image deletion. This is deliberate: a safety finding must remain reviewable. Whether an ordinary completed scan should expire is **open** |
| Deletion | Erasing a scan cascades to results, flags, conversations, deliveries, and idempotency rows |

## 3. Questions and answers

| | |
| --- | --- |
| What | The user's question, the grounded answer, cited source IDs, intent, language, verifier outcome |
| Where | Postgres: `conversations`, `messages`, `assistant_tool_calls` |
| Why | Keep the conversation coherent within a session; the API serves it back |
| Retention | Conversation TTL (default 24h) sets `expires_at`; the retention sweep deletes expired conversations |
| Deletion | Cascades when the scan is erased |

## 4. Location

Only a coarse grid cell (`scans.location_cell`) is stored from optional
latitude/longitude. Raw coordinates are never persisted or logged.

## 5. API clients and keys

| | |
| --- | --- |
| What | Client name, plan limits, sandbox flag; key prefix and SHA-256 hash; scopes; expiry and last-used time |
| Where | Postgres: `api_clients`, `api_keys`, `usage_counters` |
| Why | Authenticate, authorize, and meter usage |
| Retention | Until revoked. `last_used_at` is operational telemetry |
| Note | The full key is shown once at creation and is not recoverable |

## 6. Webhooks

`webhook_endpoints` stores the customer URL and its **plaintext** signing secret,
because the secret is required to sign each delivery. Encryption at rest is an
open phase 12 item. `webhook_deliveries` stores the public scan payload, status,
attempts, and the last error; it cascades when the scan is erased.

## 7. Cost and latency ledger

`llm_calls` records provider, model, prompt version, latency, token counts,
estimated cost, and outcome. It has no columns for prompts, answers, images, or
extracted text. Deleting a scan nulls `scan_id` instead of deleting spend
history, so the ledger becomes anonymous rather than lost.

## 8. Logs and metrics

- Logs are JSON lines: method, path, status, duration, response size, request ID,
  client name and key prefix. Authorization headers, request bodies, and images
  are never logged. Configured secrets are registered with the log redactor.
- `/metrics` exposes aggregate HTTP counters and durations labelled by route
  pattern, plus named application events. No scan ID, key, question, or image
  appears in a metric.
- Container and platform logs are retained by the hosting environment, not by
  this application. Their retention period is **open**.

## 9. Public reference data

Greenbook products, leaflet sections and chunks, and watchlist entries are
public regulatory or safety material, not personal data. Leaflet PDFs are stored
in object storage; chunks and embeddings in Postgres.

## 10. Cross-border transfer

Sending images and questions to a model provider can move health-related data
out of Nigeria. The provider, region, and data-processing terms are **open** and
must be resolved before a pilot (see the phase 12 document). Until then, CI and
local development use the deterministic fake provider, which sends nothing.

## 11. Erasure path

`medverify-admin scan delete <id> --confirm`:

1. deletes every image object for the scan,
2. deletes the scan row, which cascades to images, results, flags, conversation,
   messages, tool calls, webhook deliveries, and idempotency records,
3. leaves the cost ledger with a null scan reference.

If an object delete fails, the command stops before deleting the row, so the
keys are still known and the operation can be retried.
