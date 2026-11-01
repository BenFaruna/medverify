# 05 Registry Verification

**Goal:** take validated label data and decide, using the local Greenbook copy, whether the pack's claims match a registered product, producing a typed, explainable match result.

**Depends on:** 02, 04. **Blocks:** 06, 07. **Read first:** 00 Build Tracker, 02 Greenbook Ingestion, 04 Label Extraction.

## 1. Deliverables checklist

- [ ] MatchResult type with per-field comparison outcomes
- [ ] Lookup strategy: exact NRN, then NRN variant candidates, then trigram name plus manufacturer
- [ ] Name normalization for comparison (separate from storage normalization)
- [ ] Field comparers: name, manufacturer, ingredients, strengths, expiry, registry status
- [ ] Configurable thresholds (trigram similarity, strength tolerance) in a versioned config file
- [ ] Ambiguity handling: multiple candidates returned, never silently picking one
- [ ] A dataset-coverage concept: a distinct not\_found\_in\_dataset outcome
- [ ] Integration with process\_scan: result stored in scan\_results.features
- [ ] Benchmark for lookup latency

## 2. Tools needed

| Tool | Purpose |
| --- | --- |
| PostgreSQL pg\_trgm (similarity, word\_similarity) | Fuzzy matching |
| testcontainers-go with a seeded database | Integration tests |
| go-cmp | Comparing result structs |
| rapid | Property tests |
| Go benchmarks (testing.B) | Latency checks |

## 3. Setup required for success

1. A seeded test dataset: 100 to 200 real products exported from the phase 02 sync, committed as a SQL or JSON fixture (public data only), including deliberately tricky cases: near-duplicate names, same name from different manufacturers, withdrawn, expired.
2. Written matching rules agreed with a pharmacist: what counts as the same product name (brand versus generic wording), and how to treat strength differences.
3. A decision, recorded in this document, on the minimum evidence needed for each outcome (see the table below).

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
