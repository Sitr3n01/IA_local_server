# Native Claude and the local gateway

A fix limited to how Claude Desktop is opened, 2026-10-02.

## Observed cause

The IA Local flyout exposed only the gateway, although the controller already
had a separate action for Anthropic's Claude. On this machine the selector
`%LOCALAPPDATA%\Claude-3p\claude_desktop_config.json` was set to `3p`, so a
generic launch of the package picked the gateway.

## Fix

- The flyout code offers `Claude oficial` and `Gateway local`. The official
  option does not depend on the local server: it needs only an installed
  Desktop and no other tray action in progress; the gateway keeps its
  prechecks.
- On Windows, the original `Claude` entry is the Anthropic option. Only
  `Claude Local` was added, on the desktop and in the Start menu under
  `IA Local`, calling `-claude-local` on the installed executable.
- The earlier `Claude oficial` and `Claude Gateway` shortcuts were removed
  from both locations, after a backup. The cleanup checks the target and the
  arguments, so any shortcut that does not belong to this integration is kept.
- `Claude Local` uses the original logo from the installed package, with its
  six PNG sizes preserved byte for byte in a per-user ICO file.
- The official flow returns the selector to `1p` before it opens or shows its
  instance. The gateway configuration remains available in its own profile.
- `scripts/v2/New-V2ClaudeShortcuts.ps1` reproduces the per-user creation,
  with a preview, idempotent reapplication, and a refusal to overwrite
  shortcuts that differ.

## Validation

After the requested change to keep only the native entry and the local one,
the desktop and Start-menu `Claude Local` shortcuts exited with code zero.
On Claude Desktop `2.19675.0.0`, the official instance kept PID `16012` and
the gateway opened as PID `12180`. Their helpers use `%APPDATA%\Claude` and
`Claude-3p` respectively. The local shortcut brought PID `12180` to the
foreground; activating the native entry brought PID `16012` to the foreground.
The selector ended at `1p`; the SHA-256 of the official account's
`claude_desktop_config.json` was unchanged. No Claude process was closed.

`Get-StartApps` returned only `Claude` and `Claude Local` as app entries with
those names. The four old copies were saved before removal. Windows decoded
the icon, and each of the six frames matched the SHA-256 of the original
installed PNG. Reapplying kept both shortcuts and the icon byte for byte; the
Windows PowerShell 5.1 run and the syntax check passed.

The tests for `cmd/cia-tray`, `internal/trayui` and `internal/claudedesktop`,
vet on those packages and the tray build passed. The light/dark theme renders,
at 96/144 DPI, passed the bounds, width and overlap checks. The new regression
test checks that, with the local server offline, the official button stays
enabled and the gateway button is disabled.

The shortcuts already work on this machine. The installed tray executable was
not replaced: the menu with both buttons is fixed in source and built for
review. The logs, the `native-*-proof.json` launch proofs, the verified
shortcuts and the build are in
`C:\IA\tmp\claude-dual-launch-20261002`.

## Icon fix in Windows Search

A later screenshot from the user showed `Claude Local` with a generic icon in
Search. The ICO file, the reference in both shortcuts and the images returned
by the Shell were correct, including through the `AppsFolder` catalog. That
points to a stale image in Search; the earlier validation did not prove the
visual result in that interface.

The generator now puts the content hash in the ICO file name, keeping the
logo's bytes unchanged. It also leaves an identical file untouched on
reapplication and sends `SHChangeNotify(SHCNE_UPDATEITEM, SHCNF_PATHW | SHCNF_FLUSH)`
for the icon and both shortcuts. That notification is the item-specific update
[documented by Microsoft](https://devblogs.microsoft.com/oldnewthing/20150903-00/?p=91671/).

Applied to the existing shortcuts under Windows PowerShell 5.1.
`IShellItemImageFactory` returned the Claude Local logo through the
`AppsFolder` catalog at 64, 96 and 256 pixels; the 256-pixel image was
inspected. Reapplying preserved the ICO file's content and timestamp, the
shortcuts kept their correct targets and arguments, and the catalog still
listed only `Claude` and `Claude Local`. The official configuration was
unchanged and the selector stayed at `1p`. No process was restarted and no
cache was cleared globally. The results are in `icon-refresh-validation.json`
and `icon-search-after-proof.json` in the evidence folder above. What Search
shows after the update depends on reopening it; the image was validated
through the Windows catalog API.
