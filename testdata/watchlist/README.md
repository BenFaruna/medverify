# Watchlist fixtures

`entries.csv` is a synthetic fixture for tests and for exercising the admin
import. It is **not** production watchlist data.

- Every product name includes "Test" and every manufacturer is fictional.
- Every NRN uses the `TT4-` prefix, which does not occur in the Greenbook
  fixtures.
- Every source URL is under `example.test` and resolves to nothing. The issuing
  body and dates are shaped like real notices so the import and provenance
  rules are exercised.
- Reviewer names are placeholders.

Real entries must come from a human reviewer working from a cited NAFDAC or
other regulatory document, and must be imported with the admin command. Never
scrape a source directly into the database; see
[docs/watchlist-process.md](../../docs/watchlist-process.md).

The format is documented in
[internal/safety/import.go](../../internal/safety/import.go). Required columns:
`kind`, `reason`, `issuing_body`, `source_title`, `source_url`, `issued_at`,
`reviewed_by`, plus the matcher columns for the entry kind.
