# Data handling rules

These rules apply to every phase. They are short on purpose: they are review
criteria, not aspirations.

## 1. Photos

- Pack photos are user data. Strip metadata (EXIF, GPS, device identifiers)
  before the image is stored or sent to a vision provider. Phase 3 implements
  the stripping; the rule is fixed here.
- Precise user location is never stored. Location is not required to verify a
  pack.
- Images are deleted on the retention schedule chosen in phase 12; the schedule
  is configuration, not a hard-coded interval.

## 2. Logs

- Logs are structured JSON and carry a request ID.
- Never log images, base64 payloads, API keys, session tokens, or raw user text.
  The `logging.Redactor` in `internal/platform/logging` masks configured secrets
  and long base64-like strings; register every secret at startup.
- Provider errors are logged with the provider name and status, not with the
  request body.

## 3. Third-party providers

- Vision and language model providers sit behind Go interfaces in
  `internal/vision` and `internal/llm`. No package outside those interfaces may
  call a provider.
- CI never calls a real provider. Tests use fakes and recorded golden responses.
- Hosting region and data residency for provider calls are decided in phase 12.
  Until then, assume no production data leaves the pilot environment.

## 4. Registry and leaflet data

- Greenbook and leaflet content is used for verification only, under the
  permission decision recorded in phase 02.
- Watchlist entries always store their source URL and reviewer.

## 5. Secrets and configuration

- Configuration comes from environment variables only, parsed once by
  `internal/platform/config`. No package reads the environment directly.
- Secrets live in the deployment platform, never in the repository. `.env` is
  git-ignored; `.env.example` holds placeholders and comments only.
- CI runs a secret scan (gitleaks) on every push.

## 6. User-facing responses

- Every factual statement carries a source.
- Error responses use RFC 7807 problem details with a stable `code`; internal
  errors never expose stack traces or provider messages.
- Readiness and any personalized response are `no-store`.
