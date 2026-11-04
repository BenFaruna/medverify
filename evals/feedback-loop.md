# Feedback loop

Evals only improve the product if real misses become labelled cases. This
process keeps that loop from quietly leaking test cases or inflating scores.

## Signals

1. A user-visible fallback where a supported answer existed.
2. A refusal for a legitimate lookup (over-refusal).
3. An answer without a citation, or a citation that does not support the claim.
4. Forbidden reassurance such as "safe", "genuine", "authentic", or a verdict
   stated as certainty.
5. A dosing, diagnosis, or personal-advice answer.
6. An extraction fabrication, especially an invented NRN.
7. A retrieval miss where the answer existed in a leaflet.
8. A verdict label that contradicts lab or regulator evidence.

Do not log raw user questions, images, prompts, or provider responses. Capture
a de-identified report reference and reproduce the case with synthetic or
consented inputs.

## Triage

For each signal, decide:

- **Product bug**: wrong tool, wrong scope, missing citation, template mistake.
  Fix the code or policy, then add a regression case.
- **Model/prompt quality**: the pipeline was correct but the model failed. Add
  a `dev` case, change the prompt or model, and re-run.
- **Dataset gap**: the expected behavior was not covered. Add a labelled case.
- **Policy ambiguity**: stop and get the project lead or pharmacist reviewer to
  decide. Do not encode a guess as a label.
- **Not reproducible / unsafe to store**: record the blocker; do not fabricate
  a case.

## Adding a case

1. Write the case with `source`, `labeler`, and `date` filled in.
2. Add it to the `dev` split first.
3. Fix the code, prompt, or threshold until the dev case passes.
4. If the failure class is genuinely unseen, add a distinct equivalent case to
   `test`. Do not move the same input across splits; the loader rejects exact
   duplicates, and the labelling guide rejects near-duplicates.
5. Bump the dataset version and commit the cases with the fix.
6. Run the component eval with the proposed gate file and attach the report to
   the change.

## Release check

Before a release:

- Run the `test` split with the agreed gate file.
- Compare against the previous report in `evals/reports/` (local artifacts,
  not committed). Record the dataset version and hash, model, prompt version,
  rules version, and date.
- A gate failure blocks the release unless the project lead records a written
  exception. Do not lower a threshold to make a run pass.
- Any new safety leak (advice, injection success, fabricated NRN) blocks the
  release even if the aggregate rate still passes.

## Ownership

- Engineering owns the harness, gates, and report format.
- The project lead approves thresholds and exceptions.
- A pharmacist or legal reviewer approves assistant policy, refusal text, and
  the disclaimer.
- Two labelers own real safety and verdict labels.

None of these approvals are recorded yet for phase 10; the blockers stay open
in the build tracker.
