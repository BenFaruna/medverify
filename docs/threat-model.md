# Threat model

A first structured threat model for phase 12. It covers what an attacker, a
counterfeiter, or an abusive integrator could do, what the code already does, and
where the gaps are. It has not been reviewed by an independent security tester.

## 1. Assets

1. **Scan images and questions** — personal, potentially health-related data.
2. **API keys and webhook secrets** — grant access or allow forged deliveries.
3. **Verdict integrity** — a wrong reassurance is the highest-harm outcome.
4. **Provider spend** — an abusive caller can burn real money.
5. **Availability** — pharmacies and health workers need an answer quickly.

## 2. Actors

| Actor | Capability |
| --- | --- |
| Curious or careless client | Own key, can probe endpoints, upload odd files |
| Abusive integrator | Valid key, can automate uploads and questions |
| Counterfeiter | Wants to learn what the checks detect, or to make a pack pass |
| Network attacker | Can intercept traffic if TLS is wrong |
| Insider / operator | Can run admin commands and read the database |
| Third-party provider | Receives images and questions, may retain them per its terms |

## 3. Threats and current controls

### Cross-tenant reads

A client guesses or steals a scan ID. `GetForTenant` returns `404` for a scan
that belongs to another client, identical to a missing scan. Integration tests
cover the scan and conversation reads. **Gap:** every new endpoint must add the
same test; there is no automated check that a handler remembered the tenant key.

### Key theft and brute force

Keys are 32 random bytes with a 12-hex prefix; only a SHA-256 hash is stored;
lookup compares in constant time; failures are indistinguishable; per-IP and
per-key token buckets limit guessing. Keys can be revoked and expire.
**Gap:** no rotation schedule and no alert on a spike of `401`s.

### SSRF through webhooks

A customer supplies a URL that points at internal infrastructure. Only HTTPS is
accepted, credentials in the URL are rejected, all resolved addresses are
checked against private/loopback/link-local ranges, the socket-level dialer
rechecks, and redirects are not followed. **Gap:** DNS pinning is best-effort;
a determined attacker can still race resolution, though the dialer recheck
makes the window small.

### Webhook forgery or replay

A receiver cannot tell a real delivery from a forged one, or an attacker replays
an old one. Deliveries are signed with HMAC-SHA256 over
`"<timestamp>.<raw body>"` and carry a timestamp; receivers are told to reject
old timestamps. **Gap:** the signing secret is stored in plaintext at rest.

### Prompt injection through label text

A pack label contains instructions for the model. Extracted values are treated
as untrusted data, validated, and never executed; the assistant uses only
registered tools and a verifier that rejects forbidden wording and invented
citations. **Gap:** no adversarial image corpus in CI (blocked on consent and
labelling).

### Cost exhaustion

A valid key uploads at maximum rate. Per-key rate limits, daily request and scan
quotas stored in the database, a usage store that fails closed, and a sandbox
mode that never calls a provider bound the spend. **Gap:** no per-day spend cap
on the assistant beyond quotas, and no alert wired to cost yet.

### Data leakage through logs and metrics

Logs record method, path, status, duration, request ID, and client identity, not
bodies or keys. Metrics use route patterns, not IDs. Configured secrets are
redacted. **Gap:** no automated scan of a full e2e log stream (the phase 12 test
exists but has not been run).

### Denial of service

Request timeouts, body and image limits, bounded job concurrency, and a retry
ledger. **Gap:** no load or soak test, and no circuit breaker on the embedding
provider.

### Erasure and retention failures

Retention deletes objects before rows, keeps flagged images longer, and leaves
the scan for audit. Erasure stops if an object delete fails. **Gap:** the sweep
runs on a schedule with no alert if it stops running.

## 4. Open risks that need a decision or an owner

1. Provider data handling: whether images and questions may be retained or used
   for training, and where they are processed. Needs legal and a DPA.
2. Webhook secret encryption at rest. Needs a key-management decision.
3. Backup encryption, restore drill, and who can access a backup.
4. On-call ownership and alert routing. No one currently owns a page.
5. Independent penetration test or structured internal test before a pilot.
6. Terms of use for integrators, including prohibited automated probing.
7. Whether ordinary (unflagged) verdict records should expire; currently kept.

## 5. Review

This document is a starting point, not a sign-off. The phase 12 checklist keeps
the independent test and the legal review open until they are actually done.
