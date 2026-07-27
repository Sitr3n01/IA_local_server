# ADR 0007: Multimodel discovery and hidden Unsloth boundary

## Status

Accepted for v2 canary.

## Decision

Allow several manifest models in one environment and generate an independent
`llama-swap` block for each. The router remains the lifecycle authority and may
load at most one model. Edge publishes the environment allowlist and preserves
the existing top-level status fields while adding a per-model collection.

The native panel recursively discovers GGUF files below `C:\IA\models` and
operator-selected roots. It resolves paths, rejects traversal through reparse
points, deduplicates files, caches hashes and validation state, and never removes a GGUF
when a root is removed. Unknown files are visible but cannot be loaded until
their provenance, metadata, resource profile, and real generation pass.

Unsloth is not a v2 user-interface or startup surface. No Unsloth label, action,
launcher, or automatic process remains in the panel. The existing installation,
scripts, models, private state, disabled legacy shortcuts, and candidate runtime
are preserved for manual offline work.

## Consequences

- Partial model failure has an explicit per-model reason and does not remove the
  provider's last working deployment.
- Codex/OpenCode actions are enabled only for models whose declared wire and
  agent contracts have passed.
- Additional roots are opt-in and persisted under protected v2 state; there is
  no whole-disk scan.
- The panel starts hidden through `wscript.exe` and a VBS wrapper. Tray and
  supervisor binaries use the Windows subsystem, and auxiliary children use
  no-window process creation flags.
