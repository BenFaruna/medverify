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

- [ ] Tables: watchlist\_entries, watchlist\_sources, safety\_flags (or flags stored in scan\_results.features)
- [ ] Admin CLI: import watchlist entries from CSV, list, deactivate, with validation
- [ ] Provenance rule enforced in the database: no entry without a source URL, issuing body, and issue date
- [ ] Ingredient synonym table shared with phase 02
- [ ] Safety checker: input is MatchResult plus LabelData, output is a list of SafetyFlag
- [ ] Configurable strength tolerance and severity map
- [ ] Review workflow documented: who adds entries, who approves, how they are retired
- [ ] Flag wording catalog (codes to templates), reviewed by a pharmacist
- [ ] Integration into process\_scan

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
