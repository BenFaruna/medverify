# Evaluation datasets

Versioned JSON datasets and the recorded scores produced by `cmd/eval`
(phase 10). Each dataset is checked in with the date it was captured so scores
stay comparable across runs.

Datasets contain synthetic or approved fixture packs only — never real user
photos or personal data (see [../docs/data-handling.md](../docs/data-handling.md)).

## Layout

```
evals/
  schema.md              dataset.json and cases.jsonl fields
  labeling-guide.md      how a human labels a case
  feedback-loop.md       how a production miss becomes a labelled case
  gates/                 proposed release thresholds, one file per component
  fixtures/              synthetic format examples, never quality evidence
  reports/               local output for `-out-dir`; only the README is committed
  <component>/           a real dataset, one directory per dataset name
    dataset.json
    cases.jsonl
```

A real dataset is a directory whose name is also `dataset.json:name`. The
runner reads `dataset.json` and `cases.jsonl` only. The dataset hash is the
SHA-256 of `cases.jsonl`.

## Versioning

- Bump `version` whenever a case is added, removed, relabelled, or moved
  between splits.
- Never edit a published version in place. Add a new version directory or bump
  the version and keep the old report.
- `split: test` is held out. Do not tune prompts, models, thresholds, or rules
  against test cases. The loader rejects the same labelled input appearing in
  both `dev` and `test`.
- Every case records `source`, `labeler`, and the ISO `date` it was labelled.
- Dataset files must not contain raw user questions, images, prompts, or
  provider responses.

## Fixtures are not evidence

`evals/fixtures/` exists only to prove the runner, schema, and gate machinery
work in CI. Fixture cases are synthetic, single-labeller, and deliberately
small. They must never be quoted as model quality, recall, safety, or
calibration evidence.

No real assistant, extraction, retrieval, or verdict dataset is committed in
this phase. The required consented photos, human-labelled question corpora, and
verdict ground truth are external dependencies recorded in the phase 10
checklist and build tracker.

## Running

```sh
# Offline assistant scoring from recorded outputs
go run ./cmd/eval assistant \
  -dataset evals/fixtures/assistant-format \
  -recorded evals/fixtures/assistant-format/recorded.jsonl \
  -split dev \
  -gates evals/gates/assistant.proposed.yaml \
  -out-dir evals/reports

# Extraction and retrieval call real providers/DB; CI must not run them.
go run ./cmd/eval extraction -manifest testdata/eval/extraction.json -gates evals/gates/extraction.proposed.yaml
go run ./cmd/eval retrieval -file testdata/rag/eval-draft.yaml -gates evals/gates/retrieval.proposed.yaml

# Verdict replay over normalised signals (offline)
go run ./cmd/eval verdict -dataset evals/fixtures/verdict-format -split test \
  -gates evals/gates/verdict.proposed.yaml

# Judge calibration against agreed human labels and an agreed kappa floor
go run ./cmd/eval judge-calibration -labels <pairs.jsonl> \
  -values supported,unsupported -min-kappa <agreed> -min-cases <agreed>
```

A failing gate exits non-zero after writing the report. The report header
records dataset version and hash, model, small model, prompt version, rules
version, start time, and cost.
