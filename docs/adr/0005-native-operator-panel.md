# ADR 0005: Native, thin Windows operator panel

## Status

Accepted for v2 canary.

## Decision

Provide `cia-tray.exe`, a native Win32 notification-area controller. It uses
the installed manifest for model metadata, a public sanitized edge snapshot for
runtime truth, Windows Credential Manager for administrative mutations only,
and allowlisted PowerShell launchers for Codex and OpenCode.

The panel stores only the selected model for future launches. It does not own
the loaded model, infer a model from a client UI, manipulate existing sessions,
start a second inference runtime, expose an HTTP UI, or read serving logs.

The panel may display several deployed models plus discovered candidates, but it
cannot promote or bypass their deployment/capability gates. It stores explicit
model roots and validation summaries without deleting or rewriting model files.

## Rationale

A notification-area menu is sufficient for the personal Windows workflow and
adds no browser server, frontend dependency graph, CORS surface, or new port.
Using the existing control contracts preserves `cia-edge` and `llama-swap` as
the lifecycle authorities. Explicit launchers make provider and model choice
observable without changing normal cloud defaults.

## Consequences

- A cold lifecycle operation runs on a worker and may take up to the generated
  operation timeout; Explorer remains responsive.
- Periodic refresh never sends a credential, and the icon restores itself after
  Explorer broadcasts `TaskbarCreated`.
- An active or queued inference blocks lifecycle changes and is never aborted.
- Existing Codex/OpenCode sessions stay pinned and must be relaunched to adopt
  a new selection.
- Unsloth is absent from the panel and v2 startup. Its installation and private
  persistence are not modified, and manual instances are never stopped.
- The panel is a convenience process, not a serving supervisor. Closing it has
  no effect on Router, Edge, or a loaded model.
