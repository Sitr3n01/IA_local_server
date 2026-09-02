# cia-console

`cia-console.exe` is a native Windows host process that displays a WebView2
control rendering the operator console frontend (`frontend/`, a React and
TypeScript app served over a virtual host mapping, no listening socket
involved). It is a Phase A spike per
[ADR 0018](../../docs/adr/0018-webview-operator-console.md): additive to
`cia-tray.exe`, which remains the supported operator panel until the console
passes its own gate.

The package is split as:

- `main_windows.go` - the real Win32 window, WebView2 environment, virtual
  host mapping, and the one-shot load self-check (`CIA_CONSOLE_DIAGNOSE`).
- `env_windows.go` - ADR 0018 control 2: asserts webviewloader's `init()`
  has neutralized every `WEBVIEW2_*` ambient-environment override before
  this process ever creates a WebView2 environment.
- `bridge.go` - the portable decision point between the page's web messages
  and the credential-free/DACL-protected clients (ADR 0018 control 1: every
  mutation requires a native, non-DOM confirmation). No Windows imports, so
  it builds and its tests run on the `ubuntu-latest` race job too.
- `e2e_windows_test.go` - see below.

## End-to-end regression harness

`e2e_windows_test.go` exists because a window that opens, creates its WebView2
environment, completes navigation, mounts React, and shows nothing produces no
error anywhere - and because a page cannot observe whether it is being
composited, so the console's own self-report is incapable of describing that
state.

Four causes were suspected. Each was isolated by removing it and re-running:

1. The virtual host's `.localhost` TLD, thought to compete with
   `SetVirtualHostNameToFolderMapping` via Chromium's built-in loopback
   resolution. **Refuted** - the page renders identically either way. The host
   is `cia-console.invalid` (RFC 2606) today because that TLD cannot collide
   with a browser's resolution rules - a deliberate choice, not a fix.
2. Vite's `<script crossorigin>`/`<link crossorigin>` attributes, thought to
   put same-origin asset fetches in CORS mode and trip the host's
   `COREWEBVIEW2_HOST_RESOURCE_ACCESS_KIND_DENY_CORS` mapping. **Refuted** -
   measured `scripts: 1, stylesheets: 1` with the attributes present.
3. A controller left at zero bounds because a window created at its final size
   raises no `WM_SIZE`. **Refuted** - `Embed` already sizes the controller; the
   viewport measured 1264x821 with no explicit resize. The `Resize()` call
   added for this was removed again.
4. The controller never being made **visible**: `Embed` leaves
   `ICoreWebView2Controller::IsVisible` false, so its child windows exist at
   the correct size and composite nothing. **Confirmed** - the entire cause.
   Fixed with `chromium.Show()`.

Three of four suspected causes were wrong, and all three looked plausible. That
is why this file asserts on OS-level child-window visibility rather than on
anything the page reports about itself.

A second, unrelated failure mode is guarded here too: a status reply that is
correlated, `ok: true`, and *incomplete*. Routing the console's read through a
narrower Go struct silently dropped a third of cia-edge's snapshot, the page's
schema validation rejected what arrived, and the operator saw an error card
while every host-side signal reported success. See ADR 0018 for the account;
the assertions it produced are `bridge_status_arrives_intact` (the key sets
that reached the page versus what the page's schema requires) and
`page_renders_the_status_it_received` (the settled document carries no
`role="alert"` and no `aria-busy` region). The pre-existing
`bridge_status_round_trip` passes in both the healthy and the broken case,
which is precisely why the other two exist.

The harness builds the real binary, launches it with the one-shot load
self-check enabled and with ADR 0018's own hostile environment recipe
(`WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=<port>` and
`WEBVIEW2_PIPE_FOR_SCRIPT_DEBUGGER=1`), waits for the self-check line on
stderr, and then checks five kinds of thing: the self-check JSON itself; the
live Win32 window tree (via `golang.org/x/sys/windows` - `EnumWindows`,
`EnumChildWindows`, `IsWindowVisible`), because a controller that composites
nothing but a fully-loaded, fully-mounted page behind it is exactly what the
in-page self-check *cannot* see; Sprint 9's settled layout geometry and
nonblank bitmap summary of the visible WebView2 content; whether anything is
listening on the hostile debug port; and whether the console process itself
owns any listening TCP socket at all, queried directly from the OS's owner-PID
TCP table (`GetExtendedTcpTable`, no `netstat` shelled out).

**What this proves, and what it does not:** a pass means the window loaded,
navigated to the right place, mounted React, sized and composited its
WebView2 control, rendered the default Overview layout without horizontal
overflow, captured a nonblank/nonmonochrome bitmap from the visible content,
and opened no port anywhere. It does **not** mean the console looks exactly
right: there is no golden screenshot, OCR, or pixel-perfect comparison.

The diagnose payload does report `document.scripts.length` and
`document.styleSheets.length`, so the `page_assets_loaded` subtest asserts both
directly rather than skipping. That assertion is what refuted suspected cause 2
above: the counts stayed at 1 with the `crossorigin` attributes in place.

### Running it

The harness is skipped by default - `go test ./cmd/cia-console/...` never
runs it - because it needs an interactive desktop session (WebView2 will not
compose onto a headless runner or a Windows service session) and a built
frontend, neither of which exists in CI. To run it:

```
cd frontend && npm install && npm run build   # produces frontend/dist
cd ..
CIA_CONSOLE_E2E=1 go test ./cmd/cia-console/... -run TestConsoleE2E -count=1 -v
```

`-count=1` is not optional. `go test` caches a passing result and replays it -
log output and all - whenever it believes the inputs are unchanged, and it
cannot see this harness's real inputs: a live WebView2 runtime, an interactive
desktop session, and the contents of `frontend/dist`, which only the child
process reads. A replayed pass is indistinguishable from a real one in the
output and means nothing, because no window was ever opened. Three consecutive
attempts to prove an assertion had teeth - by deliberately breaking the code
under it - reported PASS from cache without launching anything, which reads as
"the assertion is useless" when the truth is "the test never ran".

It builds its own copy of `cia-console.exe` into a temp directory - it does
not touch anything already built in `frontend/` - launches it, and tears
down the process and every WebView2 helper process it spawned (via a
Windows Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`) in
`t.Cleanup`, whether the test passes, fails, or panics.
