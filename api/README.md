# MedVerify NG API

The API compares a photographed medicine pack against NAFDAC records and returns
an explainable verdict. It never declares a medicine safe, genuine, or
authentic, and it never says a pack is fake. "Not found in records" is a
statement about our data, not about the pack.

- Machine contract: [`openapi.yaml`](openapi.yaml). CI validates it and runs
  contract tests against real responses.
- Interactive docs: the API serves Swagger UI at `/docs`; the raw document is
  at `/openapi.yaml`. Both are embedded in the binary and need no CDN, Node
  build, or extra service.

## Contents

1. [Interactive documentation](#interactive-documentation)
2. [Quickstart](#quickstart)
3. [Authentication and scopes](#authentication-and-scopes)
4. [Errors](#errors)
5. [Rate limits and quotas](#rate-limits-and-quotas)
6. [Endpoints](#endpoints)
7. [Webhooks](#webhooks)
8. [Sandbox](#sandbox)
9. [Versioning and deprecation](#versioning-and-deprecation)

## Interactive documentation

Start the stack, then open:

- <http://localhost:8080/docs> — Swagger UI with every documented endpoint,
  request body, response schema, and error model.
- <http://localhost:8080/openapi.yaml> — the raw OpenAPI 3.1 contract.

```bash
make run
open http://localhost:8080/docs        # macOS
xdg-open http://localhost:8080/docs    # Linux
```

Use **Authorize** in Swagger UI to paste an API key, then use **Try it out** on
any endpoint. The key is kept in memory only and is not written to browser
storage; re-enter it after a reload. The docs page is same-origin, so the
relative server entry in the spec makes requests against whichever host served
the page. File uploads (`POST /v1/scans`) work through the generated multipart
form.

Both routes are public: they expose the contract, not scan data. Every `/v1`
route still requires a key and its documented scope even when called from the
docs page. The documentation routes are mounted by `cmd/api` before the static
web client and are not affected by the API kill switch.

The UI assets are vendored under [`swaggerui/`](swaggerui) (Apache-2.0,
Swagger UI 5.33.0) so local development and air-gapped deployments work without
a CDN. The raw spec and UI are embedded with `go:embed`, so a binary built from
a clean checkout serves them directly.

## Quickstart

Programmatic requests need a key in `X-API-Key`. The key format is
`mv_<prefix>_<secret>`; the server looks it up by prefix and compares a SHA-256
hash of the full value in constant time, so only the hash is stored. A browser
uses the session cookie set by `POST /v1/auth/login` instead, and never holds a
key; see [Authentication and scopes](#authentication-and-scopes).

```bash
export MEDVERIFY_KEY="mv_...your key..."

curl -sS http://localhost:8080/v1/scans \
  -H "X-API-Key: $MEDVERIFY_KEY" \
  -H "Idempotency-Key: $(uuidgen)" \
  -F "images=@front.jpg" \
  -F "images=@back.jpg" \
  -F "language=en"
```

A finished scan answers `201` with the result. A scan that is still working
answers `202` with a `Location` header; poll it.

```bash
curl -sS http://localhost:8080/v1/scans/<id> -H "X-API-Key: $MEDVERIFY_KEY"
```

### Go

```go
req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/scans/"+id, nil)
if err != nil {
	return err
}
req.Header.Set("X-API-Key", key)

resp, err := http.DefaultClient.Do(req)
if err != nil {
	return err
}
defer resp.Body.Close()
```

### JavaScript

```js
const resp = await fetch(`${baseURL}/v1/scans/${id}`, {
  headers: { "X-API-Key": key },
});
if (!resp.ok) {
  const problem = await resp.json();
  // Branch on problem.code, never on problem.detail.
  throw new Error(problem.code);
}
const scan = await resp.json();
```

Always send the same `Idempotency-Key` and the same body when retrying an
upload. The same key with a different body returns `422`.

## Authentication and scopes

| Scope | Grants |
| --- | --- |
| `scans:write` | `POST /v1/scans` |
| `scans:read` | `GET /v1/scans/{id}`, `GET /v1/scans/{id}/conversation` |
| `questions:write` | `POST /v1/scans/{id}/questions` |

A key without the scope for an endpoint gets `403 insufficient_scope`. A
missing, unknown, revoked, or expired key gets `401 unauthorized`, with no
detail about which case it was.

In development the server also accepts the shared `API_LEGACY_KEY`
(`dev-api-key` by default) for the quickstart. Database-backed keys are checked
first and work at the same time. The bundled web page does **not** use it: the
page signs in and sends the session cookie, so no key lives in its source or
its storage. Production rejects a non-empty `API_LEGACY_KEY` at startup.

### Two credentials, one client

| Caller | Sends | Why |
| --- | --- | --- |
| A person in the bundled web client | `mv_session` cookie | `HttpOnly`, `SameSite=Lax`; script cannot read it and another site cannot send it on a write |
| Their own code, a CI job, an integration | `X-API-Key` | A long-lived secret the caller controls and can rotate |
| Anyone in Swagger UI at `/docs` | `X-API-Key` via **Authorize** | The UI is served from this origin, but a key is the documented credential for scripted calls |

Both credentials resolve to the same `api_clients` row, so they share scopes,
rate limit, and daily quotas. `X-API-Key` wins when both are present, so an
integration's plan always comes from its own key. A cookie-authenticated write
from another origin returns `403 forbidden`; a key request needs no such check
because a browser cannot set the header without an approved CORS preflight.

An account is created at registration, and the first key comes back in the same
response:

```bash
curl -sS -X POST http://localhost:8080/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"ada@example.test","name":"Ada","password":"a-long-enough-password"}'
# 201 with user, session.expires_at, and api_key.key — the only time the key is shown
```

| Account endpoint | Credential | Purpose |
| --- | --- | --- |
| `POST /v1/auth/register` | none | Create the account, its first key, and a session |
| `POST /v1/auth/login` | none | Start a session |
| `POST /v1/auth/logout` | none | Revoke the session and clear the cookie |
| `GET /v1/account` | session cookie | The signed-in account |
| `GET /v1/account/api-keys` | session cookie | Key metadata: prefix, scopes, state, timestamps |
| `POST /v1/account/api-keys/rotate` | session cookie | Revoke active keys, issue one replacement |

Every one of these is documented in `openapi.yaml`, and
`TestEveryV1RouteIsDocumented` fails the build if a route is added without an
operation or a documented operation loses its route.

Keys are per client. One client cannot read another client's scans: a scan that
belongs to someone else is reported as `404`, exactly like a scan that does not
exist, so a response cannot be used to probe for IDs.

Never log the key. Server logs record the client name and the key prefix, never
the secret.

### Issuing a key

Keys are issued by an operator with the admin CLI, not through the public API.
Creating a key is the only moment the full value exists outside the caller's
hands; only its SHA-256 hash is stored.

```bash
medverify-admin apikey create --client acme \
  --scopes scans:write,scans:read,questions:write
```

The command prints the key once. It can list prefixes and revoke a key later,
but it can never show a key again:

```bash
medverify-admin apikey list
medverify-admin apikey revoke mv_abc123
```

Pass `--sandbox` to make a test client, `--expires 720h` for a fixed lifetime,
and `--rate`, `--daily-requests`, or `--daily-scans` to set the plan. Plan flags
only apply when the client is first created, because limits belong to the
client, not to each key.

## Errors

Every error is an RFC 7807 problem document with a stable `code`.

```json
{
  "type": "https://medverify.ng/problems/insufficient_scope",
  "title": "Forbidden",
  "status": 403,
  "detail": "The API key does not have permission to perform this action.",
  "instance": "/v1/scans",
  "code": "insufficient_scope"
}
```

Branch on `code`. `detail` is for humans and may be reworded.

| Code | Status | Meaning |
| --- | --- | --- |
| `bad_request` | 400 | Malformed body or parameters |
| `invalid_location` | 400 | Latitude and longitude were not a valid pair |
| `invalid_language` | 400 | Language tag is not a short BCP 47 value |
| `too_many_images` | 400 | More images than the endpoint accepts |
| `corrupt_image` | 400 | An image could not be decoded |
| `unauthorized` | 401 | Missing or invalid key |
| `insufficient_scope` | 403 | The key lacks the required scope |
| `not_found` | 404 | No such scan for this client |
| `method_not_allowed` | 405 | Wrong method for the path |
| `conflict` | 422 | Idempotency key reused with a different body |
| `unsupported_format` | 415 | Not JPEG, PNG, or WebP |
| `image_too_large` | 413 | One image is over the limit |
| `payload_too_large` | 413 | The whole body is over the limit |
| `rate_limited` | 429 | Too many requests, or a conversation hit its turn limit |
| `quota_exceeded` | 429 | The key's daily allowance is used up |
| `internal_error` | 500 | Unexpected server failure |
| `unavailable` | 503 | A dependency is down, or the usage counter is unwritable |
| `timeout` | 504 | The request exceeded its budget |

## Rate limits and quotas

Two limits apply to every authenticated request:

- a token bucket per key, from the client's plan row (`rate_per_min`), and
- a token bucket per calling address, which protects the unauthenticated edge.

Responses carry `X-RateLimit-Limit` and `X-RateLimit-Remaining`. A rejection is
`429 rate_limited` with `Retry-After` in seconds.

Daily quotas are database counters, not code, so a plan change needs no deploy:

- every request counts against the daily request quota, and
- creating a scan also counts against the daily scan quota.

Running out returns `429 quota_exceeded` with `Retry-After` set to the seconds
until the counters reset at midnight UTC. A scan is never counted twice: one
scan consumes one request and one scan unit.

If the usage counter cannot be written the request is refused with `503`
rather than allowed, so an outage cannot become unbounded spend.

## Endpoints

### `POST /v1/scans`

Multipart upload of one to four images plus optional `latitude`, `longitude`,
`language`, and an `Idempotency-Key` header. Coordinates are reduced to a coarse
grid cell; raw coordinates are never stored or logged, and image metadata is
stripped before storage.

`201` when the scan finished within the sync wait, `202` when it did not.

### `GET /v1/scans/{id}`

The current state and, once terminal, the result.

The public result contains the status, the verdict with its confidence band, the
reasons with reviewed text, the extracted label fields with per-field
confidence, the registry comparison with its dataset size, the sourced safety
flags, and a fixed disclaimer.

It never contains the internal numeric score, and it never contains a claim that
a medicine is safe, genuine, or authentic. Display `reasons[].text`; never
display `reasons[].code`.

### `POST /v1/scans/{id}/questions`

Ask a question about that pack. Answers come only from the registry record,
sourced flags, and stored leaflet passages, and every factual claim carries a
citation. Advice questions return a fixed refusal template. There is no medical
advice, diagnosis, or personal dosing.

### `GET /v1/scans/{id}/conversation`

The stored transcript. `Cache-Control: no-store` because it contains user text.

## Webhooks

Subscribe a public HTTPS URL to `scan.completed`. Inbound:

- `X-MedVerify-Signature: v1=<hex HMAC-SHA256>`
- `X-MedVerify-Timestamp: <unix seconds>`
- `X-MedVerify-Event: scan.completed`
- `X-MedVerify-Delivery: <delivery uuid>`

The signature covers `"<timestamp>.<raw body>"` with the endpoint's secret.
Verify against the raw bytes, before any JSON parsing.

```go
mac := hmac.New(sha256.New, []byte(secret))
mac.Write([]byte(timestamp + "." + string(rawBody)))
expected := "v1=" + hex.EncodeToString(mac.Sum(nil))
if !hmac.Equal([]byte(expected), []byte(header)) {
	http.Error(w, "bad signature", http.StatusUnauthorized)
	return
}
```

Reject a timestamp more than a few minutes old (and one far in the future):
that is what stops a captured delivery from being replayed.

Respond `2xx` to acknowledge. Anything else is a failed attempt:

| Outcome | Behaviour |
| --- | --- |
| `2xx` | delivered |
| `429` or `5xx` | retried with exponential backoff |
| other `4xx` or `3xx` | dead-lettered immediately, because retrying cannot help |

After the attempt limit a delivery is dead-lettered and can be replayed. Only a
`2xx` counts as delivered.

Webhook URLs must be HTTPS and must not resolve to a private, loopback, or
link-local address. The check runs when the endpoint is saved and again at
connect time, so a DNS answer cannot swing the connection into an internal
network. Redirects are not followed.

The payload is the public scan object, so a webhook can never carry the internal
score.

## Sandbox

A sandbox key returns a fixed result for any accepted upload and never calls a
vision or language provider, so an integration can be built without cost. The
result is always `needs_review` at `low` confidence with the
`insufficient_label_data` reason and `sandbox: true`; it is a shape to build
against, not a statement about the uploaded image. Sandbox results carry the
same schema and the same disclaimer as live ones, and are labelled `sandbox` in
the stored provider field.

## Pagination

`/v1` has no collection endpoint that returns an unbounded list, so no response
is paginated yet. Any future list endpoint uses opaque cursors (`limit` plus
`cursor`), never offsets, caps `limit`, and returns `next_cursor` only when more
results exist. Cursors are opaque: do not parse or construct them.

## Versioning and deprecation

- Everything lives under `/v1`. Only additive changes are allowed inside a
  version: new optional fields, new enum values a client can ignore, new
  endpoints.
- Removing or re-typing a field, or narrowing an enum a client must handle,
  requires `/v2`.
- A deprecated field or endpoint stays available for at least one announced
  deprecation window and is marked in the OpenAPI description before removal.
- Contract tests run in CI, so a change that breaks a documented response fails
  before it merges.
