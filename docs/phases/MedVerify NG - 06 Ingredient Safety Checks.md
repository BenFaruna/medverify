# 06 Ingredient Safety Checks

**Goal:** flag packs whose product or ingredients appear on a curated, sourced watchlist, and flag label-versus-registry ingredient problems, without ever claiming a medicine is safe or unsafe on our own authority.

**Depends on:** 02, 05. **Blocks:** 07. **Read first:** 00 Build Tracker, 05 Registry Verification.

## 1. What counts as a safety flag

| Flag type | Trigger | Example wording shown to users |
| --- | --- | --- |
| watchlist\_product | The matched NRN or product is on an alert or withdrawal list | This product appears in a NAFDAC alert dated (date). See source. |
| watchlist\_ingredient | A registered or printed ingredient is on a banned or restricted list | An ingredient on this label appears on a NAFDAC list (see source). |
| undeclared\_ingredient | Printed ingredient has no counterpart in the registered composition | The label lists an ingredient that is not in the registered record. |
| missing\_ingredient | Registered ingredient is absent from the printed label | The registered record lists an ingredient not found on this label. |
| strength\_deviation | Printed strength differs from registered strength beyond tolerance | The printed strength differs from the registered strength. |
| registration\_problem | Registry status is expired or withdrawn | NAFDAC records show this registration as (status). |

Every flag carries: type, severity, reason code, evidence (the compared values), and a source (watchlist entry ID with URL, or the registry record).

## 2. Deliverables checklist

- [x] Tables: watchlist\_entries, watchlist\_sources, safety\_flags (dedicated table; the phase 05 `features` shape is unchanged)
- [x] Admin CLI: import watchlist entries from CSV, list, deactivate, with validation
- [x] Provenance rule enforced in the database: no entry without a source URL, issuing body, and issue date
- [x] Ingredient synonym table shared with phase 02
- [x] Safety checker: input is MatchResult plus LabelData, output is a list of SafetyFlag
- [x] Configurable strength tolerance and severity map
- [x] Review workflow documented: who adds entries, who approves, how they are retired (docs/watchlist-process.md)
- [ ] Flag wording catalog (codes to templates), reviewed by a pharmacist — codes and catalog are in place; the pharmacist review has not happened
- [x] Integration into process\_scan

## 3. Tools needed

| Tool | Purpose |
| --- | --- |
| encoding/csv and a validation library | Watchlist import |
| PostgreSQL CHECK and NOT NULL constraints | Provenance enforcement |
| testcontainers-go | Integration tests |
| rapid | Property tests on comparers |

## 4. Setup required for success

1. **Source the watchlist with a human in the loop.** Start with NAFDAC public alerts and withdrawal notices. Other regulators or the WHO may be added later. Begin with manual entry or CSV import, reviewed by a pharmacist, before considering any automation.
2. **Appoint a reviewer** and write the process in docs/watchlist-process.md. Nothing goes live without a second person approving it.
3. **Agree the strength tolerance** with a pharmacist. Store it in config. Do not invent a number in code.
4. **Legal review of wording** for any flag that could name a manufacturer. Only name a company if the cited source names it.
5. Prepare a seeded fixture of 20 or more watchlist entries (synthetic or real, with real-looking sources) for tests.

## 5. Design notes

```sql
CREATE TABLE watchlist_entries (
  id bigserial PRIMARY KEY,
  kind text NOT NULL CHECK (kind IN ('alert','withdrawal','banned_ingredient','restricted_ingredient')),
  nrn text, product_name text, ingredient_norm text,
  reason text NOT NULL,
  issuing_body text NOT NULL,
  source_url text NOT NULL,
  issued_at date NOT NULL,
  effective_until date,
  active boolean NOT NULL DEFAULT true,
  reviewed_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (nrn IS NOT NULL OR product_name IS NOT NULL OR ingredient_norm IS NOT NULL)
);
```

**Matching.** Product entries match on NRN first, then on normalized name plus manufacturer when no NRN is given in the source. Ingredient entries match on normalized ingredient name, including known synonyms and salt forms.

**Label versus registry.** Use the field comparisons from phase 05. Undeclared and missing ingredients are computed on normalized ingredient sets. Strength deviation uses the configured tolerance and unit conversion.

**Severity.** A severity map in config (for example high, medium, low) from flag type. Severity feeds the verdict engine but never appears as a health claim.

**Language.** Flags are codes plus parameters. Human wording lives in a reviewed catalog per language. Never generate flag wording with an LLM.

## 6. Tests for this phase

| Test | Level | What it proves |
| --- | --- | --- |
| Inserting a watchlist entry without source\_url fails | integration | Provenance enforced |
| Entry with none of nrn, product\_name, ingredient\_norm fails | integration | Constraint |
| CSV import: valid rows load, invalid rows are reported with line numbers, nothing partially loads | integration | Import safety |
| Product on watchlist by NRN produces watchlist\_product with the source URL | integration | Matching |
| Ingredient synonym (paracetamol versus acetaminophen) still matches a banned entry | unit | Synonym handling |
| Salt form variant matches the base ingredient entry | unit | Normalization |
| Inactive or expired entry produces no flag | unit | Lifecycle |
| Label has an extra ingredient: undeclared\_ingredient with evidence | unit | Label comparison |
| Registered ingredient missing from label: missing\_ingredient, only if the label ingredient list was read with sufficient confidence | unit | No false flags from bad photos |
| Strength within tolerance gives no flag, outside gives strength\_deviation | unit | Tolerance |
| No flags when everything agrees and nothing is on the list | unit | No false positives |
| Every flag template exists for every flag code and language, and contains no forbidden words (safe, harmless, fake, counterfeit, poison) | unit | Wording catalog |
| Every flag returned has a source reference | property | Always sourced |
| Watchlist lookup under 50 ms with 10,000 entries | benchmark | Performance |

**Acceptance test.** Take 30 real or realistic cases covering clean packs, watchlisted products, and label mismatches. A pharmacist reviews every flag and every non-flag and records disagreements. Resolve all disagreements by fixing rules or data, not by hiding cases.

## 7. Pitfalls and guards

- **Claiming harm.** We report a match against a cited source. We do not assess toxicity or safety ourselves.
- **Defamation and legal risk.** Naming a manufacturer or product wrongly can cause real damage. Source every entry, show the source, and keep an audit log of who approved what.
- **Stale watchlist.** Entries need review dates. Show the issue date to users and alert admins when entries are older than a configured age without review.
- **False positives from synonyms.** A loose synonym table can flag innocent products. Review additions to the synonym file as carefully as watchlist entries.
- **Flags from bad photos.** Missing and undeclared ingredient flags require adequate extraction confidence. Otherwise return unknown.
- **Automated scraping of alerts straight into production.** Never. A scraper may only create drafts for review.
- **Hidden tolerance.** Keep the tolerance in config and store its value with each result.
- **Excluding legitimate variation.** Some products have overage or acceptable ranges per the leaflet. Do not assume exact equality unless the pharmacist agrees.

## 8. Definition of Done

- Provenance and lifecycle rules enforced in the database and tested.
- Pharmacist review of the acceptance cases completed and recorded.
- Wording catalog approved. Checklist ticked, tracker updated.

## 9. Notes for agents

- Do not add watchlist data on your own. Only the import command loads data supplied by a human reviewer.
- Do not write user-facing flag text in code. Add a code and a catalog entry, and request review.
- If a test seems to need a real manufacturer name, use a clearly fictional one.

## 10. Implementation notes

- **Schema.** Migration `00006` adds `watchlist_sources`, `watchlist_entries`,
  and `safety_flags`. Sources are normalized out of entries so provenance is
  one row per document, and `watchlist_entries.source_id` is `NOT NULL ... ON
  DELETE RESTRICT`. The shape constraint allows an ingredient entry or a
  product entry (NRN and/or name), never both and never neither. A unique
  expression index over `(source_id, kind, nrn, product_name_norm,
  manufacturer_norm, ingredient_norm)` plus `ON CONFLICT DO NOTHING` makes a
  repeated import of the same reviewed file a no-op. `UpsertWatchlistSource`
  returns `xmax = 0` so the CLI reports only genuinely new sources.
- **Store.** `internal/safety.Store` is the only writer, and only through
  `Import`. Reads are `ProductEntries` (NRN then fuzzy name), `IngredientEntries`
  (shared synonym normalization), `List`, `Counts`, and `Stale` for entries past
  `review_due_at`.
- **Name lookup performance.** A GIN trigram search scored every row and took
  ~165 ms against 10,000 entries. The index is now GiST and
  `SearchLiveWatchlistEntriesByName` is a nearest-neighbour query: an inner
  `ORDER BY product_name_norm <-> $1 LIMIT $2` uses the index and stops, and the
  similarity threshold is applied to those candidates. Measured on this machine
  against 10,000 seeded entries: **NRN 0.14 ms, name 2.1 ms** (target 50 ms).
  The candidate cap is the same 20 as before, so a name with more than 20 live
  near-matches still returns the closest 20.
- **Import validation.** `ParseImportCSV` is atomic: the file is fully parsed
  and validated into memory (header exact and complete, no unknown or duplicate
  columns, kind-specific shape, dates, absolute `https` URL) and only then
  written in one transaction. Errors carry the CSV line number. The header
  tolerates a UTF-8 BOM and case/underscore variants because exported
  spreadsheets add them.
- **Config and wording.** `config/safety-v1.yaml` (embedded, versioned, unknown
  keys rejected) holds the name and manufacturer thresholds, the minimum
  ingredient confidence, the strength tolerance, and the severity map.
  `SAFETY_CONFIG_FILE` overrides it; the loaded version is logged at worker
  start. `wording/en.yaml` maps every reason code to a reviewed template, and a
  test asserts every code is covered and no template contains safe, harmless,
  fake, counterfeit, or poison. The catalog is marked draft pending pharmacist
  review.
- **Checker rules.** NRN hits are always attributed. A name-only entry needs
  manufacturer agreement on the label, so a bare brand match is never
  attributed to a company. Ingredient checks reuse the phase 02 synonym
  normalization and salt-form handling; undeclared and missing ingredients are
  computed on normalized sets. Missing-ingredient flags are only raised when at
  least one printed ingredient claim was read with adequate confidence, so a
  bad photo cannot produce one. Strength deviation reuses phase 05's unit-aware
  comparison (now exported as `verify.StrengthsAgree`) and embeds the tolerance
  in the evidence. A registration status of expired or withdrawn adds its own
  flag. Every flag carries a source snapshot with URL, title, issuing body, and
  issue date; a rapid property test asserts no flag is ever sourceless.
- **Scan integration.** `scan.Service` takes a `SafetyChecker` option and runs
  it after registry verification. Flags are deleted and reinserted in the same
  transaction that finishes the scan, so a retry replaces them rather than
  duplicating. `scan.View.Flags` is always an array (empty, never null) and the
  OpenAPI adds `SafetyFlag` and `SafetyFlagSource`. A checker failure fails the
  scan with a retryable error, matching the phase 05 matcher's behaviour.
- **Fixture.** `testdata/watchlist/entries.csv` holds 20 synthetic entries from
  five fictional source documents (`example.test` URLs, `TT4-` NRNs). It is a
  format example and import test fixture, not real regulatory data.
- **Tests.** Unit tests cover every checker rule, config and wording validation,
  and property tests for sourcing and wording. Integration tests cover
  provenance constraints, import atomicity and re-import idempotency, flag
  persistence and replacement on retry, the scan failure path, and the 10,000
  entry lookup budget.

## 11. Blockers and open items

All four external blockers remain open. None is resolved by this phase's code:

1. **Consented, double-labelled 100+ photo eval set.** The extraction harness
   is ready; the dataset is absent.
2. **Live provider acceptance run.** Needs `VISION_API_KEY`, pinned model ids,
   a provider spending cap, and data-handling approval.
3. **Data-handling decision** for sending pack photos to a foreign provider.
4. **NAFDAC data-sharing agreement** from phase 02.

Open items specific to this phase:

- No pharmacist has reviewed `wording/en.yaml`; it is shipped marked draft and
  must not be presented as approved clinical wording.
- The strength tolerance (0.05 in `config/safety-v1.yaml`) is a provisional
  engineering default, not a pharmacist decision.
- The reviewer and second approver in docs/watchlist-process.md have not been
  appointed, so no production watchlist data has been imported. Only synthetic
  fixtures exist.
- The 30-case acceptance review in section 6 has not been performed; it needs a
  pharmacist and realistic cases.
- Entry review dates and the stale-entry alert exist in the store and CLI, but
  nobody is scheduled to act on them yet.
