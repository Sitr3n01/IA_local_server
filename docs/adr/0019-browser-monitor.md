# ADR 0019: A browser monitor replaces the console direction

## Status

Accepted, 2026-09-28. Supersedes the direction of ADR 0018: `cia-console.exe`
is no longer developed and is not deployed. Its code stays in the tree until the
operator decides to remove it; this record does not delete it.

Amended 2026-10-03: the operator removed `cia-console.exe`, its React frontend
and the frontend quality gate. `frontend/` now holds only this monitor's lint
and DOM tests.

Amended the same day. The first version made the monitor strictly read-only;
the operator then asked for two model controls on the page, and section 4 is
the decision that admits them. `cia-tray.exe` keeps every other administrative
operation.

## Context

The operator asked for a view of the server like the Monitor tab of
[Strata](https://github.com/Niko1221/Strata): one page, refreshed every second,
that says what the provider is doing right now — which model, which phase of a
request, how fast, on how much of the GPU. The WebView2 console of ADR 0018 did
not deliver that. It was judged broken and unpleasant to use, two of its five
controls (the pinned runtime and the npm SBOM) were never closed, and it put an
always-on browser engine on a workstation whose admission gate already refuses
models when ordinary desktop load rises.

The deeper gap was not the client. What the operator wanted to see was not
published anywhere. `/api/v1/status` carried capacity arithmetic, GPU pressure
and gate counts, but nothing about requests: no token counts, no generation
speed, no time to first token, no cache reuse. The agentic-reuse gates of
2026-09-28 measured exactly those numbers, offline, with a driver script; live
traffic had no equivalent.

Strata's answer is small: a local HTTP server, a hand-written page, one JSON
snapshot polled once a second, and a disciplined set of design tokens. The
question this record settles is how to build that here without giving back the
properties the rest of the system is built on.

## Decision

### 1. The edge publishes per-request numbers

`GET /api/v1/inference` on the control listener reports the requests holding an
admission slot (model, route, phase, elapsed time, time to first token, tokens
so far, live decode rate) and a ring of the last 64 finished requests (prompt,
cached and output tokens, prompt and decode rates, time to first token,
duration, HTTP status, finish class), plus totals since start.

It is unauthenticated, like `/api/v1/status`, because it carries nothing that
route does not already treat as public: model IDs are bound to the manifest,
routes to the allowlist, and every other field is a number or a closed
enumeration. It never carries content. The observer reads the upstream response
byte-for-byte on its way to the client and keeps only `usage`, `timings`,
finish reasons and a count of token events. It is bounded — one buffered line
may be 1 MiB and a buffered non-streaming body 8 MiB — and past either bound the
request's telemetry goes missing while its bytes still reach the client
unchanged.

The counts are the runtime's own. llama-server reports `usage` when asked and
`timings` always; the edge prefers `usage`, falls back to `timings`
(`prompt_n + cache_n`, `predicted_n`), and counts token events only when neither
arrives, marking that record `output_estimated`. The parser was checked against
captured b10225 output, whose final streamed chunk carries `timings` and no
`usage` at all.

A successful read of this route is not recorded in `recent_events`: a page
polling once a second would otherwise evict the event log it sits beside within
two minutes. A refused read still is.

`model_statuses[]` gains `process_state`, the router's word for the model's
process (`starting`, `ready`, `stopping`, `shutdown`, `stopped`), passed through
only when it is one of those words. It is what tells a model being loaded from a
long prompt being read; before it, the two were indistinguishable.

### 2. `cia-monitor.exe` serves the page on loopback

A separate process, so the monitor can be started, stopped and crash without
touching anything that serves inference.

- It listens on a literal loopback IP with an explicit port — `127.0.0.1:18095`
  for canary, `127.0.0.1:8095` for final — and refuses a hostname, a wildcard or
  a LAN address at startup.
- It reads the edge's two public control routes, through a client that refuses
  redirects. It holds no credential and links no credential code. Every route
  but `POST /api/actions` (section 4) answers `GET` and `HEAD` only.
- It answers only `Host: 127.0.0.1:<port>` and `localhost:<port>`, which is what
  defeats DNS rebinding: a page elsewhere that resolves its own name to
  loopback still sends its own name.
- Every response carries `Content-Security-Policy: default-src 'none';
  script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self';
  base-uri 'none'; form-action 'none'; frame-ancestors 'none'`, `nosniff`,
  `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer` and same-origin
  opener and resource policies. There is no CORS.
- The page is three hand-written files — HTML, CSS, a script — compiled in with
  `go:embed`. No framework, no build step, no npm tree. Everything the page
  shows is written with `textContent` and DOM nodes; tests fail if the script
  contains an HTML sink, or if the page contains inline script, inline style or
  a reference to another origin.
- The edge's status is decoded into typed structs before it is re-served, so
  fields the page does not use — the event log with its request IDs, the router
  URL, a runtime's source repository — are dropped by construction rather than
  forwarded until someone notices.

The machine side comes from the counters Windows already publishes:

- PDH through `PdhAddEnglishCounterW`, for the reason `internal/edge` gives: the
  reference workstation runs a pt-BR Windows on which the localized paths do not
  resolve. GPU utilization uses Task Manager's definition — each engine's share
  summed across processes, the adapter as busy as its busiest engine — because
  an average would report a card saturated on compute as a few percent busy.
- Dedicated and shared adapter memory, CPU `% Processor Utility`, disk bytes per
  second; `GlobalMemoryStatusEx` for RAM and commit; the llama-server process's
  private bytes, working set and dedicated VRAM.
- The adapter's name and size from the D3DKMT thunks by LUID — plain calls
  where DXGI would need COM for the same two facts.
- The adapter is the one the edge judged (`gpu_memory.adapter`) when it reported
  one, so both processes describe the same device; otherwise the edge's own rule
  and tie-break apply.

It samples once a second and keeps two minutes of history in the monitor, not
the page, so a page opened in the middle of a request arrives with its context.
Each sample is encoded once and every open page is served the same bytes.

Anything that could not be measured reaches the page as `null` and is drawn as
a dash — never as a plausible zero. AMD on Windows publishes no temperature,
power or PCIe link counters through PDH, so the monitor shows none rather than
estimating them.

### 3. The page follows Strata's design discipline

Colors, radii and spacing exist only as tokens; light is the default and dark
follows the system unless the viewer chooses. A 4-point spacing scale, radii of
8, 14 and 20, tabular numerals, heavy stat figures with quiet units, one accent
with info, warning and danger tints, 12%-opacity area sparklines, a 270° gauge,
dimmed badges for the phases a request is not in. The text is Portuguese.

The Monitor tab shows the phase (idle or ready, queued, loading, reading the
prompt, generating) with live output, speed, time to first token and a progress
bar against the output ceiling; eight machine cards; a context gauge with cache
reuse and the client's compaction threshold; and the recent requests with
totals. Admission verdicts appear on the card of the resource they are about —
a refusal for physical memory is shown on RAM, not on commit. Modelos lists the
roster with each model's profile, runtime, requirements and capabilities.
Conexão gives the client base URLs, the release identity, the runtimes and what
the monitor will and will not do.

### 4. Two model controls

The page can load a chosen model and unload the loaded one. Load is the pipe's
`switch`: with `max_loaded_models` pinned to 1, loading a model means unloading
the current one first, and `switch` is the verb that says so. Nothing else is
offered — not `drain` or `resume`, which belong to the release transaction, and
not a bare `load`, which only fails with a conflict whenever a model is loaded.

Both travel over the ADR 0015 named pipe through `internal/adminpipe`. The
monitor sends no credential because the transport uses none, falls back to
nothing, and accepts an answer only from a pipe owned by the installed
`cia-edge.exe`. Adding a route from a web page to that pipe is the decision
ADR 0018 examined, and the controls are the ones it concluded such a route
needs, adapted to a browser:

1. **Only this page can ask.** `POST /api/actions` requires the exact `Origin`
   of the monitor, `Sec-Fetch-Site: same-origin` when the browser sends it, a
   custom header, and a JSON body. A form on another site can produce none of
   these without a CORS preflight, and the monitor never answers one.
2. **Only this user can ask.** Loopback is reachable by every local account, and
   the pipe admits only the serving user; a monitor that relayed any caller would
   lend that user's authority to the others. Before a request is admitted, the
   kernel's TCP table names the process holding the other end of the
   connection, and its token must carry the monitor's own user SID.
3. **Only the operator can say yes** — ADR 0018's control 1. Every request stops
   at a native message box owned by the monitor, naming the environment, the
   operation, the model by name and ID, and the model it will replace. The page
   cannot reach, answer or suppress it; Cancel has the default focus; and it
   closes itself as not approved after 45 seconds, because an approval given
   long after the request describes a moment that has passed.
4. **Only what the edge lists, one at a time.** The model must be in the edge's
   own roster; loading the loaded model, unloading one that is not loaded, and
   asking while inference is active or queued are refused before any dialog,
   since the edge would refuse the last one anyway. One operation may be pending
   at a time.

Failures reach the page as codes chosen by the edge or the monitor, never as
error text, which can name a path. Every state change is logged as metadata:
operation ID, verb, model, state, code.

The confirmation closes the gap the credential-free pipe leaves: script running
in the page — an injected one, or a browser extension's — can ask for an
operation but cannot approve it. The same-user check keeps the monitor from
being a way around the pipe's DACL. A process already running as the serving
user gains nothing from the monitor: it can open the pipe directly, as
`THREAT_MODEL.md` has always recorded.

The path was exercised end to end on the reference workstation: a load
requested from the page, confirmed in the dialog, reached the installed canary
edge over the pipe and had `gemma4-12b-qat-ud-q4xl` serving five seconds later.

## Against ADR 0018's controls

ADR 0018 accepted a script-executing surface on the strength of five controls.
Measured against them:

1. *Native confirmation for every mutation* — kept. Section 4 implements it for
   the two operations the page can request.
2. *A sanitized WebView2 environment* — moot. Nothing is embedded; the page runs
   in the operator's own browser.
3. *A pinned runtime* — moot for the same reason. The browser is the operator's,
   as it is for every other page they open, and what the page can ask for still
   has to be approved outside it.
4. *No HTML sink, and a policy the build asserts* — kept, and enforced by Go
   tests over the embedded files and over the headers the handler emits.
5. *An SBOM for the npm tree* — moot. There is no npm tree; the page is covered by
   the same review, secret scanning and CI as the Go beside it.

What the monitor adds that the console did not is a listener: a fourth loopback
port, serving HTML and accepting one kind of request. That is the cost, stated
plainly. It is bounded by being credential-free, Host-checked, unframeable,
origin- and user-checked for its one mutation, and confirmed outside the page.

## Consequences

- Request metadata — when requests happen, their token counts and speeds, which
  model served them — becomes readable by any local process on the control port,
  including processes of other local users, exactly as `recent_events` already
  was. Prompt and response content is never exposed. `THREAT_MODEL.md` records
  this.
- The monitor's port is not in `Test-V2Installation.ps1`'s loopback assertion,
  which covers the three configured ports. `scripts/v2` is owned by the
  in-flight ADR 0017 work; until the inventory can be extended, startup
  validation of the listen address is the control.
- `Build-V2Binaries.ps1` builds, tests and stages `cia-monitor.exe` with the
  other components. The release transaction now approves its SHA-256, backs up
  and installs it, and verifies its presence. An operator-run monitor using the
  protected binary must be closed before replacement; the installer refuses to
  replace a running executable. The monitor remains an operator-launched tool.
- Per-request data appears only after the edge carrying `/api/v1/inference` is
  released. Against an older edge the page says telemetry is unavailable and
  shows everything else, and the model controls work, because the pipe predates
  this change.
- Measured on the reference workstation, the monitor costs about 0.5% of one
  core and 28 MiB of working set, with a flat handle count over time. The page
  runs in a browser, and `RUNBOOK.md` already tells the operator to close the
  browser before a measurement run; the monitor tab is part of that
  instruction, not an exception to it.
- A confirmation dialog needs an interactive desktop. The monitor is an
  operator-launched program, so it has one; run where it had none, a request
  would wait out its 45 seconds and do nothing.
- The console is frozen: not deleted, not deployed, not maintained. Its rows in
  `THREAT_MODEL.md` stay until its code is removed, because the code is still in
  the tree.
