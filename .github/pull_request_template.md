## What changed

<!-- One or two sentences. Link the phase checklist item this closes. -->

## Definition of Done

Copied from the build tracker so it is a checklist, not a memory test.

- [ ] Code compiles, lint is clean (`make lint`), govulncheck is clean (`make vuln`)
- [ ] Unit tests pass (`make test`); new behaviour has tests
- [ ] Integration tests pass (`make test-integration`) when the change touches DB, storage, migrations, or jobs
- [ ] Migrations are reversible and the down path was run
- [ ] `api/openapi.yaml` and the phase document are updated if behaviour changed
- [ ] No secrets, images, or personal data in logs or fixtures
- [ ] Phase checklist item ticked; tracker status updated

## Dependencies

<!-- Any new module needs a one-line justification. "None" is a valid answer. -->
