# ADR 0013: Production artifact boundary

## Status

Accepted. Supersedes the decision recorded in ADR 0008.

## Context

`Set-V2Acl.ps1` hardens exactly `C:\IA\local-ai-v2`. Every GGUF and every
runtime build referenced by `config/models.yaml` lives outside it — under
`C:\IA\models`, `C:\IA\runtimes`, `C:\IA\local-llama\amd\...`, and
`C:\Users\Sitr3n\.unsloth\...`. Those files are writable by the ordinary user,
which means a production deployment would depend on bytes anyone logged in can
replace between two runs. `RUNBOOK.md` and `THREAT_MODEL.md` both named this a
final-cutover blocker.

ADR 0008 proposed hardening those paths in place and explicitly rejected
relocation, because relocating would invalidate every SHA-256 pinned against the
current paths across `models.yaml`, the source snapshot, the validation state,
and the client catalogs.

That rejection assumed relocation means *re-pinning*. It does not have to.

A second problem the in-place approach cannot solve: candidate artifacts are
supposed to churn. Qualification produces new quantizations, retires old ones,
and moves files around. Making the candidate tree immutable fights the workflow
it exists to support, and an operator who has to unprotect it to run the next
campaign will leave it unprotected.

## Decision

Separate the two roles instead of trying to give one location both.

**Candidate artifacts** stay where they are produced. They remain writable,
remain discoverable by the panel, and are never modified, moved, or deleted by
any deployment operation.

**Production artifacts** are verified *copies* of the same bytes inside the
protected installation:

```text
C:\IA\local-ai-v2\artifacts\models\<model-id>\<file>
C:\IA\local-ai-v2\artifacts\runtimes\<runtime-id>\<runtime directory>
C:\IA\local-ai-v2\artifacts\published.json
```

Three properties make this cheap rather than disruptive:

1. **The manifest is not rewritten.** Identity stays SHA-256 plus byte size. The
   production path is *derived* from the artifact identifier and the source file
   name, so no pinned hash, source snapshot, qualification record, or client
   catalog changes. This is what ADR 0008 was right to want to avoid and wrong
   to think relocation required.

2. **Only Final resolves production paths.** Canary keeps reading candidates in
   place, which is what a canary is for. `New-V2Config.ps1` records which of the
   two it used in the deployment marker as `artifact_source`.

3. **Publication is a copy through staging.** Size and SHA-256 are verified in
   the source, again in the staging copy, and again at the destination.
   Publication is atomic (`File.Replace` for a model, a directory rename for a
   runtime), refuses reparse points anywhere on the path, refuses any target
   that escapes the artifact root, and aborts if the destination changed between
   preflight and publish.

A runtime is published as its whole directory, not as the pinned executable
alone: `llama-server.exe` cannot load without the backend libraries beside it,
and a production tree containing only the executable would silently fall back to
CPU or fail to start.

`artifacts` is deliberately absent from the runtime-writable set in
`Set-V2Acl.ps1`. The serving user gets read and execute; Administrators and
SYSTEM keep full control for recovery.

## Consequences

- Final generation fails until every deployed model and runtime has been
  published. That is the intended gate, and it replaces a documented blocker
  with an executable one.
- Publishing a large GGUF costs disk (a second copy) and time (a full SHA-256
  over both copies). Both are accepted: the alternative is production depending
  on a user-writable file.
- The candidate tree needs no ACL change at all, so qualification campaigns keep
  working exactly as they do today.
- `published.json` records what was published, but it is not authority: the
  production path is always re-derived, and a record pointing anywhere else is
  refused. Editing the index cannot redirect a deployment.
- Retiring a model does not remove its published copy. Reclaiming that space is
  a deliberate, separate operator action; nothing deletes weights automatically.
