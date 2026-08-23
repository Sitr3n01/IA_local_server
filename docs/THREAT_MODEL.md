# Threat model

## Security objective

Protect local source code, prompts, model artifacts, credentials, and machine integrity while delivering an inference API only to processes on this Windows host. Confidentiality takes precedence over transparent fallback or verbose debugging.

## Assets

- Harness prompts, responses, tool schemas, file content, and repository metadata.
- Inference, administration, router, Codex/OpenAI, and other application credentials.
- Runtime executables, generated launchers, manifests, GGUF weights, logs, and scheduled tasks.
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

Loopback is a routing constraint, not sufficient authentication. Other local processes and users are potential attackers.

## Threats and controls

| Threat | Required control | Verification |
|---|---|---|
| Remote access to inference/control | Bind every service to `127.0.0.1`; inspect listeners | `Test-V2Installation.ps1` |
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
| Stored/browser XSS | No web UI in v2 | Listener/route inventory |
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
