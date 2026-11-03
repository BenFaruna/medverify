# 07 Verdict Engine

**Goal:** combine registry matching, safety flags, and extraction quality into one explainable verdict with a score, reasons, and a confidence level, using transparent weighted rules that can later be compared with a trained model.

**Depends on:** 05, 06. **Blocks:** 09, 11. **Read first:** 00 Build Tracker, 05 Registry Verification, 06 Ingredient Safety Checks.

## 1. Verdicts

These replace the placeholder verdict from phase 03. Names are chosen so that none of them accuses or reassures beyond what we know.

| Verdict | Meaning | Typical inputs |
| --- | --- | --- |
| consistent\_with\_records | Registered product, label agrees, no flags | matched\_consistent, no flags |
| mismatch\_found | Registered product but the label disagrees on one or more fields | matched\_with\_mismatch |
| not\_found\_in\_records | Nothing matches in our dataset (this is not proof of a fake) | not\_found\_in\_dataset with a readable label |
| flagged | A watchlist or registration-problem flag applies | any safety flag of high severity |
| needs\_review | Candidates only, ambiguity, or conflicting signals | candidate\_only, ambiguous |
| unreadable | Not enough readable data to judge | low extraction confidence |

No verdict is called safe, genuine, authentic, fake, or counterfeit. If the API enum in OpenAPI differs, update it here and there together.

## 2. Deliverables checklist

- [x] Signals struct: the normalized inputs (match outcome, per-field outcomes, flags, extraction confidences)
- [x] Rules file (YAML) with weights, thresholds, and a version string
- [x] Pure function Evaluate(signals, rules) returning Verdict, Score, Confidence, Reasons
- [ ] Reason catalog: codes, parameters, and the wording per language (reviewed text, not generated) — codes and English catalog are in place; the pharmacist review has not happened
- [x] Persistence: full signals, rules version, and output stored in scan\_results
- [x] Short-circuit rules (for example unreadable input skips scoring)
- [x] Scenario test suite (golden cases) and property tests
- [x] Documentation of the scoring philosophy in docs/scoring.md
- [x] A replay command: re-run the verdict for stored scans with a different rules version and show differences

## 3. Tools needed

| Tool | Purpose |
| --- | --- |
| gopkg.in/yaml.v3 | Rules file parsing |
| rapid | Property tests |
| go-cmp and golden files | Scenario snapshots |
| testcontainers-go | Persistence tests |

## 4. Setup required for success

1. A written scoring policy agreed with the project lead and a pharmacist: what each verdict means and which behaviors we want users to take from each.
2. A table of 40 or more scenarios (inputs and the expected verdict) written by humans before the weights are tuned. These are the specification.
3. Rules in a file with a version, loaded at startup and validated (weights in range, thresholds ordered).
4. A forbidden words list used by tests: safe, genuine, authentic, fake, counterfeit, harmless, dangerous.

## 5. Design notes

**Pure and deterministic.** Evaluate has no I/O, no clock, no randomness. The same signals and rules always give the same output. This is what makes it testable and auditable.

**Order of evaluation.**

1. Gate: if extraction confidence is below the gate, return unreadable with the missing fields listed.
2. Hard rules: a high severity watchlist flag leads to flagged regardless of other signals.
3. Registry outcome sets the base verdict (see the table above).
4. Weighted adjustments (field mismatches, expired label date, registration status) can move a verdict between consistent\_with\_records and mismatch\_found or needs\_review.
5. Confidence combines extraction confidence and match evidence strength. It is shown as low, medium, or high, never as a precise percentage.

**Score.** An integer from 0 to 100 where higher means more concern. It exists for sorting and analytics. The verdict is what users see.

**Reasons.** Every verdict has one or more reasons, each a code plus parameters (for example strength\_mismatch with printed and registered values). Reasons are ordered by importance and capped for display.

**Future ML.** Every scan stores the complete signals vector and, once available, a human or lab confirmed label. Do not build a model in this phase.

## 6. Tests for this phase

| Test | Level | What it proves |
| --- | --- | --- |
| Scenario suite: every row of the human-written table yields the expected verdict | unit | The specification |
| Determinism: 1,000 random signal sets evaluated twice give identical output | property | No hidden state |
| Monotonicity: adding a concern (a mismatch or flag) never lowers the score | property | Sensible weighting |
| Hard rule: any high severity watchlist flag yields flagged | unit | Priority rules |
| Gate: low extraction confidence yields unreadable, never a guess | unit | Safety |
| Unknown field outcomes never reduce confidence below the gate by themselves in an otherwise strong match | unit | Fair treatment of partial data |
| Every reason code in any output exists in the catalog for all languages | unit | Completeness |
| No output string or catalog text contains a forbidden word | unit | Wording rule |
| Rules file validation rejects out-of-range weights and unordered thresholds | unit | Config safety |
| Rules version is stored with every result | integration | Auditability |
| Replay of 100 stored scans under a new rules version produces a readable diff | integration | Change management |
| Boundary tests at each threshold (one below, equal, one above) | unit | Edge handling |

**Acceptance test.** Run the full eval set (phase 10) through the pipeline end to end. Produce a confusion table of verdicts versus ground truth (where lab or expert truth exists). Review every case where the verdict is consistent\_with\_records but ground truth says otherwise. Each such case must lead to a rule change or a documented limitation.

## 7. Pitfalls and guards

- **False reassurance.** The costliest error is consistent\_with\_records on a bad product. Bias rules toward needs\_review when evidence is thin.
- **Alert fatigue.** Too many flagged or needs\_review outcomes teach users to ignore them. Track the verdict distribution and review it regularly.
- **Silent tuning.** Weights changed without a version bump make results impossible to audit. Every change bumps the version and runs the scenario suite.
- **Overfitting weights to the eval set.** Keep a held-out set that is not used for tuning.
- **Presenting score as probability.** A score of 70 is not a 70 percent chance of anything. Never display it as one.
- **Hidden dependencies.** If Evaluate calls the database or the clock, tests become flaky. Keep it pure.
- **Language drift.** Adding a reason without catalog text in every supported language ships a broken experience. The completeness test prevents this.

## 8. Definition of Done

- Scenario table and tests are signed off by the project lead and a pharmacist.
- Held-out evaluation reviewed. Rules version 1 tagged.
- Checklist ticked, tracker updated.

## 9. Notes for agents

- Write the scenario table tests first, then implement.
- Do not add new verdict values without updating this document, the OpenAPI enum, the assistant policy (phase 09), and the catalog.
- Do not use an LLM anywhere in this phase.

## 10. Implementation notes

- **Package layout.** `internal/verdict` holds the closed verdict set
  (`types.go`), the normalized input (`signals.go`), the versioned policy and
  its validation (`rules.go`), the evaluator (`evaluate.go`), and the reason
  catalog loader (`reasons.go`). Nothing in the package touches a database, a
  clock, or a network connection.
- **Verdicts.** The closed set is `consistent_with_records`, `mismatch_found`,
  `not_found_in_records`, `flagged`, `needs_review`, and `unreadable`. Each has
  a doc comment stating what it does not claim; none of them says a medicine is
  safe, genuine, authentic, fake, or counterfeit. Adding a value requires a
  rules band, an OpenAPI enum entry, and a change to the phase 09 assistant
  policy.
- **Rules file.** `internal/verdict/config/verdict-v1.yaml` is embedded and
  versioned. `VERDICT_CONFIG_FILE` overrides it; `config.Load` exposes it as
  `cfg.Verdict.ConfigFile` and the worker logs `verdict_config_version` at
  startup. Unknown keys are rejected, bands must be closed, ordered, and inside
  0-100, weights are range-checked, and the confidence thresholds must be
  ordered. Invalid configuration fails startup rather than degrading a scan.
- **Migration.** `00007_verdict.sql` drops the unused `scan_results.verdict`
  jsonb column and adds `verdict_code`, `verdict_score`, `verdict_confidence`,
  `verdict_rules_version`, and `signals`. A CHECK constraint allows either all
  five to be null (not yet scored) or all five to be present, so a half-written
  verdict cannot be stored. Codes and confidence values are constrained by
  CHECK, score is constrained to 0-100, and two indexes support replay and
  analytics by version and recency. The down migration restores the placeholder
  column.
- **Store and API.** `scan.Store.FinishScan` writes the typed verdict and the
  exact signals in the same upsert as the rest of the result; it refuses a
  verdict without signals. `Store.ListVerdictReplay` reads stored rows only.
  `scan.View` marshals the typed verdict, and the OpenAPI schema replaces the
  opaque `verdict` object with an explicit `Verdict` plus `VerdictReason`
  (code, field, message, params) and documents the score as internal.
- **Evaluation wiring.** `scan.Service` takes an optional `Rules` pointer. When
  it is set, completed scans evaluate `SignalsFrom(match, flags, label)` and
  unreadable scans (no readable image, unidentified label, or low confidence)
  evaluate `SignalsFromUnreadable`, so even a photo-only failure has an
  auditable verdict. When it is nil no verdict is computed, which keeps earlier
  phases' behavior and tests unchanged.
- **Reasons.** Output carries stable codes and parameters only; the reviewed
  sentence lives in `reasons/en.yaml` and is rendered server-side. Reasons are
  deduplicated, sorted by importance, and capped by `max_reasons` (8). A test
  asserts every emitted code has a template and that no rendered sentence or
  catalog entry contains the forbidden words. Only `en` is supported.
- **Scenario table.** `testdata/scenarios.yaml` holds 50 cases covering every
  verdict, written before the weights were selected. A loader test requires
  every verdict to appear and the scenario tests assert each expected outcome,
  score band, confidence, and reason code. It is marked a draft specification
  pending review.
- **Replay.** `medverify-admin verdict replay --rules <file> [--limit N]`
  re-evaluates stored signals under a candidate rules file and prints changed
  scans (stored vs replayed value/confidence/score and which changed) plus a
  per-verdict count. It writes nothing and is a dry run by design.
- **Tests.** Unit tests cover the gate, hard rule, band mapping, confidence,
  unknown-field handling, every reason code, rules validation, wording, and
  boundary values. Property tests cover determinism (1,000 randomized signal
  sets evaluated twice) and monotonicity (adding a concern never lowers the
  score). Integration tests cover typed persistence, the stored signals,
  rules-version auditing, and replay under a stricter rules file.
- **Not done.** No LLM is used anywhere in this phase, and no machine-learned
  model was built. The `signals` column is the training input for a future
  model once labels exist; nothing consumes it yet.

## 11. Blockers and open items

All four external blockers remain open. None is resolved by this phase's code:

1. **Consented, double-labelled 100+ photo eval set.** The extraction harness
   is ready; the dataset is absent, so no held-out evaluation of the weights
   exists.
2. **Live provider acceptance run.** Needs `VISION_API_KEY`, pinned model ids,
   a provider spending cap, and data-handling approval.
3. **Data-handling decision** for sending pack photos to a foreign provider.
4. **NAFDAC data-sharing agreement** from phase 02.

Open items specific to this phase:

- The verdict meanings, the 50-case scenario table, the score bands, and the
  weights are a proposed engineering policy. The project lead and a pharmacist
  must sign them off before release; no test treats them as clinically
  approved.
- `internal/verdict/reasons/en.yaml` is draft wording. It must not be presented
  as approved clinical communication until a pharmacist reviews it.
- Only English reason text exists. Any second language needs a complete catalog,
  or the wording-completeness test fails.
- The weights were chosen from the scenario table, not from real distribution
  data. Alert fatigue and false reassurance have not been measured against a
  pilot.
- The held-out eval set is unavailable, so overfitting of the weights cannot be
  ruled out yet.
- The phase 09 assistant policy must mirror these verdicts and the same
  forbidden wording; it does not exist yet.
- `docs/scoring-philosophy.md` was written in phase 1 before the internal score
  existed. `docs/scoring.md` now documents the implemented behavior; the two
  should be merged when the policy is signed off.
