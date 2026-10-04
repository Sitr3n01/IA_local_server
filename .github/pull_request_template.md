## Intent

<!-- What changes and why. Link the issue or ADR when there is one. -->

## Trust boundary

<!-- The boundary this touches — listeners, credentials, the administrative surface, logging, artifacts — or "none". -->

## Evidence

- [ ] `go test ./...` and `go vet ./...` pass
- [ ] `gofmt -l .` lists nothing
- [ ] Monitor lint and DOM tests pass (when `internal/monitor/web/` or `frontend/` changed)
- [ ] `scripts/v2/Test-V2Manifest.ps1` passes (when `config/` changed)

<!-- Sanitized result summary: counts, timings, hashes. No prompts, responses, or credentials. -->

## Provenance

<!-- New or changed dependencies, runtimes, or model artifacts, with pinned versions and SHA-256. Write "none" otherwise. -->

## Migration and rollback

<!-- How a deployed canary moves to this change, and back. -->

## Documentation

- [ ] `CHANGELOG.md` updated under `Unreleased`
- [ ] ADR added or amended (trust boundary, public API, autonomy rule, deployment mechanism, or compatibility promise)
- [ ] README or docs updated
