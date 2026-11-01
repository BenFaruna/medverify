# 10 Evals and Guardrails

**Goal:** measure the quality and safety of every AI-dependent component with versioned datasets, repeatable scoring, and release gates, so that changes to prompts, models, or rules are judged by evidence and not by feel.

**Depends on:** starts with 04 and grows through 09. **Blocks:** release decisions in 12. **Read first:** 00 Build Tracker, 04, 07, 08, 09.

## 1. Deliverables checklist

- [ ] Dataset format and directory layout under /evals, versioned in git
- [ ] cmd/eval runner: loads a dataset, runs the target component, scores, writes a JSON and Markdown report
- [ ] Tables or files for tracking results over time (eval\_runs, eval\_cases)
- [ ] Extraction eval (phase 04)
- [ ] Retrieval eval (phase 08)
- [ ] Assistant evals: answerable, unanswerable, advice and diagnosis, over-refusal, injection, languages (phase 09)
- [ ] End-to-end verdict eval with expert or lab ground truth where available
- [ ] LLM judge, calibrated against human labels, used only where needed
- [ ] Release gates file (thresholds) enforced by the runner with a non-zero exit code
- [ ] Offline recorded mode (replays stored model outputs) for pull requests, live mode for nightly runs
- [ ] Labeling guide and a process for turning production failures into new cases
- [ ] Held-out test split that is never used for tuning

## 2. Tools needed

| Tool | Purpose |
| --- | --- |
| Go runner (cmd/eval) with a plugin per component | Execution |
| JSON Lines dataset files with a schema | Cases |
| A labeling spreadsheet or simple internal tool | Ground truth creation |
| CI scheduler (nightly) with a spending cap | Live runs |
| A dashboard or Markdown trend report | Tracking |

## 3. Setup required for success

1. **Own the data early.** Start labeling extraction photos as soon as phase 03 is done. Labeling is the slowest part of the project.
2. Define a dataset schema with: case ID, component, input, expected output or expected behavior, tags (language, difficulty, category), source, labeler, date, split (dev or test).
3. Write a labeling guide with examples and edge rules, and measure agreement between two labelers on a sample (for example Cohen's kappa) before scaling.
4. Set a nightly spending cap, and record cost per run.
5. Decide who may change thresholds: only the project lead, with a changelog entry.

## 4. Datasets and metrics

| Eval | Contents | Primary metrics |
| --- | --- | --- |
| Extraction | 100+ labeled photos across conditions | Per-field exact match, normalized match, fabrication rate (value not present in the image), unreadable rate |
| Retrieval | 100+ questions labeled with the product and section | Recall at 5, rate of correct no-result on unanswerable |
| Answerable Q and A | Questions with answers in the data | Correctness, citation precision, unsupported-claim rate |
| Unanswerable | Questions the data cannot answer | Correct not-in-records rate (hallucination rate is the complement) |
| Advice and diagnosis | Should I give this to my baby, I have a headache, what dose | Refusal recall, advice leak count |
| Over-refusal | Legitimate lookups, oddly phrased | Refusal precision, over-refusal rate |
| Injection | Hostile pack text, hostile leaflet text, hostile user messages | Attack success rate |
| Languages | The same questions in English, Pidgin, Hausa, Yoruba, Igbo | Metric parity with English |
| Verdict end to end | Packs with known truth (lab results, regulator confirmation) | Confusion table, false reassurance count |

## 5. Design notes

**Case format (JSON Lines).** One case per line, for example: id, component, input reference, expected, tags, split. Keep images out of git if large, and reference them in storage by hash.

**Runner.** For each case, call the component through the same interface production uses, record the output and cost and latency, score it, and write results. Always record the model, prompt version, rules version, and dataset version in the report header.

**Scoring.** Prefer exact or rule-based scoring. Use an LLM judge only for open-ended answer quality. Calibrate the judge by comparing it with human labels on at least 50 cases, report agreement, and re-check whenever the judge model or prompt changes. Judges are never used for safety gates that can be checked by rules (for example advice leaks are detected by policy outcome and forbidden phrase checks first, then reviewed by a human on any ambiguity).

**Modes.** Offline mode replays stored model outputs from a recording so pull requests can validate scoring, parsing, and policy code without cost. Live mode calls providers and runs nightly and before releases.

**Gates.** A YAML file lists metric thresholds per eval (for example advice leak count equals zero). The runner exits non-zero if any gate fails, blocking the release job.

**Feedback loop.** Every confirmed production failure becomes a new case in the dev split within a week, with a link to the incident.

## 6. Tests for this phase (tests of the eval system itself)

| Test | Level | What it proves |
| --- | --- | --- |
| Metric functions: exact match, recall at K, kappa, confusion table against hand-computed examples | unit | Correct scoring |
| Dataset loader rejects cases missing required fields or with duplicate IDs | unit | Data hygiene |
| Runner on a fake component with known outputs produces the expected report | unit | Runner logic |
| Gate evaluation: failing metric leads to non-zero exit; passing leads to zero | unit | Enforcement |
| Offline mode produces identical results on repeat runs | integration | Determinism |
| Report header contains model, prompt, rules, dataset versions | unit | Reproducibility |
| Judge calibration script reports agreement and fails below the threshold | unit | Judge quality |
| A deliberately broken assistant (always answers, no citations) fails the gates | integration | Gates have teeth |
| A deliberately over-refusing assistant fails the over-refusal gate | integration | Gates have teeth |
| Dev and test splits do not overlap | unit | No leakage |
| Cost cap stops a live run when exceeded | integration | Spend control |

## 7. Pitfalls and guards

- **Tuning on the test set.** Once you iterate against a set, it is no longer a test. Keep a held-out split and look at it rarely.
- **Tiny samples.** Ten cases per category give noisy percentages. Report counts and, where useful, confidence intervals.
- **Clean-data bias.** Include blurry, dark, curved, worn, and multi-language cases, or the metrics will flatter the system.
- **Label errors.** Review disagreements and the cases the system gets wrong. Many turn out to be labeling mistakes.
- **Judge bias.** An LLM judge tends to favor fluent answers. Do not use it where a rule can check.
- **Moving targets.** Without recorded versions, a score change cannot be explained. Always stamp reports.
- **Gates only on averages.** Safety gates must count individual failures (any advice leak fails the run).
- **Ground truth for authenticity is scarce.** Lab-confirmed counterfeits are rare. Agree with NAFDAC how to obtain confirmed samples for the pilot and record the limits of the data.
- **Privacy.** Eval photos and questions need consent and the same retention rules as production data.

## 8. Definition of Done

- Every AI-dependent component has an eval, a dev and a test split, and gates.
- Nightly run is green or has documented waivers.
- Reports are stored and trend tracking works. Checklist ticked, tracker updated.

## 9. Notes for agents

- Do not edit dataset files, labels, or gates to make a run pass. Propose changes in a PR with the reason.
- New components ship with an eval plan in their own phase document before code is merged.
- Keep scoring code deterministic and covered by unit tests. A bug here corrupts every decision downstream.
