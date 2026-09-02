# Threat model

## Security objective

Protect local source code, prompts, model artifacts, credentials, and machine integrity while delivering an inference API only to processes on this Windows host. Confidentiality takes precedence over transparent fallback or verbose debugging.

## Assets

- Harness prompts, responses, tool schemas, file content, and repository metadata.
- Inference, administration, router, Codex/OpenAI, and other application credentials.
- Runtime executables, generated launchers, manifests, GGUF weights, logs, and scheduled tasks.
- The operator console's served bundle (`frontend/dist`) and its host binary.
  The bundle is script that runs in the operator's session and drives
  administrative mutations, so its integrity matters the way a runtime
  executable's does — not the way a static asset's does.
- GPU/RAM/commit availability and the integrity of model output.

## Trust boundaries

1. Harness to edge data plane: untrusted input authenticated with the inference token.
2. MCP/operator to edge control plane: public sanitized reads over HTTP;
   mutations over a DACL-protected named pipe that the operating system
   authenticates and that carries no credential. The bearer-authenticated HTTP
   mutation API is retained, deprecated, for compatibility.
3. Edge to llama-swap: loopback-only, router token, client authorization stripped.
4. llama-swap to llama-server: dynamically allocated loopback port and fixed command.
5. Unsloth/export pipeline to production manifest: offline validation boundary.
6. Repository to installed binaries/configuration: build and deployment boundary.
7. SOTA harness to inference MCP: user-directed text delegation to a pinned
   local endpoint/model; no harness authority is transferred.
8. Candidate artifact tree to production artifact store: a copy-and-verify
   boundary. Production runs only from bytes inside the protected installation
   root; candidates stay user-writable and are never mutated by a deployment.
9. Console page to console host: untrusted DOM to native process. The rendered
   page is treated as hostile input, not as part of the host. It reaches the
   host only through `window.chrome.webview.postMessage`, and the host accepts
   exactly six operation kinds from a closed allowlist — one read (`status`)
   and five mutations (`load`, `unload`, `switch`, `drain`, `resume`), matched
   by whole-string lookup, never by prefix or substring. There is no operation
   that writes configuration, and no field in the message that carries a value
   to write. Every mutation stops at a native confirmation the page cannot
   reach. This boundary is inside boundary 2, not beside it: what crosses it
   ends up on the same DACL-protected pipe.

Loopback is a routing constraint, not sufficient authentication. Other local processes and users are potential attackers.

## Threats and controls

| Threat | Required control | Verification |
|---|---|---|
| Remote access to inference/control | Bind every service to `127.0.0.1`; assert the three configured ports (router, data, control) are loopback-bound. Note the scope: this checks the ports already in the configuration, so it confirms known ports are local — it is not an inventory and would not discover a fourth listener | `Test-V2Installation.ps1` |
| A second operator surface opens a port | `cia-console.exe` opens no socket at all. It serves its bundle from disk through a WebView2 virtual-host mapping (`cia-console.invalid`, an RFC 2606 name chosen over `.localhost` because Chromium special-cases the latter with built-in loopback resolution), with host-resource access set to `DENY_CORS`. There is no HTTP server in the package; the page's only channel to the host is `postMessage` | The E2E enumerates every listening socket by owning PID and asserts the console process owns none, and separately asserts nothing listens on the debug port the hostile environment asked for |
| Prompt/code exfiltration through fallback | No remote upstream; route/model allowlists; outbound firewall during cutover | Unknown-model contract test and firewall audit |
| Repository config redirects an explicitly local harness session | Codex CLI-precedence endpoint/provider pins; OpenCode process-scoped inline override and explicit model; reject override arguments | Launcher/config static tests and manual canary session |
| Client bearer forwarded upstream | Edge removes it and injects only router auth | Fake-upstream integration test |
| Credential disclosure in logs | Metadata-only structured logs and redaction; no headers/bodies | Secret scan after tests and soak |
| Decompression bomb | 16 MiB wire, 64 MiB decoded, 100:1 expansion limits | Unit/fuzz tests |
| Resource exhaustion | One active request, four waiting, 120-second wait, resource admission reserve | Concurrency/load test |
| Unauthorized administrative operation | Separate admin token and control listener; DACL-protected pipe for mutations; admin MCP unregistered | Negative authorization tests |
| Admin token capture by a loopback impostor during an explicit mutation | Mutations move to a named pipe whose DACL admits only SYSTEM, Administrators, and the serving user; no credential is transmitted; the first instance is claimed with `FILE_FLAG_FIRST_PIPE_INSTANCE`; clients verify the server process image and refuse to fall back to HTTP on anything but an absent pipe | Pipe round-trip, impostor-executable, and squat tests |
| Production inference running from a user-writable model or runtime | Final resolves artifacts only from `local-ai-v2\artifacts`, verified by size and SHA-256 before and after publication; the tree is immutable to the serving user | Artifact store self-test and `Test-V2Installation.ps1` |
| Update kills an in-flight generation | Deployment drains the provider and waits for `active=0, queued=0` before stopping anything; a drain timeout aborts before any binary is replaced | Drain tests and the deployment transaction |
| Failed cutover leaves a hybrid installation | Release transaction records replaced bytes and task definitions before mutation and restores them on failure; an incomplete restore reports DEGRADED and withdraws the release manifest | Release transaction self-test |
| SOTA model invokes local inference without user intent | Single-purpose server/tool instructions require an explicit user request; one named tool; optional client approval policy | MCP schema/config test and prompt smoke test |
| Delegated prompt escapes to a remote endpoint | Literal-loopback URL validation, disabled proxy/redirects, egress firewall, pinned model | Negative URL/redirect tests and firewall audit |
| Local model gains file/tool authority | Text-only prompt/context schema; no roots, resources, sampling, filesystem, shell, or nested tools | Exact MCP capability/tool inventory |
| Credential leaks into MCP config or process arguments | Inference credential read directly from Windows Credential Manager only after request validation | Config/command-line inspection and secret scan |
| Periodic admin-token capture by a loopback impostor | Status is public and sanitized; panel reads admin only on an explicit mutation, and prefers the credential-free pipe | Status-client test asserts no `Authorization` and panel review |
| Release metadata discloses paths or secrets | The edge reads a strict subset of `release.json` — environment, release id, version, commit, previous release, status — and never its paths or hash inventory | Status sanitization and release-metadata tests |
| Script execution in the operator plane | A DOM-rendering surface now exists: `cia-console.exe` renders a React page in a WebView2 control. Four controls bound it. (a) Every mutation requires a native Win32 `MB_OKCANCEL` dialog owned by the host, naming the operation and its target; the page cannot forge, suppress, or answer it. (b) No HTML sink is reachable: `dangerouslySetInnerHTML`, `innerHTML`/`outerHTML` assignment and `insertAdjacentHTML` are lint errors. (c) `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'none'`, asserted against the built bundle. (d) The console holds no credential — `TokenProvider` is a stub returning an error, so the deprecated HTTP mutation path fails closed | `TestBridge*` approval-refusal tests; `npm run lint`; `npm run verify:prod-bundle`; ADR 0018 |
| Egress from a compromised console page | `connect-src 'none'` closes fetch/XHR/WebSocket but not window opening. Two host-side guards fail closed on anything that is not `https://cia-console.invalid`: `NavigationStarting` cancels the navigation, `NewWindowRequested` marks it handled and opens nothing | `TestNavigationPolicyPermits` (attacker-shaped hosts and schemes); the E2E asserts both guards refuse a live attempt |
| Substituted console frontend deceives the operator | `CIA_CONSOLE_FRONTEND_DIR` is read unvalidated, so a serving-user process can point the console at attacker-chosen HTML. This grants no new authority — that account can already open the pipe — but it can render a false operator view and solicit approval for an operation the operator did not initiate. The native dialog names the operation and its target for exactly this reason, and defaults focus to Cancel | Approval-dialog text names `kind` and `model_id`; no automated check covers the override — see residual risks |
| Debugger attached to the console DOM | A serving-user process setting `WEBVIEW2_*` in the ambient environment would otherwise turn the next launch into a remotely debuggable Chromium. `webviewloader`'s `init()` neutralizes all five variables before any WebView2 environment is created, and the host asserts this at startup and refuses to run if it did not happen | `env_windows_test.go`; the E2E launches with `--remote-debugging-port` set and asserts nothing listens on it |
| Model/runtime tampering | SHA-256, byte size, provenance revision, restricted ACL | `-VerifyHashes` and second-user ACL test |
| Supply-chain substitution | Fixed releases/checksums, dependency scanning, SBOM and notices | CI/release checklist |
| Malicious model metadata/template | Manifest review, explicit Jinja qualification, no unreviewed auto-download | Promotion checklist |
| PID/task replacement | Fixed absolute paths and protected task definitions/launchers | Scheduled-task and ACL inspection |
| Sensitive retained process logs | llama-swap capture buffer disabled; no llama-server log file | Runtime inspection |
| Direct dynamic-port inference bypass | Protected router API-key file and llama-server authentication | Unauthenticated direct inference must return `401` |

## Known incident inherited from v1

The v1 Python panel logged complete request headers and undecodable compressed bodies. Bearer material in those logs is considered compromised. The response sequence is mandatory:

1. Prevent new requests from entering v1.
2. Record only sanitized timestamps, counts, hashes, and stack signatures.
3. Rotate the affected external session/credential and initialize new v2 credentials.
4. Search for credential fingerprints without printing matching values.
5. Obtain explicit operator confirmation before deleting contaminated logs or body captures.
6. Preserve the uncommitted diagnostic change as a private patch if evidence retention is required.
7. Never reactivate the vulnerable panel as rollback.

## ACL target

`Set-V2Acl.ps1` is preview-only by default and operates only on exact,
non-reparse targets beneath `C:\IA\local-ai-v2`. It makes the built-in
Administrators group the owner, grants `Sitr3n` read/execute access to the
installation, and grants that user `Modify` only beneath `state` and `logs`;
`Administrators` and `SYSTEM` retain `FullControl` for elevated recovery. Apply
requires an elevated shell plus `-Apply`, creates a pre-change SDDL recovery
record, and verifies every target afterward. `-Audit` is read-only and fails on
owner, inheritance, or DACL drift.

Production model and runtime artifacts live under `artifacts`, which the policy
classes as immutable: the serving user reads and executes, Administrators and
SYSTEM retain full control for recovery. Candidate artifacts outside the
installation root are deliberately not mutated by this script and no longer need
to be: a final deployment does not read them (ADR 0013). They remain
user-writable on purpose, because qualification campaigns rewrite them.

`Set-V2Firewall.ps1` has the same preview/elevated-apply operator boundary for
egress rules.

## Residual risks

- A process already running as `Sitr3n` can access loopback and the user's Credential Manager.
- A SOTA harness may include more context than necessary in an explicitly
  delegated call. The adapter bounds bytes and stores nothing, but the harness
  and user remain responsible for minimizing the text sent to the local model.
- Administrative mutations now travel over a DACL-protected named pipe that
  carries no credential (ADR 0015), so winning the control port no longer yields
  a replayable token. Two residual pieces remain. First, the deprecated HTTP
  mutation API is still enabled for compatibility and still authenticates with
  the bearer token; a client falls back to it only when no pipe is listening,
  and `cia_edge_admin_http_mutations_total` is the signal for removing it.
  Second, a process already running as the serving user can open the pipe — that
  account can already read Windows Credential Manager, so this is unchanged and
  not fixable at this layer.
- The operator console runs on the machine's Evergreen WebView2 runtime, which
  auto-updates outside the release transaction and is not hashed or inventoried.
  ADR 0018 control 3 requires the pinned Fixed Version distribution instead;
  that control is decided but **not implemented**, and this is the gap it
  leaves. It is the one component executing operator-plane code that the
  "production runs only from bytes we hashed" rule does not currently cover.
- `CIA_CONSOLE_FRONTEND_DIR` selects the folder the console serves and is not
  validated. A process running as the serving user can substitute the operator
  console's content. It gains no authority by doing so — that account can open
  the administrative pipe directly — but it can misrepresent provider state and
  solicit approval for an operation the operator did not intend. The native
  confirmation dialog names the operation and its target, and defaults to
  Cancel, so the deception has to survive an operator reading it.
- The console's frontend and host source are not tracked in git (see
  `docs/reports/`), so nothing about them is covered by the branch protections,
  review, or secret-scanning that apply to the rest of this repository.
- Model output can be incorrect or adversarial even when artifact integrity is valid.
- The AMD baseline is reproducible only by recorded binary hash, not by its misleading directory label.
- Windows interactive-logon tasks provide availability only while the user
  session exists: nothing serves before first logon and everything stops at
  logoff. A Windows Service migration is an explicit non-goal for a workstation
  deployment. `state\supervisor-<component>.json` records restart count,
  consecutive unstable exits, and current backoff so a crash loop is diagnosable
  without adding a listener.
- Router/UI behavior in future llama-swap versions may differ; upgrades require full requalification.
- llama-server requires a plaintext API-key file while running. It is derived from Credential Manager into the ACL-restricted `state` directory, never logged or passed on a command line, but remains readable to the serving user.
- Firewall egress enforcement and immutable administrative ownership require elevation; a canary is not eligible for cutover until both are verified.
- A published production artifact is a second copy of the same weights. Disk cost
  is the trade for not depending on a user-writable file, and retiring a model
  does not reclaim it: deleting weights is always an explicit operator action.
- The release record is written by PowerShell and read by Go. Both validate the
  same field set independently and both refuse a manifest they do not recognise,
  but the field names are a real coupling between the two implementations.
- A deployment that runs with `-AllowUndrainableProvider` can interrupt an
  in-flight generation. The switch exists for exactly one deployment — the one
  that installs drain support — and the run is recorded in the release journal.
