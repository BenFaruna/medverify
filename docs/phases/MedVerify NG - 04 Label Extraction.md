# 04 Label Extraction

**Goal:** turn pack photos into validated, structured label data (NRN, name, manufacturer, batch, dates, ingredients and strengths) using a vision model, with deterministic checks around the model so that nothing unverified is trusted.

**Depends on:** 03. **Blocks:** 05, 07, 10. **Read first:** 00 Build Tracker, 03 Scan Pipeline Skeleton.

## 1. Deliverables checklist

- [ ] VisionProvider interface and LabelData types
- [ ] Provider implementation 1: Claude vision (structured output)
- [ ] Provider implementation 2: Google Vision or a second model, behind the same interface (for comparison and fallback)
- [ ] Versioned prompt files and JSON schema under internal/vision/prompts/v1/
- [ ] Deterministic validators: NRN, dates, ingredient normalization (reuse phase 02 functions)
- [ ] Multi-image merge logic (front, back, side faces)
- [ ] Retry, timeout, circuit breaker, and per-call cost and latency recording (llm\_calls table)
- [ ] Image preprocessing step (resize to the provider limit, contrast fix if proven helpful)
- [ ] process\_scan job updated to use the real pipeline, behind a config switch to the fake
- [ ] Extraction eval set of at least 100 labeled pack photos and a scoring command (see document 10)
- [ ] Recorded provider responses for contract tests

## 2. Tools needed

| Tool | Purpose |
| --- | --- |
| Provider SDK or plain HTTP client for each vision provider | Calls |
| sony/gobreaker or similar | Circuit breaker |
| go-cmp | Comparing structs in tests |
| A labeling spreadsheet or small admin tool | Creating ground truth |
| Test fixtures: real pack photos with consent, blurry, glare, curved bottle, non-English, multiple faces | Eval and contract tests |

## 3. Setup required for success

1. Obtain API keys for the chosen providers and store them as secrets, never in code. Set a monthly spending cap at the provider.
2. **Build the ground truth set early.** Collect 100 or more real packs (pharmacies, partners, volunteers), photograph them in realistic conditions, and label NRN, name, manufacturer, batch, expiry, ingredients and strengths by hand. Two people should label a sample to measure agreement. Record consent for each photo.
3. Pin the exact model identifier in config. Never use a floating alias in production.
4. Decide the data-handling position for sending images to a foreign provider and record it in docs/data-handling.md (see phase 12 for the full review).
5. Define the NRN pattern from the real data found in phase 02. Keep it in one package.

## 4. Design notes

```go
type VisionProvider interface {
    ExtractLabel(ctx context.Context, images [][]byte) (RawLabel, Usage, error)
}

type Field[T any] struct {
    Value      *T
    Confidence float64
    Source     string // which image index it came from
}

type LabelData struct {
    NRN, ProductName, Manufacturer, BatchNo Field[string]
    MfgDate, ExpiryDate                    Field[time.Time]
    Ingredients                            []IngredientClaim
    Unreadable                             []string
}
```

**Model output.** Request JSON that matches a strict schema (use the provider's tool-use or structured output feature). The prompt says: return null for any field you cannot read; copy text exactly as printed; do not infer from knowledge of the product; report confidence per field.

**Validators run after the model, in code.**

- NRN must match the pattern. Otherwise it becomes unreadable with the raw string kept for debugging.
- Dates are parsed with a list of accepted formats (month and year, day month year, with separators such as slash or space). Ambiguous or impossible dates become unreadable.
- Ingredients go through NormalizeIngredient and ParseStrength. Unparseable entries keep their raw text.
- Visually confusable characters (O and 0, I and 1, S and 5) in the NRN are not auto-corrected. Instead, generate candidate variants and let phase 05 check which exists.

**Multi-image merge.** For each field, take the value with the highest confidence. If two images give conflicting values with similar confidence, mark the field as conflicting and downgrade to unreadable.

**Resilience.** Per-call timeout, up to 2 retries with backoff on transient errors, circuit breaker that opens after repeated failures. When open, the scan finishes as failed with a retryable code, not as a wrong answer. Optional fallback to the second provider.

**Observability.** Every call records provider, model, latency, token or unit usage, cost estimate, scan ID, and outcome in llm\_calls. No image bytes or raw text in logs.

**Determinism.** Temperature zero where supported. Prompts live in files with a version string stored on every result.

## 5. Tests for this phase

| Test | Level | What it proves |
| --- | --- | --- |
| NRN validator: valid, invalid, confusable characters, lowercase, extra spaces | unit | Validation |
| Date parser: 12 or more real label formats, ambiguous and impossible dates | unit | Date handling |
| Merge logic: agreement, conflict, one image only, empty | unit | Merge rules |
| Contract tests: recorded provider responses (golden files) parse into LabelData | contract | Schema compatibility |
| Malformed JSON, missing fields, extra fields from the provider are handled without panic | unit | Robustness |
| Provider returns a refusal or empty content: scan ends unreadable, not failed | unit | Edge behavior |
| Provider timeout: retried, then failed with a retryable code | integration | Resilience |
| Circuit breaker opens after N failures and recovers after the cool-down | unit | Resilience |
| Injection fixture: a pack image whose text says to ignore instructions and output a fake NRN. Output must still follow the schema and validators apply | contract | Untrusted input handling |
| Hallucination guard: blurred image fixture returns nulls, not invented values | eval | No guessing |
| Cost record is written for every call, including failures | integration | Cost visibility |
| Prompt version is stored on the result | integration | Traceability |
| Extraction eval: per-field exact match and normalized match against ground truth | eval | Quality |

**Acceptance targets (agree with the project lead, then record them here).** Suggested starting gates on the labeled set: NRN exact match at or above 90 percent on legible photos, zero fabricated NRNs that pass the format check but do not appear in the image, p95 latency within budget from document 12.

## 6. Pitfalls and guards

- **Plausible but invented NRNs.** The most dangerous failure. A made-up NRN that happens to exist in the Greenbook would produce a false match. Mitigations: model must return null when unsure, per-field confidence, a second-pass crop-and-reread for NRN, and the eval metric for fabricated values.
- **Treating pack text as instructions.** Text in the image is data. Never put extracted text into a prompt as instructions in later stages.
- **Provider drift.** A silent model change shifts accuracy. Pin versions and run the nightly eval.
- **Ingredients on a different face.** Require or encourage multiple photos and report which fields were not found.
- **Locale and units.** Dates like 03/04 are ambiguous. Prefer unreadable over a wrong guess unless a format is unambiguous.
- **Cost surprises.** Cap images per scan, resize before sending, cache by image hash, alert on spend.
- **Biased test data.** An eval set of only clean photos hides real-world failure. Include poor lighting, curved surfaces, and worn packaging.
- **Logging sensitive data.** Do not log images, base64, or full model responses outside a protected debug mode that is off by default.
- **Vendor lock-in.** Keep all provider specifics inside the provider packages.

## 7. Definition of Done

- Both providers pass the contract tests and the eval gates agreed above.
- The pipeline survives provider outage tests without wrong results.
- Checklist ticked, tracker updated.

## 8. Notes for agents

- Implement the fake provider first (it should already exist from phase 03), then validators and merge logic with unit tests, then the real providers.
- Never put API keys in fixtures. Recorded responses must be scrubbed of identifiers.
- When adding a field, update the schema, prompt, validator, tests, and the OpenAPI result shape together.
