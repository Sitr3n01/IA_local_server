# Changelog

All notable changes are documented here. This project follows Keep a Changelog conventions and will use semantic versioning once v2 leaves canary.

## [Unreleased]

### Added

- Go-based v2 edge, MCP, and Windows Credential Manager helper foundations.
- Single provenance- and checksum-based model/runtime manifest with JSON Schema.
- llama-swap v240 template with lazy loading, 900-second TTL, router authentication, no aliases, one active model, and no retained log buffer.
- Canary/final configuration generator and hidden scheduled-task launchers, all preview-only by default.
- Installation, artifact, listener, side-effect, and secret-pattern verification scripts.
- Architecture, threat model, runbook, promotion, benchmark, security, contribution, licensing, and ADR documentation.
- Windows CI for Go quality gates, PowerShell parsing, manifest validation, vulnerability analysis, and secret scanning.
- Native Win32 `cia-tray` operator panel with live status, explicit lifecycle
  operations, atomic selected-model state, capability-aware harness launchers,
  single-instance protection, and no new listener or UI framework dependency.
- Supported Unsloth canary/final launchers that leave private Studio state
  untouched and never restore the retired external connection.
- Approved-hash, atomic installers for staged Edge/MCP/tray binaries and an
  approved-plan transactional harness installer.
- Separate `cia-mcp-inference` bridge with one bounded `local_ai_delegate`
  tool, direct Credential Manager authentication, and provider-preserving
  global integrations for SOTA Codex, Claude, and OpenCode sessions.
- Metadata-only MCP live probe, DPAPI-backed transactional client registration,
  and an idempotent current-user Startup shortcut for the native panel.
- Python quality-eval and long-context/tool-call stress-eval scripts
  (`scripts/run-profile-quality-eval.py`, `scripts/run-profile-stress-eval.py`),
  mirroring the existing PowerShell quality harness for cross-language reuse
  and adding a needle-in-haystack plus forced-tool-call test the PowerShell
  suite did not have.

### Fixed

- Tray dashboard `Abrir Codex`/`Abrir OpenCode` only enabled for the model with
  the *persisted* selection (`Selecionar`) instead of whichever model was
  highlighted in the list; launch now targets the highlighted row.
- Codex canary launcher's console closed instantly on completion or error,
  hiding any output, because `powershell.exe` ran without `-NoExit`. Confirmed
  `codex.exe` is a genuine CLI that needs a real console (not a packaged-app
  shim) and restored `CREATE_NEW_CONSOLE` for it.

### Changed

- Removed `Abrir Codex` from the tray dashboard and tray context menu: the
  unified ChatGPT desktop app's bundled Codex mode has no GUI provider picker
  for custom/local providers (tracked upstream as `openai/codex#29156`); the
  CLI-only path remains available via `Start-CodexLocalCanary.ps1`.
- Registered the five non-Ornith canary models with validated
  `chat_completions` support (Qwen 3.5 ×3, Gemma 4 12B ×2) in the Codex and
  OpenCode local-only catalogs, with per-model `reasoning`/`tool_call`
  capability flags; left the two unvalidated Gemma 4 31B variants out
  (`chat_completions`/`streaming` not yet confirmed for them).
- Added the local provider to the personal default OpenCode configuration
  alongside existing cloud providers, previously reachable only through the
  isolated canary launcher; the OpenCode Start Menu shortcut now routes
  through the credential helper so the local provider authenticates without a
  standing plaintext environment variable.
- Raised `cia-mcp-inference` output-token ceiling (4096 → 65536 tokens) and
  the edge's upstream response timeout (2 min → 30 min), which were silently
  truncating and timing out ordinary delegate responses; added a configurable
  `temperature` (default 0.2, previously unset).
- Raised `local-coding` KV cache precision (`q4_0` → `q8_0`) and weight
  quantization (`Q4_K_M` → `Q8_0`, 9.53 GB); ~10.8 GiB VRAM measured with the
  full 131072-token context resident (within the operator's stated headroom).
- Lowered `local-coding` weight quantization again in the source manifest
  (`Q8_0` → `Q5_K_M`, 6.47 GB) to recover memory headroom, keeping KV cache at
  `q8_0`/`q8_0`; publication to the installed canary is staged and still
  awaits an elevated `New-V2Config.ps1 -Apply` run. llama.cpp's own
  device-memory fit dropped from 10764 MiB to 8210 MiB (~2.5 GiB recovered) at
  the full 131072-token context. A same-conditions A/B against the prior
  `Q8_0`/`q8_0` config (identical port, context, and quality-eval battery)
  showed no regression: 4/4 on the instruction-following/arithmetic/
  list-reasoning/config-recall suite with reasoning enabled, and a matching
  pass on a new long-context stress test (106745-token haystack, a fact
  planted at ~50% depth, correctly retrieved and reused as a forced
  tool-call argument).
- Removed the two never-validated Gemma 4 31B candidates (`gemma4-31b-q4km`,
  `gemma4-31b-ud-q4xl`; `chat_completions`/`streaming` had never been
  confirmed for either) and the superseded `Ornith 1.0 9B Q4_K_M` long-context
  test profiles from the manifest and test matrix after deleting their weight
  files (~39.8 GiB reclaimed); regenerated the Codex/OpenCode local-only
  catalogs from the cleaned manifest and updated canary validation/hash state
  to match.
- Documented the pinned executor model's demonstrated limitations and a
  capability rating directly in the `local_ai_delegate` tool description and
  `integrations/mcp-inference/README.md`, based on repeated canary testing.

### Security

- Removed cloud fallback and client-authorization forwarding from the v2 design.
- Separated inference, administration, and router credentials.
- Established metadata-only logging, fail-closed routing, loopback-only listeners, decompression limits, and explicit incident handling.
- Removed administrative credentials from status polling and default read-only
  MCP registrations; mutations read the admin secret only on demand.
- Added bounded switch completion after client cancellation, Explorer tray-icon
  recovery, idempotent panel startup, and rollback-safe elevated publication.
- Kept inference delegation isolated from the read-only and administrative MCP
  surfaces; endpoint/model are pinned, redirects/proxies are rejected, and no
  client configuration contains an inference credential.

### Migration

- v1 remains untouched and is not a v2 dependency.
- `local-coding` is a canary candidate only; final generation remains blocked until qualification.
- `local-fast` is recorded but disabled.
