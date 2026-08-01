# Contributing

## Principles

- Keep the provider local-only, stateless, fail-closed, and independent from harness agent logic.
- Prefer an upstream capability to custom protocol translation.
- Preserve v1 files until the documented observation period and explicit archival decision are complete.
- Never mix unrelated user work into a change.

## Repository hygiene

Do not commit:

- GGUF weights, runtime/release binaries, generated installation files, caches, logs, state, dumps, or benchmark prompts/responses.
- Credentials, authorization headers, cookies, user session material, or private filesystem captures.
- Unpinned download URLs such as `latest` or a mutable branch revision.

Models and runtimes are external artifacts referenced by exact path, byte size, SHA-256, source revision, and license in `config/models.yaml`.

## Development workflow

1. State the behavior and security boundary being changed.
2. Add or update unit/contract tests first for protocol or policy changes.
3. Keep public error shapes deterministic and content-free.
4. Run formatting, static analysis, vulnerability checks, manifest validation, and tests.
5. Update the changelog and an ADR when a trust boundary, public API, autonomy rule, deployment mechanism, or compatibility promise changes.

Local checks:

```powershell
.\scripts\v2\Test-V2Manifest.ps1
gofmt -w .
go test ./...
go vet ./...
staticcheck ./...
govulncheck ./...
```

Review `gofmt` changes before committing. PowerShell operational scripts must be preview-only by default for generated files, credentials, scheduled tasks, ACLs, firewall rules, or process starts. State-changing behavior requires an explicit `-Apply`, `-Run`, or equivalent operator action.

## Manifest changes

- A candidate may be added to canary only.
- Final deployment requires complete qualification evidence described in `docs/MODEL_PROMOTION.md`.
- An environment may deploy several models; the router keeps at most one loaded and one inference active.
- Changing any artifact, runtime flag, context, template, quantization, or revision requires requalification.
- Generated llama-swap files are never edited as source.

## Pull request evidence

Include:

- Intent and affected trust boundary.
- Tests executed and sanitized result summary.
- Artifact/dependency provenance changes.
- Migration and rollback behavior.
- Documentation/ADR updates.

Do not include secrets or sensitive logs even in private pull requests.
