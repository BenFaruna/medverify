# Fixed-text copy approval

Status: **approved and enabled.**

The project owner reviewed the fixed copy and approved it on 2026-11-05. The
web client uses Pidgin when selected, and the assistant loads the English and
Pidgin fixed text.

## Approved surfaces

| Surface | File | State |
| --- | --- | --- |
| Assistant fixed text, English (refusals, fallbacks, disclaimer) | `internal/assistant/templates/en.yaml` | `reviewed: true`; production startup passes |
| Assistant fixed text, Pidgin (refusals, fallbacks, disclaimer) | `internal/assistant/templates/pcm.yaml` | `reviewed: true`, loaded when `ASSISTANT_LANGUAGES` includes `pcm` |
| Web page copy (labels, hints, errors, verdict wording) | `web/assets/copy.js` (the `pcm` object) | `pcm` is in `REVIEWED_LANGUAGES`, so the selector activates it |

The approval covers the copy as written in those files. Any later wording change
needs a new review before it ships.

## Out of scope

Verdict reason sentences and safety flag wording still come from the English
catalogs in `internal/verdict/reasons/en.yaml` and
`internal/safety/wording/en.yaml`. A Pidgin result therefore mixes Pidgin fixed
text with English reason/flag sentences. Translating those surfaces is a
separate review.

## Enabled configuration

- `ASSISTANT_LANGUAGES=en,pcm` in `.env` and `.env.example`
- `docker-compose.yml` passes `ASSISTANT_LANGUAGES` through to the API and worker
- `web/assets/copy.js` lists `"pcm"` in `REVIEWED_LANGUAGES`

## Verification checklist

1. Select **Nigerian Pidgin** in the web client: page labels, hints, errors, and
   verdict headings switch to Pidgin.
2. Ask a question that triggers a refusal or fallback: the answer uses the
   Pidgin fixed text.
3. A result card shows Pidgin verdict wording and the English reason/flag
   sentences that are still out of scope.
4. Switch back to English: the page copy and new answers return to English.
