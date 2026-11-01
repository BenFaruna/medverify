# Scoring philosophy

This document fixes the language and evidence rules the verdict engine (phase
07) must follow. It exists from phase 1 so the UI, the assistant, and the eval
suite are written against the same rules.

## What a verdict is

A verdict is a short, evidence-backed statement about how a photographed pack
relates to NAFDAC records and to our curated watchlist. It is not a
certificate, a guarantee, or medical advice.

## Allowed verdict language

| Situation | Wording |
| --- | --- |
| Label fields match a NAFDAC record | "Consistent with NAFDAC records" |
| A label field disagrees with the record | "Mismatch found" |
| No matching NAFDAC record | "Not found in NAFDAC records" |
| Record or brand matches a watchlist entry | "Flagged against a NAFDAC alert" |
| Extraction confidence too low | "Unreadable" |
| Provider or registry unavailable | "Verification incomplete" |

Forbidden wording includes "safe", "genuine", "authentic", "fake", and "banned"
as conclusions about a specific pack. A product may be described as flagged
against a specific alert, always with the alert's source.

## Grading, not scoring

Phase 1 deliberately defines no numeric score. A number invites users to read a
probability of authenticity, which our evidence cannot support. The verdict
engine returns a grade plus the evidence behind it:

1. **Unreadable**: the label could not be read with enough confidence.
2. **Incomplete**: one or more checks could not be completed.
3. **Mismatch**: at least one check contradicts the registry or the watchlist.
4. **Consistent**: every applicable check agrees with the registry.

Order matters: a mismatch outranks consistency, and an unreadable result never
degrades into a guess.

## Evidence rules

1. Every factual statement shown to a user carries a source: a Greenbook record,
   a leaflet section, or a watchlist entry with a URL.
2. "Not found in NAFDAC records" is never presented as "fake". Our dataset can be
   incomplete, stale, or missing recent registrations; the wording must stay
   neutral and must encourage the user to check with a pharmacist.
3. Confidence thresholds are owned by explicit configuration, tested with
   golden files, and never adjusted inside a request to make a result look
   better.
4. Retrieval or extraction failures reduce the grade; they never promote it.

## Human review

Watchlist entries are curated, reviewed by a pharmacist, and versioned with a
source URL. The review process and the reviewer are recorded in phase 06; phase
1 only reserves the vocabulary.
