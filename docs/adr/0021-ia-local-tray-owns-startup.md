# ADR 0021: IA Local — one startup entry and a flyout in the monitor's design

## Status

Accepted, 2026-09-30. Amends ADR 0004 (the tasks' own logon trigger) and ADR
0005 (the native menu, the control window, model folders and validation, and
"closing the panel has no effect on serving").

## Context

The operator asked for three things: to switch the system's start at logon on
and off from Task Manager, to have the tray icon present whenever the system
runs, and to have the tray follow the browser monitor's design instead of
Windows' default menu and controls, with every tray function reviewed so that
none is a ghost, broken, or out of scope.

What was installed did not allow any of it:

- The tray started from a Startup-folder shortcut whose target was
  `wscript.exe` running `launchers\tray-canary.vbs`. Task Manager names a
  startup entry after its target, so it listed "Microsoft Windows Based Script
  Host" with wscript's icon, and the entry was disabled there.
- Router and Edge started from their own logon triggers (ADR 0004), so no
  Task Manager entry controlled the server, and the server could run with no
  icon at all.
- The installed tray did not start: the saved selection named
  `gemma4-12b-qat-ud-q4xl`, retired in the roster round, and the selection
  store failed closed at startup.
- The executable had no icon or version resource and no DPI manifest.

The function review found:

| Function | Finding |
|---|---|
| Codex launch | Ghost: implemented in `internal/panel`, never offered by any menu or button, yet a missing Codex launcher script stopped the tray from starting |
| Model-menu selection (`commands` map) | Ghost: the map was never filled |
| Tooltip | Never shown: NOTIFYICON_VERSION_4 requires `NIF_SHOWTIP` |
| Win32 control window | Duplicated the monitor's model list with default controls; its status line hard-coded "AMD Radeon RX 9070 XT" |
| Model folders, GGUF detection and "Validar" | Dead end: a detected GGUF can never be loaded without a manifest profile, and validation hashed multi-GB files from the tray; validating a registered model also switched the loaded model |
| "Atualizar", "Detalhes do status" | Redundant with the periodic refresh and a status message box |
| "Fechar painel" | Mislabelled: it exited the tray |
| Claude gateway check | Sent a discovery request with the gateway key on every 10 s refresh |
| README: "drain, resume and the rest of the lifecycle stay with cia-tray" | False: the tray never offered drain or resume |

## Decision

### 1. The tray is the system's one startup entry

`Install-V2PanelStartup.ps1` writes `HKCU\...\Run\CIA Local AI v2 <Env>` =
`"<root>\bin\cia-tray.exe" -config "<root>\config\panel.<env>.json"`, adds an
"IA Local" Start-menu shortcut, and removes the legacy wscript shortcut and its
Task Manager entry when it recognises them as this installation's. It reports,
and never changes, a choice the operator made in Task Manager.

`cia-tray.exe` carries an icon and a version resource whose FileDescription is
"IA Local" (`cmd/cia-tray/winres`, packed by go-winres into a committed
`.syso`), so Task Manager lists the entry as "IA Local" with the IA Local mark.
The manifest declares per-monitor-v2 DPI awareness.

`Install-V2ScheduledTasks.ps1` registers Router and Edge without a trigger. The
tray starts them when it opens, through the Task Scheduler COM API, running
only a task that is neither running nor queued; "Encerrar" ends the monitor it
started, then Edge, then Router, after an in-flyout confirmation that says how
many requests would be interrupted. Everything ADR 0004 bought is unchanged:
the task's action is still `cia-supervisor.exe`, its kill-on-close job still
takes the whole serving tree down, and restart-on-failure still applies.

Disabling "IA Local" in Task Manager therefore means nothing of IA Local starts
at logon, and opening IA Local means the system runs.

### 2. A flyout drawn from the monitor's tokens

Clicking the icon (left or right, or Enter on it) opens one flyout: a
borderless topmost popup beside the taskbar, closed by losing activation or
Esc, like the system flyouts. It is drawn natively — GDI+ for antialiased
shapes, GDI with ClearType for text — from a palette that mirrors
`internal/monitor/web/assets/tokens.css` (`internal/trayui/theme.go`), with the
same components: brand mark, environment chip, status pill with halo, state
card, notices, radio list, primary/secondary/danger buttons, and a segmented
control drawn like the page's tabs. It follows the Windows app theme, scales per
monitor, and supports Tab, arrows, Enter/Space and a focus ring.

It is not a web view. There is no listener, no browser runtime and no DOM, so
none of ADR 0018's controls are needed; the tray keeps ADR 0005's trust model.
The icon itself is the mark, coloured by state: brand green when healthy, blue
while working, amber when degraded or waiting, red when offline.

### 3. What the tray does now

| Kept | Added | Removed |
|---|---|---|
| Status of edge, router, capacity, loaded model, queue | Start the server when the tray opens, and a button when it is offline | Win32 control window |
| Select, load, switch, unload (edge-validated, as before) | "Encerrar": stop the server and the tray, confirmed | Model folders, GGUF detection, validation and `-validate-model` |
| Open OpenCode with the selected model | Open Codex (the ghost made real) | "Atualizar" and "Detalhes do status" |
| Claude Desktop: open, Anthropic/Local mode | "Abrir painel": starts the monitor on demand and opens it | Model submenu map, "Fechar painel" |
| `-diagnose`, `-claude-mode`, `-claude-open` | Notifications for results that finish while the flyout is closed | |

The monitor runs in a job owned by the tray, with kill-on-close and silent
breakaway: it ends with the tray, and the browser it opens is not in the job.
A second start of IA Local opens the running tray's flyout.

### 4. Starting cannot be blocked by stale state

A saved selection that is no longer served, or unreadable, falls back in
memory to `provider.public_model` and the flyout says so; the file is not
rewritten until the operator selects a model. A missing launcher script fails
only its own button. The Claude gateway check runs at most once a minute, and
again right after a mode change.

## Consequences

- Only the tray starts the server automatically. Starting it without the tray
  is `Start-ScheduledTask`; the next time IA Local opens it finds the tasks
  running and leaves them alone.
- If the tray crashes, the server keeps running under its supervisor, and the
  monitor it started ends with it. Reopening IA Local reattaches.
- A release must install the tray and re-register the tasks together: a
  trigger-less task set with the old tray would leave nothing to start the
  server at logon.
- `model_roots_path` and `validation_path` stay accepted in the panel
  configuration so existing files load; nothing reads them, and
  `New-V2Config.ps1` no longer writes them.
- The flyout's layout is tested offscreen in both themes at 100 and 150
  percent: every control inside the window, no live controls overlapping, and
  every button wide enough for its label.
- The tray reads one credential on its own, the Claude gateway key, to check
  the loopback gateway at most once a minute. The administrative credential is
  still read only for an explicit load, switch or unload.
