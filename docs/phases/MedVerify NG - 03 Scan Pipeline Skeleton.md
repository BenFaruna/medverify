# 03 Scan Pipeline Skeleton

**Goal:** upload a pack photo through the API, store it safely, run an asynchronous job using a fake vision provider, and return a result. The whole flow works end to end before any real AI is attached.

**Depends on:** 01. **Blocks:** 04, 07, 11. **Read first:** 00 Build Tracker, 01 Foundations.

## 1. Deliverables checklist

- [ ] Migrations: api\_clients (stub), scans, scan\_images, scan\_results, idempotency\_keys
- [ ] POST /v1/scans: multipart upload of 1 to 4 images, optional coarse location, optional language
- [ ] GET /v1/scans/{id}: status and result
- [ ] Image validation, orientation fix, metadata stripping, re-encoding
- [ ] Object storage client (S3 compatible) with random keys
- [ ] Scan state machine: queued, processing, completed, unreadable, failed
- [ ] River job process\_scan that calls the vision interface (fake for now)
- [ ] Sync-with-timeout behavior: wait up to a configured time for completion, otherwise return 202 and let the client poll
- [ ] Idempotency-Key header support
- [ ] Placeholder API key middleware (full version in phase 11)
- [ ] OpenAPI entries for both endpoints

## 2. Tools needed

| Tool | Purpose |
| --- | --- |
| MinIO (local) and an S3-compatible client library | Image storage |
| Go image, image/jpeg, image/png, golang.org/x/image/webp | Decoding and re-encoding |
| An imaging library with auto-orientation support (for example disintegration/imaging) | Rotate by EXIF orientation before metadata is dropped |
| River | Async processing |
| httptest and multipart writer | Upload tests |
| Fixture images: normal photo, photo with GPS EXIF, rotated photo, huge dimension image, corrupt file, renamed non-image | Tests |

## 3. Setup required for success

1. Define limits in config: max files per scan, max bytes per file, max pixel dimensions, allowed formats (JPEG, PNG, WebP), sync wait timeout, storage bucket name.
2. Create the bucket with private access only. Never serve stored images by public URL.
3. Create the fixtures listed above under testdata/images/. The GPS fixture must contain real GPS EXIF fields so the strip test is meaningful.
4. Agree the status values and the problem-response codes (for example image\_too\_large, unsupported\_format, too\_many\_images) and add them to openapi.yaml before coding.

## 4. Design notes

**Upload handling order (do not reorder).**

1. Enforce request size limit before parsing.
2. For each file: read at most the byte limit, sniff the content type from the bytes (not the header or filename), reject if not allowed.
3. Call DecodeConfig first to read dimensions without decoding pixels. Reject if over the pixel limit (protects against decompression bombs).
4. Decode, apply orientation, then re-encode to a clean JPEG. This removes EXIF, GPS, and embedded thumbnails.
5. Store under a random key such as scans/{scan\_id}/{uuid}.jpg. Never use the client filename.
6. Insert scan and image rows in one transaction, then enqueue the job.

**Location.** Accept an optional latitude and longitude, immediately convert to a coarse grid cell identifier, and store only the cell. Do not store or log the raw coordinates.

**State machine.** Only these transitions are allowed: queued to processing, processing to completed, unreadable, or failed, and failed back to queued on retry. Enforce with a database CHECK or a guarded update (UPDATE ... WHERE status = expected).

**Sync wait.** After enqueueing, the handler listens for completion (Postgres LISTEN/NOTIFY or short polling of the row) up to the configured timeout. On timeout return 202 with a Location header for polling. The client must handle both.

**Idempotency.** Store the key, a hash of the request body, and the created scan ID. Same key and same body returns the original response. Same key and different body returns 422.

**Result shape (placeholder).** status, verdict (null until phase 07), reasons (empty), extracted (null until phase 04), created\_at, completed\_at.

## 5. Tests for this phase

| Test | Level | What it proves |
| --- | --- | --- |
| Valid single image upload returns 201 or 202 with a scan ID | integration | Happy path |
| 5 images when the limit is 4 returns the documented error | integration | Limits |
| Oversized file is rejected without storing anything | integration | Limits |
| Text file renamed to .jpg is rejected (content sniffing) | unit | Type safety |
| Image with huge pixel dimensions is rejected before full decode | unit | Decompression bomb guard |
| Corrupt JPEG returns a clean 4xx, not a 500 | unit | Robustness |
| GPS fixture: stored image contains no EXIF or GPS tags (read it back and assert) | unit | Privacy |
| Rotated fixture is stored upright | unit | Orientation handling |
| Stored key does not contain the client filename | unit | Key hygiene |
| Raw coordinates are absent from the database and logs; only the grid cell is stored | integration | Privacy |
| Job runs with the fake provider and the scan reaches completed | integration | Pipeline wiring |
| Fake provider returning an error leads to failed, then retry succeeds | integration | Retry behavior |
| Illegal state transition is rejected | unit | State machine |
| Same Idempotency-Key and body returns the same scan; different body returns 422 | integration | Idempotency |
| Client disconnect during the sync wait does not cancel the background job | integration | Decoupling |
| Concurrent identical requests produce one scan | integration | Race safety |
| Missing or invalid API key returns 401 | unit | Auth stub |

**Acceptance test.** Use curl to upload the fixture photo, observe the 202 or 201, poll GET until completed, and confirm the image exists in MinIO with no EXIF.

## 6. Pitfalls and guards

- **Losing orientation.** If you strip EXIF before applying the orientation tag, phone photos end up sideways and later extraction quality drops. Apply orientation first.
- **Trusting the Content-Type header or file extension.** Always sniff the bytes.
- **Decoding before checking dimensions.** A tiny file can decompress to gigabytes.
- **Reading whole uploads into memory.** Use size-limited readers and stream to storage where possible.
- **Predictable object keys.** Use random identifiers and private buckets.
- **Logging request bodies or filenames.** Log sizes and IDs only.
- **Double-processing.** River can retry jobs. The job must be safe to run twice, so use the guarded state update and make provider results idempotent per scan.
- **Holding HTTP requests open too long.** The sync wait must respect request context and have its own cap.
- **Orphaned files.** If the transaction fails after storage upload, delete the object or run a cleanup job for unreferenced keys.
- **Premature intelligence.** Do not call a real provider in this phase.

## 7. Definition of Done

- The upload to result flow works end to end with the fake provider in integration tests.
- Privacy tests for EXIF and coordinates pass.
- OpenAPI updated and validated.
- Checklist ticked, tracker updated.

## 8. Notes for agents

- Implement validation as small pure functions that take bytes and return typed errors, so each can be unit tested without HTTP.
- Do not store original uploads, only the re-encoded images.
- If a test needs a new fixture image, generate it in code or commit a very small file. Do not commit large photos.
