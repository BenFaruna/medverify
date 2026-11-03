# Watchlist process

How a watchlist entry gets into MedVerify NG, who approves it, and how it is
retired. This is the process the code enforces where it can (provenance is a
database constraint) and relies on people for where it cannot (judgement,
approval, review dates).

**Status:** proposed. The reviewer and second approver named below have not been
appointed yet, so no entry may be imported for production use until they are.
The process is written down first so the appointment has something concrete to
sign up to.

## 1. What the watchlist is, and is not

It is a copy of statements other regulators have already published: NAFDAC
public alerts, withdrawal notices, and lists of banned or restricted
ingredients. Each entry cites the document it came from.

It is not our own risk assessment. Nobody at MedVerify decides that a product is
unsafe, counterfeit, or harmful. We report that a cited source says something
about a product or ingredient, and we show that source.

Consequences:

- An entry without a source document cannot exist. The database refuses it.
- We never name a manufacturer the cited source does not name.
- A missing entry means we have not seen a source about the product. It does
  not mean the product is unproblematic.

## 2. Roles

| Role | Responsibility |
| --- | --- |
| Watchlist reviewer | Pharmacist. Decides whether a source document is in scope, drafts entries, sets severity and review dates. |
| Second approver | A different person (project lead until a second pharmacist is appointed). Reads the draft against the source and approves the import. |
| Operator | Runs `admin watchlist import`, records the import output, and keeps the CSV with the source documents. |

One person may not fill both the reviewer and second-approver role for the same
import. The importer records `reviewed_by` on every entry; the approval is
recorded outside the database, in the ticket that carries the CSV.

## 3. Adding entries

1. **Collect sources.** Save each alert or notice as issued, with its URL and
   issue date. A screenshot or a forwarded message is not a source; find the
   published document.
2. **Draft a CSV** in the format of `testdata/watchlist/entries.csv`
   (header documented in `testdata/watchlist/README.md`). One row per entry, with
   `reviewed_by` set to the reviewer's name, `source_title` and `source_url`
   from the document, and `issued_at` as the document's date.
3. **Review.** The reviewer checks each row against the document: the NRN or
   product name, the manufacturer spelling, the ingredient normalization, the
   reason, and any `effective_until`. Reject anything the document does not
   support.
4. **Approve.** The second approver reads the same CSV against the same
   documents and approves it in the ticket.
5. **Dry run.** `admin watchlist import --dry-run <file>` validates the whole
   file. Nothing is written unless every row passes; a rejected import writes
   nothing at all.
6. **Import.** `admin watchlist import <file>`. Record the printed counts (new
   sources, new entries, skipped duplicates) in the ticket. Re-running the same
   file is safe: a repeated row is skipped by its natural key.
7. **Archive.** Attach the CSV and the source documents to the ticket. They are
   the audit trail for what was live at any date.

Anything automated may only produce drafts for step 3. A scraper that writes
entries directly is not permitted.

## 4. Wording and severity

User-facing text is never written by the importer and never generated at
runtime. A flag carries a reason code, and `wording/en.yaml` maps each code to a
reviewed template. Changing a template is a reviewed change like any other:
propose the text, get pharmacist approval, then ship it.

The severity map lives in `config/safety-v1.yaml`. It orders flags for the
verdict engine; it is not a medical judgement and must not be shown to users as
one.

## 5. Review dates and retirement

- Every entry gets a `review_due_at` no more than twelve months after import.
  `admin watchlist list` shows entries past their review date.
- At review the reviewer re-reads the source. If the alert was withdrawn or
  superseded, the entry is retired.
- Retiring is `admin watchlist deactivate <id>`, which sets `active = false`.
  Rows are never deleted: past scans must keep citing what they saw. Existing
  flags are snapshots and do not change.
- If a source document is corrected, draft a replacement entry and retire the
  old one; do not edit history in place.

## 6. What the database enforces for you

- A source needs an issuing body, a title, an `https` URL, and an issue date.
- An entry needs a source, a non-empty reason and `reviewed_by`.
- A product entry names an NRN or a product name; an ingredient entry names an
  ingredient. Never both, never neither.
- The same row imported twice from the same document is stored once.
- Every flag stores a copy of the source it cited, so a later retirement cannot
  rewrite a past result.

Validation that cannot live in the schema — is this really the document, is the
manufacturer spelled as printed, is this the right ingredient — is the
reviewer's job.

## 7. Open items

- Appoint the reviewer and the second approver, and record their names here.
- Pharmacist review of `wording/en.yaml` (currently marked draft).
- Pharmacist sign-off of the strength tolerance (0.05 in `config/safety-v1.yaml`
  is provisional).
- Complete the 30-case acceptance review described in the phase 06 document.
