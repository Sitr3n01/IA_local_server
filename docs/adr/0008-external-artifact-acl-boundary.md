# ADR 0008: ACL boundary for external runtime and GGUF paths

## Status

Proposed.

## Context

`Set-V2Acl.ps1` hardens exactly the `C:\IA\local-ai-v2` root: 34 immutable targets plus two
runtime-writable directories (`logs`, `state`), owner `Administrators`, `Sitr3n` granted
`ReadAndExecute` (`Modify` only under the two writable directories). A preview run on 2026-07-30
confirms 36 targets and exactly one non-compliant item within that boundary:
`config\deployment.canary.json` (`inheritance_enabled`, `dacl_differs`) — unrelated to this ADR,
fixable today with the existing `-Apply` from an elevated shell.

Outside that boundary, three artifact locations carry no ACL protection at all:

- `C:\IA\local-llama\amd\llama_cpp_b8407_rocm_7.2.1\...\llama-server.exe` (AMD ROCm baseline
  runtime, pinned in `models.yaml`).
- `C:\Users\Sitr3n\.unsloth\llama.cpp\build\bin\Release\llama-server.exe` (Unsloth candidate
  runtime, also pinned in `models.yaml`).
- `C:\IA\models\` — 42 GB across six model families (Ornith 1.0 9B, Qwen 3.5 4B, Qwen 3.5 9B,
  Gemma 4 12B QAT ×2, Gemma 4 31B), each referenced by exact SHA-256 in
  `model-validation.canary.json` and the model manifests.

Egress firewall coverage is not the gap: the `Set-V2Firewall.ps1` preview already lists both
external `llama-server.exe` paths as outbound-block targets (rules 10–11). This ADR is about
filesystem ACL/ownership only.

`LOCAL_AI_ARCHITECTURE.md`'s own "Current authority" section already lists `C:\IA\models` as a
canonical location separate from the installed canary at `C:\IA\local-ai-v2` — the split is
original intent, not an oversight.

## Decision

Extend exact-target ACL hardening to the three external locations **in place**, rather than
relocating 42 GB of pinned artifacts into `local-ai-v2`. Concretely:

- A second script, mirroring `Set-V2Acl.ps1`'s exact-target/non-reparse/preview-then-elevated-apply
  shape, targets exactly: `C:\IA\models` (recursive), the AMD ROCm runtime directory, and the
  Unsloth runtime directory.
- Owner: `Administrators`. `Sitr3n`: `ReadAndExecute` only — none of these three targets are
  written at runtime (GGUF weights are read-only inputs; `llama-server.exe` is executed, not
  modified), so no `Modify`/runtime-writable subpath is needed here, unlike `local-ai-v2/state` and
  `/logs`.
- Same audit/recovery discipline as the existing script: preview by default, `-Apply` only from an
  elevated shell, pre-change SDDL backup, post-apply `-Audit`.

**Rejected alternative:** relocating these paths under `C:\IA\local-ai-v2`. This would require
re-verifying and re-pinning every SHA-256 currently recorded against the *current* paths across
`models.yaml`, `models.source-snapshot.json`, `model-validation.canary.json`, and the Codex model
catalog, plus updating every launcher/config that references them — a much larger blast radius for
a problem that a same-location ACL script fully resolves. It would also contradict the canonical
path separation `LOCAL_AI_ARCHITECTURE.md` already documents.

## Consequences

- Closes the "external runtime and GGUF paths" cutover blocker named in `RUNBOOK.md` and
  `THREAT_MODEL.md`, without touching any pinned hash, manifest path, or launcher.
- Still requires an elevated apply and independent audit, same operator discipline as the existing
  script — not something to automate without the same preview/review/apply separation.
- Does not address the existing, unrelated `deployment.canary.json` non-compliance inside the
  current boundary; that's fixed by running the existing `Set-V2Acl.ps1 -Apply` and is not gated on
  this ADR.
- Does not attempt to unify all artifacts under one ACL root — that remains a possible future
  decision, out of scope here.
