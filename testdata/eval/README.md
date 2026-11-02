# Extraction eval set

The phase 04 eval set is **not committed**. It consists of real pack photos and
hand-labelled ground truth, and each photo needs recorded consent. Fabricating
or committing synthetic "ground truth" would make the quality gates meaningless.

## Required size

At least 100 legible packs, with deliberate coverage of:

- poor lighting, glare, and motion blur
- curved bottles and worn packaging
- front, back, and side faces
- English, Pidgin, Yoruba, Hausa, and Igbo packaging where available
- at least 10 photos where the NRN is not readable

Two people should label a sample independently and the disagreement rate should
be recorded before the set is used as truth.

## Manifest format

Create `testdata/eval/extraction.json` (git-ignored or kept in the private
evidence store) with image paths relative to the manifest:

```json
{
  "name": "extraction-2026-11",
  "cases": [
    {
      "id": "pack-001",
      "images": ["images/pack-001-front.jpg", "images/pack-001-back.jpg"],
      "language": "en",
      "expected": {
        "nrn": "01-2345",
        "product_name": "Paracetamol Tablets BP 500 mg",
        "manufacturer": "Example Pharma Ltd",
        "batch_no": "PCM-2026-014",
        "mfg_date": "2026-01",
        "expiry_date": "2028-01",
        "ingredients": [
          {"name": "Paracetamol", "strength": "500 mg"}
        ]
      }
    }
  ]
}
```

Leave a field out when the labeller could not read it. A missing `nrn` is
scored as a no-NRN case: any NRN the model returns counts as fabricated.

## Running

```bash
VISION_PROVIDER=claude VISION_MODEL=... VISION_API_KEY=... \
  go run ./cmd/eval -manifest /secure/eval/extraction.json
```

The command exits non-zero when a fabricated NRN appears or the normalized NRN
match rate is below `-min-nrn` (default 0.90). It prints per-field exact and
normalized rates plus the failing cases.

CI never invokes this command against a real provider; contract tests use the
recorded fixtures under `internal/vision/testdata/`.
