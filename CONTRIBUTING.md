# Contributing

Security problems are not reported through issues or pull requests: follow
[SECURITY.md](SECURITY.md). Everyone taking part follows the
[code of conduct](CODE_OF_CONDUCT.md).

## Principles

- Keep the provider local-only, stateless, fail-closed, and independent from harness agent logic.
- Prefer an upstream capability to custom protocol translation.
- Never mix unrelated user work into a change.

## Repository hygiene

Do not commit:

- GGUF weights, runtime/release binaries, generated installation files, caches, logs, state, or dumps.
- Real prompts or responses — anything a user, harness, or session produced.
- Credentials, authorization headers, cookies, user session material, or private filesystem captures.
- Unpinned download URLs such as `latest` or a mutable branch revision.

Evaluation evidence under `benchmarks/` is the one place model output belongs.
It is generated from the synthetic fixtures in `scripts/v2/eval/`, so it may
hold those fixtures' prompts and the models' answers to them, and nothing else.

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
gofmt -l .
go test ./...
go vet ./...
staticcheck ./...
govulncheck ./...

# Monitor page
cd frontend; npm ci; npm run lint:monitor; npm run test:monitor
```

`gofmt -l` lists files that need formatting; run `gofmt -w` on them and review
the result before committing. The race detector needs cgo, so CI runs
`go test -race ./...` on Linux; run it under WSL, or on Windows with
`CGO_ENABLED=1` and a C compiler on `PATH`.

PowerShell operational scripts must be preview-only by default for generated files, credentials, scheduled tasks, ACLs, firewall rules, or process starts. State-changing behavior requires an explicit `-Apply`, `-Run`, or equivalent operator action.

## Commits

Subjects follow [Conventional Commits](https://www.conventionalcommits.org/):
a type (`feat`, `fix`, `docs`, `test`, `refactor`, `ci`, `chore`, `bench`), an
optional scope naming the component, and an imperative summary — for example
`fix(adminpipe): bound pipe I/O by the caller and reject trailing data`. The body explains why
the change is needed and what it was measured or tested against.

## Manifest changes

- A candidate may be added to canary only.
- Final deployment requires complete qualification evidence described in `docs/MODEL_PROMOTION.md`.
- An environment may deploy several models; the router keeps at most one loaded and one inference active.
- Changing any artifact, runtime flag, context, template, quantization, or revision requires requalification.
- Generated llama-swap files are never edited as source.

## Pull requests

The pull request template asks for the evidence below. All six CI checks must
pass before a change merges into `main`.

- Intent and affected trust boundary.
- Tests executed and sanitized result summary.
- Artifact/dependency provenance changes.
- Migration and rollback behavior.
- Documentation/ADR updates.

Do not include secrets or sensitive logs even in private pull requests.

## Releases

Versions follow [Semantic Versioning](https://semver.org/). While v2 is in
canary, releases are pre-releases such as `v2.0.0-canary.1`. Pushing a `v*` tag
runs [release.yml](.github/workflows/release.yml), which re-runs the gates,
builds the Windows binaries, and attaches them with an SBOM and `SHA256SUMS` to
a GitHub release; its notes come from `docs/releases/<tag>.md` when that file
exists. Move the changelog's `Unreleased` entries under the new version in the
same change that adds the notes.
