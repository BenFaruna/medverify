# 05 Registry Verification

**Goal:** take validated label data and decide, using the local Greenbook copy, whether the pack's claims match a registered product, producing a typed, explainable match result.

**Depends on:** 02, 04. **Blocks:** 06, 07. **Read first:** 00 Build Tracker, 02 Greenbook Ingestion, 04 Label Extraction.

## 1. Deliverables checklist

- [x] MatchResult type with per-field comparison outcomes
- [x] Lookup strategy: exact NRN, then NRN variant candidates, then trigram name plus manufacturer
- [x] Name normalization for comparison (separate from storage normalization)
- [x] Field comparers: name, manufacturer, ingredients, strengths, expiry, registry status
- [x] Configurable thresholds (trigram similarity, strength tolerance) in a versioned config file
- [x] Ambiguity handling: multiple candidates returned, never silently picking one
- [x] A dataset-coverage concept: a distinct not\_found\_in\_dataset outcome
- [x] Integration with process\_scan: result stored in scan\_results.features
- [x] Benchmark for lookup latency

## 2. Tools needed

| Tool | Purpose |
| --- | --- |
| PostgreSQL pg\_trgm (similarity, word\_similarity) | Fuzzy matching |
| testcontainers-go with a seeded database | Integration tests |
| go-cmp | Comparing result structs |
| rapid | Property tests |
| Go benchmarks (testing.B) | Latency checks |

## 3. Setup required for success

1. **Seeded dataset — done, with a caveat.** The committed phase 02 fixtures hold
   198 distinct real products; two rows share an NRN and are skipped exactly as
   the live sync skips them, leaving 196. Six clearly synthetic rows add the
   cases the public slice does not contain: a unique name for `candidate_only`,
   a near-identical pair for `ambiguous`, a withdrawn registration, an active
   but lapsed registration, and a confusable NRN. See section 10.
2. **Matching rules — implemented, sign-off open.** Names are normalized
   (case, punctuation, dosage form and pack words) and compared by trigram
   similarity; manufacturer and applicant are both compared with company
   suffixes stripped; strengths are compared after unit conversion. A
   pharmacist has not yet signed these off, so v1 is an engineering default.
3. **Minimum evidence — recorded.** `matched_consistent` and
   `matched_with_mismatch` require an exact NRN hit. `candidate_only` requires a
   name (and manufacturer, when printed) above the configured thresholds, and
   never carries a `Product`. `ambiguous` requires more than one candidate
   within the ambiguity margin. `not_found_in_dataset` requires that neither an
   NRN nor a name lookup found anything, and always carries dataset coverage.
   `unreadable_input` requires nothing to look up. No outcome is a safety or
   authenticity claim.

## 4. Outcomes and evidence

| Outcome | Meaning | Minimum evidence |
| --- | --- | --- |
| matched\_consistent | NRN found and printed fields agree with the record | NRN match, no material field disagreement |
| matched\_with\_mismatch | NRN found but one or more fields disagree | NRN match, at least one disagreement (list them) |
| candidate\_only | NRN unreadable, fuzzy name match found | Name and manufacturer similarity above threshold. Lower confidence |
| ambiguous | More than one plausible product | Return up to 5 candidates, do not choose |
| not\_found\_in\_dataset | No NRN or name match in our data | Do not say fake. Our data may be incomplete |
| unreadable\_input | Not enough label data to search | Ask for a better photo |

## 5. Design notes

```go
type FieldOutcome string // agree, disagree, unknown

type MatchResult struct {
    Outcome     Outcome
    Product     *ProductRef       // nil unless matched
    Candidates  []ProductRef      // for candidate_only and ambiguous
    Fields      map[string]FieldComparison
    RegistryStatus string         // active, expired, withdrawn, as stored
    Notes       []string          // human-readable reasons, with codes
}
```

**Lookup order.**

1. If the NRN is valid, exact lookup.
2. If the NRN is unreadable or missing, and the confusable-character variants exist (see phase 04), query those variants exactly. A variant hit is flagged variant\_match, with lower confidence.
3. Otherwise trigram search on name, filtered or boosted by manufacturer, with a minimum similarity from config.

**Comparison rules.**

- Names: lowercase, strip dosage form words and pack words (tablets, capsules, syrup), strip punctuation, then compare by similarity above a threshold. Brand names that differ completely are a disagreement.
- Manufacturer: normalized company names (strip Ltd, Limited, Plc, Pharmaceuticals when equivalent). Compare with similarity, and treat applicant versus manufacturer carefully because the label may show either.
- Ingredients: compare as sets of normalized names. Report missing, extra, and strength differences separately.
- Strengths: compare after unit conversion (g, mg, mcg) within the configured tolerance.
- Expiry: if the printed expiry is in the past, record expired\_on\_label. Also record whether the registration itself has expired or been withdrawn.
- Unknown is not disagree. If a printed field is unreadable, its outcome is unknown and never counts against the product.

**Explainability.** Every disagreement carries a stable reason code (for example name\_mismatch, strength\_mismatch, extra\_ingredient) so the verdict engine and the UI can display it in any language.

## 6. Tests for this phase

| Test | Level | What it proves |
| --- | --- | --- |
| Exact NRN with all fields agreeing returns matched\_consistent | integration | Happy path |
| Exact NRN with a different strength returns matched\_with\_mismatch and the right reason code | integration | Mismatch detection |
| Unreadable printed field yields unknown, not disagree | unit | Unknown handling |
| NRN not in dataset and name not in dataset returns not\_found\_in\_dataset | integration | Honest not-found |
| NRN missing, correct name and manufacturer returns candidate\_only | integration | Fallback |
| Two products with near-identical names returns ambiguous with candidates | integration | No silent guess |
| Confusable variant (O versus 0) finds the right product and is flagged variant\_match | integration | OCR error tolerance |
| Withdrawn and expired registrations are reported with the right status | integration | Status handling |
| Printed expiry in the past is flagged | unit | Date logic |
| Name normalization table tests (dosage words, case, punctuation, Ltd and Limited) | unit | Normalizer |
| Strength comparison across units (0.5 g versus 500 mg) | unit | Unit conversion |
| Property: comparing a product record with itself always yields all agree | unit | Consistency |
| Threshold changes in config change outcomes as expected, without code changes | unit | Config driven |
| Benchmark: exact lookup under 50 ms, fuzzy lookup under 200 ms at full dataset size | benchmark | Performance |

**Acceptance test.** Run the extraction eval set (phase 04) through registry verification. Report the outcome distribution and review every matched\_consistent and every not\_found\_in\_dataset result by hand for a sample of 50.

## 7. Pitfalls and guards

- **Calling not found fake.** The dataset can be incomplete or stale. The outcome name and every message must reflect that.
- **Over-eager fuzzy matching.** A loose threshold turns a counterfeit with a copied name into a match. Favor candidate\_only and ambiguous over confident matches, and tune with real data.
- **Treating unknown as disagreement.** This would flag genuine products because of a bad photo.
- **Brand versus generic naming.** The same product may carry a brand name on the front and generic name on the side. Compare against both name fields when available.
- **Strength format traps.** 500 mg, 0.5 g, 500mg per tablet, and 100 mg per 5 ml are different shapes. Compare normalized values only.
- **Multiple presentations per NRN family.** Different pack sizes and strengths have different NRNs. Never match by name alone when an NRN is present and disagrees.
- **Hidden thresholds.** Keep every tunable in config with a version, and store the config version on each result.
- **Case and whitespace in SQL.** Normalize on the way in and use indexed expressions.

## 8. Definition of Done

- All outcome types tested and demonstrated on the seeded data and the eval set.
- Reviewed sample of 50 results signed off by the project lead or a pharmacist.
- Benchmarks within targets. Checklist ticked, tracker updated.

## 9. Notes for agents

- Keep comparison logic as pure functions that take two plain structs. The database layer only fetches candidates.
- Never add a new outcome value without updating this document, the verdict engine mapping, and the OpenAPI enum.
- Do not tune thresholds to make a single test pass. Add a test case that describes the real situation.

## 10. Implementation notes

- **Package seam.** `internal/verify` holds pure comparison functions over
  `registry.Product` and `vision.LabelData`. The `Catalog` interface only
  fetches candidates, and `registry.Store` is its only implementation; the
  integration test asserts that with a compile-time check.
- **Thresholds.** `internal/verify/config/v1.yaml` is embedded and versioned
  `v1`. Unknown keys and out-of-range values are rejected, so a typo cannot
  silently leave a threshold at zero. `VERIFY_CONFIG_FILE` can point at an
  override for a preview; the version actually used is stored in
  `dataset.config_version` on every result.
- **Lookup order.** Exact NRN, then the phase 04 confusable variants, then a
  trigram name search with manufacturer filtering or boosting. A variant hit
  returns `candidate_only` with `variant_match: true`, never a match. A name
  search whose best two candidates are within `ambiguity_margin` returns
  `ambiguous` with up to five candidates.
- **NRN variants.** `NRNPattern` allows a one- or two-letter prefix, so an OCR
  reading of `O9-2006` is still a valid NRN and its `0`-for-`O` variant
  `09-2006` is looked up. A letter substituted inside the numeric part fails
  the shape check and is dropped as unreadable before verification, rather than
  guessed.
- **Name and manufacturer rules.** Names are NFKC-normalized, lowercased, and
  stripped of punctuation plus dosage form and pack words (tablet, capsule,
  syrup, bottle, BP, …) before trigram similarity. Manufacturer and applicant
  are both compared because a label may print either; company suffixes (Ltd,
  Limited, Plc, Pharmaceuticals, …) are stripped. Identical strings score 1.
- **Strengths.** Compared after unit conversion (mcg, mg, g, kg; mL, L; IU,
  unit, mEq, mmol, %), including per-volume denominators, so `100 mg/5 mL` and
  `20 mg/mL` agree. An unparseable or differently shaped strength yields
  `unknown`, never a disagreement.
- **Unknown is not disagree.** A field the provider could not read yields
  `unknown` and does not affect the outcome. `matched_consistent` therefore
  means "nothing readable disagreed", not "everything was read".
- **Expiry.** The printed expiry is compared with the scan time, not the
  registry. A month-precision pack marked 01/2027 is usable through 31 January
  2027. An expired label produces `matched_with_mismatch` with
  `expired_on_label`; an expired or withdrawn registration adds its own reason.
- **Storage.** Migration `00005` adds `scan_results.features jsonb NOT NULL
  DEFAULT '{}'`. The worker runs the matcher after extraction, the scan view
  exposes it as `features`, and the OpenAPI `RegistryVerification` schema pins
  the outcome enum. A matcher failure fails the scan; a scan without a matcher
  stores `{}`.
- **Seeded dataset.** The integration suite loads every committed Greenbook
  fixture page through the real parser and store: 198 distinct products, of
  which two share an NRN and are skipped the way the live sync skips them,
  leaving 196 real rows. Six synthetic rows (`synthetic-*`, NRNs `09-2001` to
  `09-2006`) add the tricky cases. Synthetic rows are clearly marked and never
  presented as real registrations.
- **Benchmarks** (this machine, 200 iterations, seeded database): exact NRN
  0.47 ms, fuzzy name plus manufacturer 1.71 ms. Targets are 50 ms and 200 ms.
  Run with `TESTCONTAINERS_RYUK_DISABLED=true go test -tags=integration
  -bench . ./internal/verify`.
- **Tests.** Unit tests cover config validation, normalization tables, strength
  units, unknown handling, expiry, threshold changes, and a rapid property that
  a record compared with itself never disagrees. Integration tests cover all
  nine outcome scenarios and prove the scan pipeline stores the result. Every
  integration package starts its own Postgres.
- **Scope kept out.** `cmd/eval` does not yet run registry verification over
  the extraction eval set. That acceptance step is blocked by the missing
  dataset (section 11), and adding an unexercised path would not prove
  anything.

## 11. Blockers and open items

All four external blockers remain open. None is resolved by this phase's code:

1. **Consented, double-labelled 100+ photo eval set.** The extraction harness
   is ready; the dataset is absent. The phase 05 acceptance run — outcome
   distribution over the eval set plus a hand review of 50
   `matched_consistent` and `not_found_in_dataset` results — is blocked by it.
2. **Live provider acceptance run.** Needs `VISION_API_KEY`, pinned model ids,
   a provider spending cap, and data-handling approval.
3. **Data-handling decision** for sending pack photos to a foreign provider.
4. **NAFDAC data-sharing agreement** from phase 02.

Open items specific to this phase:

- Pharmacist sign-off of the matching rules and thresholds (section 3.2) has
  not happened; v1 is an engineering default, not a clinical decision.
- The 50-result review required by the Definition of Done is blocked by item 1.
- A live Greenbook refresh is blocked by item 4, so this phase is demonstrated
  only on the committed fixture slice.
