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

- [ ] Signals struct: the normalized inputs (match outcome, per-field outcomes, flags, extraction confidences)
- [ ] Rules file (YAML) with weights, thresholds, and a version string
- [ ] Pure function Evaluate(signals, rules) returning Verdict, Score, Confidence, Reasons
- [ ] Reason catalog: codes, parameters, and the wording per language (reviewed text, not generated)
- [ ] Persistence: full signals, rules version, and output stored in scan\_results
- [ ] Short-circuit rules (for example unreadable input skips scoring)
- [ ] Scenario test suite (golden cases) and property tests
- [ ] Documentation of the scoring philosophy in docs/scoring.md
- [ ] A replay command: re-run the verdict for stored scans with a different rules version and show differences

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
