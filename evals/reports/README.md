# Eval reports

`cmd/eval -out-dir evals/reports` writes `<component>-<dataset>-<version>[-<split>].json`
and `.md` here. Reports include the dataset version and hash, model, prompt
version, rules version, start time, and cost.

Only this README is committed. A report produced by a live or fake provider run
is a local artifact, not repository content, and must never be quoted as
quality evidence until it comes from a real human-labelled dataset.

Compare a candidate run with the most recent report for the same component and
dataset version. A different dataset hash means the scores are not comparable.
Keep the reports used for a release decision with the release record, outside
git if they contain environment details.
