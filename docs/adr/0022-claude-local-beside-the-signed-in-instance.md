# ADR 0022: Claude Local runs beside the signed-in instance; harnesses reach the server only on request

## Status

Accepted, 2026-10-01. Amends ADR 0017 (the restart-based switch between 1P and
3P), ADR 0005 (allowlisted Codex and OpenCode launchers in the panel) and ADR
0021 (the Codex launch kept as a tray button).

## Context

The operator works in the signed-in Claude Desktop, including Claude Code
sessions inside it, and wants a second Desktop on this server at the same
time. ADR 0017's switch could not provide that: it wrote the 3P selector, then
closed every process of the Claude MSIX (WM_CLOSE, then TerminateProcess) and
started it again in the other mode. Choosing "Local" ended whatever the
operator was doing in Desktop, including the session making the change.

Reading Desktop 2.16120's main process showed why both can run. Each process
reads `deploymentMode` from `%LOCALAPPDATA%\Claude-3p\claude_desktop_config.json`
once at startup and caches it; in 3P it sets its Electron user-data directory
to `Claude-3p` before `app.requestSingleInstanceLock()`, whose lock belongs to
that directory. A 1P and a 3P process therefore hold different locks.
`CLAUDE_USER_DATA_DIR` is deleted in packaged builds unless a signed CDP token
is present, so the selector is the only supported way in. Nothing in the
running app watches the selector: its own writes to that file read, modify and
write it under a mutex.

Measured on 2026-10-01: with the signed-in instance running, the selector was
set to `3p`, the AUMID activated, and a second main process started with eight
helpers under `Claude-3p`; the selector went back to `1p` and the signed-in
main process kept its PID. The new instance discovered the gateway's models and
selected the public model through its opaque alias.

Separately, the tray offered Codex and OpenCode buttons that opened a whole
harness session on a local model. The operator wants both harnesses on their
own providers, reaching this server only when asked through `/local`.

## Decision

- The tray offers `Claude Local` (amended the same day: the `Claude` button
  that foregrounded the signed-in instance was removed; the Start menu does
  that, and `cia-tray -claude-open` remains for scripts). `Claude Local` writes the CIA
  gateway profile when it differs, holds the selector at `3p` for one launch,
  waits for the 3P instance's window and returns the selector to `1p`, whatever
  happened. When a 3P instance already runs, it only foregrounds it. `Claude`
  returns a leftover `3p` selector to `1p` and foregrounds or starts the
  signed-in instance.
- Amended 2026-10-02: the flyout explicitly offers `Claude oficial` and
  `Gateway local`. The official option remains available when the local
  server is offline; it opens the signed-in instance and resets a leftover
  selector to `1p`. Windows keeps its native `Claude` app entry for Anthropic;
  the only added option is `Claude Local`, on the Desktop and in the Start
  menu, calling the existing `-claude-local` action with the original Claude
  logo. The shortcut generator backs up and removes its earlier duplicate
  `Claude oficial` and `Claude Gateway` links without modifying the native app.
- Before opening, `Claude Local` loads the first published model when no
  model is loaded: Desktop's start-up health check sends a one-token request
  to it with a ten-second budget, and a cold load takes about twenty.
- Instances are identified by the `--user-data-dir=` of their helper
  processes, read with `ProcessCommandLineInformation`, and only for processes
  whose executable belongs to the discovered package.
- `internal/claudedesktop` loses its restarter, its stop path and its
  verifier. No CIA code path can stop a Claude Desktop process.
- The Codex and OpenCode tray buttons, `panel.Launcher` and the capability
  checks behind them are removed. `launchers` in a generated panel
  configuration is still read, unvalidated, so earlier configurations and a
  rollback keep loading. The launcher scripts remain manual CLI entry points.
- `cia-tray -claude-mode/-apply` become `-claude-open` and `-claude-local`;
  `Configure-ClaudeDesktop.ps1` takes `-Instance Anthropic|Local`.

## Consequences

- Opening Claude Local no longer waits for the queue to drain: it interrupts
  nothing.
- Desktop rewrites the selector's file with its own preferences, notably
  when a new window first shows, the moment the selector goes back to `1p`.
  OpenLocal re-checks it for two seconds and puts `1p` back over a stale
  write; a preference Desktop writes in that instant can be lost.
- The selector is `3p` for a few seconds per launch. A Start-menu launch in
  that window opens or shows the local instance instead of the signed-in one.
- A running local instance keeps the profile it started with; a rotated
  gateway credential applies from its next start.
- Both instances show the same Desktop icon in the notification area; quitting
  from the wrong one ends that instance.
- On the gateway, a capacity refusal is `400 invalid_request_error` with the
  shortfall and the largest memory holders, because Desktop retries a `503`
  ten times without showing why. The OpenAI routes keep their `503` contract.
- The coexistence depends on Desktop's startup order (selector, user-data
  directory, lock). A Desktop release that changes it would make the 3P
  launch hand its activation to the signed-in instance, which is visible and
  harmless; the side-by-side smoke in `docs/CLAUDE_DESKTOP.md` detects it.
