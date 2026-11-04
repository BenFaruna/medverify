# Dataset schema

`cmd/eval` loads one directory per dataset. Unknown fields are rejected at
every level, so a typo fails the run instead of becoming an unlabelled case.

## `dataset.json`

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | Stable dataset name; also used in report file names |
| `version` | yes | Human-chosen version such as `2026-11` or `v3` |
| `component` | yes | `extraction`, `retrieval`, `assistant`, or `verdict` |
| `description` | no | What the dataset covers and what it does not |
| `created_by` | no | Team or role, never a personal identifier |
| `created_at` | no | `YYYY-MM-DD` |

## `cases.jsonl`

One JSON object per line. Blank lines are ignored.

| Field | Required | Meaning |
| --- | --- | --- |
| `id` | yes | Unique within the dataset; stable across versions |
| `component` | yes | Must equal `dataset.json:component` |
| `input` | yes | Component-specific input object |
| `expected` | yes | Hand-labelled ground truth object |
| `tags` | no | `language`, `difficulty`, `category` for slicing |
| `source` | yes | Where the case came from; `synthetic` for fixtures |
| `labeler` | yes | Role or initials under the labelling policy |
| `date` | yes | `YYYY-MM-DD` labelling date |
| `split` | yes | `dev` or `test` |

`input` and `expected` must be JSON objects, not `null` or scalars. A case whose
input or expected value is missing cannot be scored.

## Component payloads

### Assistant

```json
{"question": "...", "scan_id": "optional", "language": "en"}
```

```json
{
  "behavior": "answer | refuse | redirect | not_found",
  "intent": "dosing | diagnosis | personal_safety | prompt_injection | ...",
  "must_cite": true,
  "contains": ["expected substring"],
  "not_contains": ["forbidden substring"]
}
```

`must_cite` is required on `answer` cases. `intent` is required on `refuse` and
`redirect` cases. `contains` / `not_contains` are case-insensitive substrings,
not model judgements.

Recorded outputs are a separate JSONL file:

```json
{"id": "case-id", "output": {
  "answer": "...", "source_ids": ["registry:1"], "verified": true,
  "refused": false, "not_found": false, "intent": "registration",
  "language": "en", "model": "...", "small_model": "...",
  "prompt_version": "...", "rules_version": "...", "cost_usd": 0
}}
```

All outputs in one recording must share model, small model, prompt version, and
rules version; mixing runs is rejected.

### Extraction

The extraction manifest format is defined in
[../testdata/eval/README.md](../testdata/eval/README.md). It is a JSON
manifest, not `cases.jsonl`, because image paths are relative to the manifest.
The runner wraps its scores into the shared report shape.

### Retrieval

The retrieval eval set format is defined in
[../internal/rag/eval.go](../internal/rag/eval.go) and uses
`testdata/rag/eval-draft.yaml` as a format fixture. It is YAML with
`version` and `cases`, each case naming a product, question, expected section
(or `unanswerable: true`), and optional language.

### Verdict

Verdict ground truth must come from a lab or regulator confirmation where
available. A case must state which outcome was confirmed and the evidence
reference; a screenshot or single reviewer is not ground truth. No verdict
dataset is committed yet.

## Judge calibration file

A judge is not used for release decisions until its agreement with human
labels is measured. The calibration file is JSONL, one pair per line:

```json
{"id": "case-id", "human": "supported", "judge": "unsupported"}
```

`human` is the agreed two-person label; `judge` is the judge's label for the
same case. The declared label set is passed with `-values`. The runner computes
Cohen's kappa and a confusion table, and refuses to call the judge calibrated
unless the project lead's agreed kappa floor and minimum pair count are met.
The file contains no answer text.

## Splits and leakage

- `dev` may be used for prompt, model, rule, and threshold tuning.
- `test` is only used for the final measurement.
- The same `component` + normalized `input` + normalized `expected` cannot
  appear in both splits, even under a different `id`.
- Do not put near-duplicate variants of one test case in `dev`; the loader only
  catches exact duplicates, the labeller guide asks for the rest.
