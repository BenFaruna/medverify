# Verdict scoring

This document describes how the phase 07 verdict engine turns evidence into a
verdict. It is the implementation-level companion to
[scoring-philosophy.md](scoring-philosophy.md): that document fixes the allowed
language, this one fixes the mechanics. Where the two differ, the built rules
file is authoritative and this document is out of date.

Status: **proposed**. The verdict meanings, score bands, weights, and reason
wording need sign-off from the project lead and a pharmacist before release.
Nothing here has been clinically reviewed.

## What the engine is

`internal/verdict.Evaluate` is a pure function. It takes a normalized signals
record and a versioned rules file and returns a verdict, a concern score, a
coarse confidence, and ordered reasons. It has no clock, no database, no
network, no randomness, and no LLM. The same input always produces the same
output, which is what makes a stored result auditable and replayable.

The engine is deliberately biased toward caution. When the evidence is thin,
ambiguous, or contradictory it produces `needs_review`, not
`consistent_with_records`. A false "consistent with records" is the costliest
error the system can make.

## Inputs: signals

Before evaluation, `scan.Service` normalizes everything the engine is allowed to
look at into `verdict.Signals`:

- the registry outcome (matched consistent, matched with mismatch, candidate
  only, ambiguous, not found, unreadable input);
- per-field outcomes (agree, disagree, unknown) with the reason codes that
  explain them;
- the safety flags raised in phase 06, including severity;
- extraction quality: overall confidence, whether the label was identified, and
  which fields could not be read;
- dataset size and candidate count at match time;
- whether the expiry date printed on the pack has passed.

The signals are stored on `scan_results.signals` together with the verdict. They
are the only input replay uses.

## Order of evaluation

The order is fixed and short-circuits:

1. **Gate.** If the label was not identified, or its extraction confidence is
   below `gate.min_extraction_confidence` (currently 0.50), the outcome is
   `unreadable`. The match is not consulted at all. A bad photo never becomes a
   guess.
2. **Unreadable input.** If the registry comparison itself reports unreadable
   input, the outcome is `unreadable` for the same reason.
3. **Hard rule.** Any high-severity safety flag (an attributed watchlist alert,
   for example) produces `flagged` regardless of everything else.
4. **Outcome mapping.** The registry outcome selects the base verdict.
5. **Adjustments.** Medium and low severity flags, mismatches, unknown fields,
   an expired printed date, and thin matches add concern points inside the base
   verdict's band. They order results; they do not invent a new verdict.

Unknown fields are the union of extraction fields that could not be read and
field comparisons whose outcome is `unknown`. They add concern and a reason, but
they never push confidence below the gate on their own: a partial read of a
strong match stays a match, with the gap named.

## Score bands

The score is an integer from 0 to 100 where higher means more concern. Each
verdict owns a closed, non-overlapping range, ordered by concern:

| Verdict | Band |
| --- | --- |
| `consistent_with_records` | 0-24 |
| `needs_review` | 25-54 |
| `not_found_in_records` | 55-64 |
| `mismatch_found` | 65-84 |
| `flagged` | 85-99 |
| `unreadable` | 100 |

Because the bands do not overlap and are ordered, sorting by score can never
place a `flagged` result below a `consistent_with_records` result. The bands
live in `internal/verdict/config/verdict-v1.yaml` and are validated at load
time; overlap, a missing verdict, or a range outside 0-100 is a startup error,
not a runtime surprise.

## The score is not a probability

The score exists for sorting review queues and for analytics. It is never a
percentage, a probability of authenticity, or a confidence that a pack is
"real". A score of 70 does not mean a 70 percent chance of anything, and the API
must not render it as one. Clients show the verdict, the confidence level, and
the reasons; the score is available to integrators but is documented as
internal.

## Confidence

Confidence is one of `low`, `medium`, `high`, never a number. It combines
extraction confidence with the strength of the match, and takes the weaker of
the two: a confident read of an unconfirmed candidate is still low confidence.
`unreadable` results are always low.

## Reasons and wording

Every verdict carries one or more reasons. A reason is a stable code, an
optional field name, and optional parameters (for example the printed and
registered strength). Codes are machine keys; the reviewed sentence lives in
`internal/verdict/reasons/en.yaml` and is filled from the parameters. Reasons
are deduplicated, ordered by importance, and capped at
`limits.max_reasons` (currently 8).

No verdict, reason, or catalog sentence says a medicine is safe, genuine, or
authentic, and none says it is fake, counterfeit, harmless, or dangerous. A
test scans the rendered output and every catalog sentence for those words. "Not
found in records" is a statement about our dataset, not about the pack.

## Versioning and replay

`internal/verdict/config/verdict-v1.yaml` carries a version, and every stored
result records the version that produced it
(`scan_results.verdict_rules_version`). Any change to the verdict meanings,
bands, weights, gate, or confidence thresholds must bump the version.

`medverify-admin verdict replay --rules <file> [--limit N]` re-scores stored
results from their recorded signals under a candidate rules file. It is a dry
run: it writes nothing, never calls the provider, and never reads an image. It
prints each changed scan with the stored and replayed verdict, score,
confidence, and which of those changed, followed by a per-verdict count. Use it
to see the effect of a tuning change against real history before deploying it.

The scenario table in `internal/verdict/testdata/scenarios.yaml` is the
specification the engine is tested against. It currently holds 50 cases covering
every verdict, written before the weights were chosen. It is a draft: it needs
the project lead and a pharmacist to confirm that each expected verdict is the
one a reviewer would give.

## Change control

1. Change the candidate rules file and bump its version.
2. Add or update scenario cases first, then run the scenario suite.
3. Run the property tests: determinism, monotonicity (adding a concern never
   lowers the score), and the boundary cases around each threshold.
4. Replay against stored results and review every verdict change.
5. Get the wording and verdict meanings reviewed before release.

## Open items

- The scenario table, the verdict meanings, and these numbers have not been
  signed off by the project lead or a pharmacist.
- `reasons/en.yaml` is draft wording, not approved clinical communication.
- Only English reason text exists; a second language needs a complete catalog
  or the wording-completeness test fails.
- No held-out evaluation set exists yet, so the weights have not been tested for
  overfitting. The 100+ photo eval set is still an open external blocker.
- The phase 09 assistant policy must mirror these verdicts and the same
  forbidden wording; it does not exist yet.
