# Glossary

Short definitions for terms used in the code and the phase documents.

| Term | Meaning |
| --- | --- |
| NAFDAC | National Agency for Food and Drug Administration and Control, Nigeria's drug regulator. |
| Greenbook | NAFDAC's public register of approved products, published as a spreadsheet and online. |
| NRN | NAFDAC Registration Number, the identifier printed on a pack and listed in the Greenbook. |
| SmPC | Summary of Product Characteristics: the leaflet PDF linked from a Greenbook entry. |
| Watchlist | Our curated list of alerts, withdrawals, and banned or risky ingredients, each with a source URL and reviewer. |
| Verdict | The grade returned for a pack: unreadable, incomplete, mismatch, or consistent, with evidence. |
| Extraction | Reading printed fields (product name, NRN, strength, batch, expiry) from a pack photo. |
| RAG | Retrieval-augmented generation: retrieve relevant leaflet or registry text, then answer only from it. |
| Citation | The record, leaflet section, or watchlist entry behind a statement shown to a user. |
| Problem details | RFC 7807 error body: type, title, status, detail, instance, plus our stable `code`. |
| Liveness | `/healthz`: the process is running. Never checks dependencies. |
| Readiness | `/readyz`: Postgres and object storage are reachable; 503 otherwise. |
| River | Postgres-backed job queue used for slow work such as ingestion and extraction. |
| sqlc | Generates typed Go from SQL files; all SQL in this repository is compiled by it. |
| goose | Migration tool; every migration is reversible and is tested down as well as up. |
