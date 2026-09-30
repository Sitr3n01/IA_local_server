# CIA Local AI Provider v2 — Architecture

## Purpose

The v2 system is a local-only OpenAI-compatible inference provider for agent harnesses. It owns model serving and resource admission; it does not implement an agent loop, conversation history, harness tool execution, cloud fallback, training, or model downloads. An optional MCP adapter may submit one stateless, text-only inference when the user explicitly asks a SOTA harness to consult the local model; orchestration remains in that harness.

The v1 panel, Python MCP bridge, ports, launchers, logs, and state remain present only for migration evidence and rollback to direct `llama-server`. They are not dependencies of v2.

## Runtime topology

```text
Codex / Claude / OpenCode
        |
        | OpenAI Responses or Chat Completions
        v
cia-edge (data plane) ---- cia-edge (control plane)
        |                         ^
        | stripped client auth    | read-only MCP / explicit admin
        | router auth injected     |
        v                         |
llama-swap v240 ------------------+
        |
        | lazy lifecycle, TTL=900, one loaded model
        v
    +---+--------------------------+
    |                              |
    v                              v
llama-server                  llama-server
llama.cpp upstream            buun-llama-cpp fork
(baseline, fallback)          (agentic canary, Qwen3.8 only)
    |                              |
    +---+--------------------------+
        v
   qualified GGUF

Unsloth -> export -> offline validation -> manifest promotion

cia-tray (IA Local) -> router/edge tasks + control API + monitor + explicit harness launchers

SOTA harness -> cia-mcp-inference (stdio) -> cia-edge data plane
```

| Surface | Canary | Final | Exposure |
|---|---:|---:|---|
| Edge data | `127.0.0.1:18090` | `127.0.0.1:8090` | Harnesses |
| Edge control | `127.0.0.1:18091` | `127.0.0.1:8091` | MCP/operator |
| llama-swap | `127.0.0.1:19292` | `127.0.0.1:9292` | Edge only |
| Dynamic llama-server | starts at `19300` | starts at `9300` | Router only |

No v2 listener may bind to `0.0.0.0`, `::`, a LAN address, or a public interface.

## Component contracts

### `cia-edge`

- Starts with `--data-addr`, `--control-addr`, `--upstream`, `--models-config`, and `--models-schema`.
- Allows only `GET /v1/models`, `POST /v1/responses`, and `POST /v1/chat/completions` on the data plane.
- Decodes `identity`, `gzip`, and `zstd` within fixed compressed, decoded, and expansion-ratio limits.
- Streams upstream bytes as they arrive and propagates client cancellation.
- Applies narrow, route-specific compatibility adapters required by the verified clients/runtime: Responses accepts its flat function shape and flattens/restores Codex namespace tools only on the internal hop; Chat Completions validates and preserves the standard wrapped `tools[].function` shape used by OpenCode; Anthropic Messages accepts Claude Desktop's exact beta transport query, maps mid-conversation system messages, and omits Cowork's always-present tool definitions only for a text-only model with no tool history. Initial contiguous authority messages are coalesced, because llama.cpp maps `system` and `developer` to `system` and several chat templates accept only one initial system message. Interleaved authority messages on OpenAI routes, hybrid tool shapes, unsupported tool types, and non-text authority content fail closed.
- Removes the client `Authorization` header and authenticates to the router with `CIA_ROUTER_TOKEN`.
- Uses distinct `CIA_INFERENCE_TOKEN` and `CIA_ADMIN_TOKEN` credentials.
- Fails closed for unknown routes, models, encodings, and unsupported stateful behavior.
- Implements a maintenance lifecycle so the provider can be updated without
  killing work: `RUNNING -> DRAINING -> MAINTENANCE -> RUNNING`. A drain refuses
  new admissions only; an admitted request keeps its slot and a queued request
  keeps waiting for one. New inference is refused with `503 maintenance_draining`
  and a `Retry-After`, and `/readyz` reports not ready. Maintenance state is
  process-local, so a restarted edge always comes back `RUNNING`.
- Serves administrative mutations on a DACL-protected Windows named pipe,
  `\\.\pipe\cia-local-ai-admin-<environment>`, in addition to the deprecated
  HTTP control plane (ADR 0015). No credential travels over the pipe. Both
  transports enter the same transport-neutral operation, so their admission
  policy cannot drift.
- Reports the installed release identity through `/api/v1/status` and one
  constant `cia_edge_release_info` metric series when the deployment transaction
  installed a `release.<environment>.json`. It reads a strict, sanitized subset:
  environment, release id, version, commit, previous release, and status. Paths
  and component hash inventories stay in the file.
- Logs only request ID, method, sanitized route, status, and latency. Queue and failure counters are exposed separately as metrics; prompts, responses, headers, tokens, and body bytes are never logged.

### `llama-swap`

- Version is fixed to `v240`; the Windows archive checksum is recorded in `config/models.yaml`.
- Listens only on loopback and requires the router token from `CIA_ROUTER_TOKEN`.
- Publishes every deployed model, starts the selected model lazily, applies a
  900-second idle TTL, and permits one loaded model and one active inference.
- Has no aliases, preload hook, cloud proxy, request filter, or model fallback.
- Does not retain an upstream log buffer (`captureBuffer: 0`).
- Uses warning-level router logs; model request logging is disabled.

### `llama-server`

- Is launched directly by llama-swap from the immutable runtime path in the manifest.
- Receives an explicit model, host, dynamic port, Jinja template support, 128k context, q4 KV cache, one slot, metrics, and no web UI.
- Requires the router credential through `--api-key-file`; direct inference on the dynamic port without that credential returns `401`. Health and model-discovery endpoints may remain public on loopback and are not inference paths.
- Uses the current AMD baseline only by its measured SHA-256. The directory name is not trusted as version identity.
- May be one of two builds. Upstream llama.cpp is the baseline and the fallback;
  `spiritbuun/buun-llama-cpp` is an experimental second runtime carrying the
  hybrid/recurrent context-checkpoint correction that Qwen3.8 agentic use needs
  (ADR 0010). Both are `llama-server`, so `engine` stays `llama.cpp`, llama-swap
  remains the only lifecycle authority, and the fork holds no privilege the
  baseline does not.
- A fork build declares `variant: fork` and a typed `provenance` block —
  repository, full 40-hex commit, upstream ancestry where published,
  checkpoint-fix evidence, and build configuration. Identity is repository +
  commit + artifact SHA-256 + backend + GPU target; a branch name cannot be
  expressed and a directory name is never identity.
- `cia-fork-gate` must pass on the pinned commit before it is built: it compiles
  the fork's own checkpoint predicate and asserts that recurrent selection
  ignores `pos_min` entirely and turns on the recurrent frontier instead. A patch
  being present is not evidence.
- Retiring the fork is a manifest edit. Nothing in the code knows it by name, so
  when upstream matches it on the gates in `BENCHMARKS.md` the entry is retired
  and the model's `runtime` field points back at the baseline.

### MCP

- `cia-mcp.exe` is a separate stdio process using the official MCP Go SDK.
- Default registration exposes read-only health, models, active model, capacity, and sanitized events.
- `cia-mcp-inference.exe` is a separately registered stdio process with one
  tool, `local_ai_delegate`. It accepts a bounded prompt, optional bounded
  reference context, and an output ceiling; endpoint and model are fixed by
  process configuration rather than caller input.
- The inference MCP is stateless and text-only. It reads the inference
  credential directly from Windows Credential Manager after input validation,
  rejects redirects and non-literal-loopback endpoints, and never exposes the
  credential to the harness or delegated model.
- Server instructions and the tool description require an explicit user
  request to consult the local model. It never runs because a local model merely
  exists, and it does not give the local model access to harness files or tools.
- Administrative load/unload/switch tools live in the separate, unregistered-by-default `cia-mcp-admin.exe`.
- No MCP component stores messages, summarizes context, chooses a model, starts
  a cloud request, or executes harness tools.

### Operator panel

- `cia-tray.exe` ("IA Local") is a native Win32 notification-area process with
  no listener, browser runtime, chat surface, or prompt history. Its one window
  is a flyout drawn with GDI+/GDI from the browser monitor's design tokens
  (ADR 0021); it is not a web view.
- It is the deployment's only startup entry: a current-user `Run` value that
  Task Manager lists as "IA Local". When it opens it starts the Router and Edge
  tasks that are not already running, through the Task Scheduler COM API;
  "Encerrar", after an in-flyout confirmation, ends the monitor it started, the
  Edge and the Router, then the tray.
- Its periodic snapshot reads the public status and readiness routes. The only
  credential it reads on its own is the Claude gateway key, at most once a
  minute, to check the loopback gateway Claude Desktop's local mode would use.
  It reads the administrative credential directly from Windows Credential
  Manager only after an explicit load, unload, or switch action.
- It combines immutable model/capability metadata from the installed manifest
  with live lifecycle, queue, and capacity state from `cia-edge`.
- The only durable panel state is an atomically replaced selected-model file
  under the writable installation `state` directory. Selection affects new
  launches only; it is never represented as the GPU-active model.
- Load, unload, and switch run asynchronously so a cold start cannot block
  Explorer. An admitted switch finishes independently of the panel connection;
  "Encerrar" is disabled while an administrative operation is active.
  The edge remains responsible for inference exclusion and resource admission.
- Harness processes receive a model ID only after exact catalog and capability
  validation. Their process environments are allowlisted and contain no cloud
  credentials inherited from the tray.
- "Abrir painel" starts `cia-monitor.exe` on demand in a kill-on-close job with
  silent breakaway, so the monitor ends with the tray and the browser it opens
  does not. If the tray itself crashes, the serving tasks keep running under
  their supervisors; reopening IA Local finds them running.
- At most one tray instance per deployment is permitted by a named per-user
  mutex; a second start opens the running tray's flyout. The icon registers
  `TaskbarCreated` and re-adds itself after an Explorer restart.

### Harness isolation

- Codex keeps its normal OpenAI login and base configuration. Local access is a
  separate profile selected explicitly as `cia-local-canary` or `cia-local`.
- The Codex launchers pin model, provider, loopback base URL, Responses wire API,
  and disabled request compression at CLI precedence. This prevents a trusted
  repository's `.codex/config.toml` from accidentally redirecting an explicitly
  local session.
- OpenCode receives the provider configuration through process-scoped
  `OPENCODE_CONFIG` and `OPENCODE_CONFIG_CONTENT`; no global or project config is
  written. Its launcher also pins `provider/model` on the command line.
- Canary and final use distinct provider/profile IDs and ports. Installing final
  harness files is blocked until the generated final deployment marker exists.
- Both harness templates register `cia-mcp.exe` only. The administrative MCP
  executable is intentionally absent from every default integration.
- The separate global SOTA integration registers only
  `cia-mcp-inference.exe`. It appends/merges a named MCP entry without changing
  the configured provider or model. The administrative MCP remains absent.

## Source of truth and state

`config/models.yaml` is JSON-serialized YAML 1.2. This deliberate subset is deterministic and can be parsed by stock PowerShell without installing a YAML module. `config/models.schema.json` defines its wire shape; `cia-manifest.exe` applies the schema and `Test-V2Manifest.ps1` then enforces cross-reference and deployment invariants.

Tracked source contains manifests, schemas, templates, code, tests, and documentation only. The installation root `C:\IA\local-ai-v2` contains generated configuration, signed or hashed binaries, launchers, writable state, logs, and the production artifact store. GGUF files are never copied into Git.

## Candidate and production artifacts

An artifact has two possible roles, and one location cannot serve both (ADR 0013).

A **candidate** artifact lives where it was produced or downloaded —
`C:\IA\models`, `C:\IA\runtimes`, a runtime build directory. It is writable,
discoverable by the panel, expected to churn as qualification proceeds, and
never modified, moved, or deleted by any deployment operation.

A **production** artifact is a verified copy of those exact bytes inside the
protected installation:

```text
C:\IA\local-ai-v2\artifacts\models\<model-id>\<file>
C:\IA\local-ai-v2\artifacts\runtimes\<runtime-id>\<runtime directory>
C:\IA\local-ai-v2\artifacts\published.json
```

`Publish-V2Artifact.ps1` copies through staging and verifies size and SHA-256 in
the source, in the staging copy, and at the destination. Publication is atomic,
refuses reparse points anywhere on the path, refuses any target that escapes the
artifact root, and aborts if the destination changed between preflight and
publish. A runtime is published as its whole directory, because the pinned
`llama-server.exe` cannot load without the backend libraries beside it.

The manifest is never rewritten. Identity remains SHA-256 plus byte size, and the
production path is *derived* from the artifact identifier, so publishing
invalidates no pinned hash, source snapshot, or client catalog. `artifacts` is
immutable under the ACL policy: the serving user reads and executes,
Administrators and SYSTEM retain recovery.

Canary generation resolves candidates in place. Final generation resolves the
production copy and proves it holds the manifest's exact bytes; it fails if the
artifact has not been published. `New-V2Config.ps1` records which of the two it
used in the deployment marker as `artifact_source`.

Generation installs verified copies as `C:\IA\local-ai-v2\config\models.yaml` and `models.schema.json`. Scheduled tasks may consume only those installed copies; the development worktree is never a production task input. Generated files are disposable and must not be hand-edited.

## Model state machine

```text
candidate -> qualified -> enabled -> retired
     ^            |
     +------------+  regression requires requalification
```

- `candidate`: can run only on canary ports.
- `qualified`: has complete provenance, resource envelope, contract tests, benchmark results, and soak evidence.
- `enabled`: may be present in the final deployment.
- `retired`: remains documented but cannot be generated into a deployment.

An environment may deploy several models. The generator emits one `llama-swap`
block per deployed model, including its own `gpu_layers`, while the router keeps
at most one model loaded. The edge publishes all deployed IDs and reports
availability, active state, capacity, and a sanitized reason per model. The
singular deployment marker remains readable during migration, but all newly
generated markers use `models[]`.

The panel also scans `C:\IA\models` and explicitly registered roots recursively.
Resolved paths and reparse points are constrained to their registered roots,
duplicate files are collapsed, and removing a root never removes a GGUF. Files
outside the manifest remain visible as detected candidates until isolated hash,
metadata, load, and generation validation succeeds.

The validation state stores sanitized results and a SHA-256 cache keyed by the
resolved path, size, and modification time. A detected file is hash/header
inspected without execution; load/generation is allowed only after its reviewed
runtime and execution profile enter the manifest. Registered models then run a
real generation and restore the previously active lazy/model state.

## Release lifecycle

A deployment is a transaction, not a sequence of installers (ADR 0014).
`Complete-V2Deployment.ps1 -Environment Canary|Final` is the only supported
cutover; `Complete-V2Canary.ps1` and `Complete-V2Final.ps1` are thin wrappers.
Final is a first-class environment with the same approvals, drain, release
record, rollback, and verification as canary.

```text
build -> stage -> publish production artifacts -> preview deployment
      -> drain -> cutover -> verify -> running
                      |
                      +-- failure --> restore previous release --> verify
                                          |
                                          +-- restore failed --> DEGRADED
```

Before mutating anything, the transaction records the bytes and scheduled-task
definitions it is about to replace under `state\releases\<release-id>\backup`,
with a journal rewritten at every stage. It then generates configuration and
installs the harness — neither interrupts the running provider — and only then
asks the provider to drain. It waits for `active == 0 && queued == 0` under an
explicit timeout; on expiry it resumes the provider and aborts **before any
binary is replaced**.

On failure after the cutover begins, the recorded backup drives a restore to the
previous release. A restore that cannot complete reports **DEGRADED** rather than
hiding it, and the release manifest is left absent so every consumer stays
fail-closed. `Rollback-V2Deployment.ps1` is the operator-initiated version of the
same restore.

`config\release.<environment>.json` names the installed release: release id,
version label, git commit, environment, previous release id, component hashes,
artifact identities, and status. Rollback covers exactly what the deployment
replaced — configuration, launchers, binaries, task definitions — and never
touches candidate models, published production artifacts, benchmark results, or
user data.

## Process ownership and startup

Two per-user scheduled tasks run Router and Edge. They have no trigger of their own: the IA Local tray, the deployment's single current-user startup entry, starts them when it opens (ADR 0021). Their direct action is `cia-supervisor.exe`, running without a console under limited user privileges. The supervisor constructs a minimal environment allowlist, obtains only the credentials needed by its child, and assigns the complete serving tree to a kill-on-close Windows Job Object. The router writes a derived API-key file under protected v2 state because llama-server cannot read Windows Credential Manager directly; no credential is placed in configuration or a command line. Stopping a task therefore cannot leave an edge, router, or model process behind.

Tasks started in the interactive session mean availability only while the user session exists: nothing serves before first logon, and everything stops at logoff. That is accepted for a workstation deployment and is the reason a Windows Service migration remains an explicit non-goal (ADR 0004).

Unexpected child exits use an in-process exponential backoff: one minute initially, doubling up to fifteen minutes, and resetting after a stable ten-minute run. Each transition is written to `state\supervisor-<component>.json` — restart count, consecutive unstable exits, current backoff, last exit text, and last run duration — so a restart loop is diagnosable without adding a listener to the supervisor. The record is metadata only. Task settings retain `IgnoreNew`, restart-on-failure, and no execution timeout as a second recovery layer. Generated VBS files remain optional hidden manual launchers; they are not the supervision boundary.

The task installer is preview-only unless `-Apply` is supplied. It refuses to replace an existing task unless `-Replace` is also supplied and never starts a task automatically.

## Failure behavior

- Router unavailable: edge readiness fails and inference returns `503`; it never contacts another provider.
- Queue full or wait expired: edge returns `429` with `Retry-After`; it never swaps or downgrades the request.
- Provider draining for maintenance: edge returns `503 maintenance_draining` with `Retry-After` for *new* inference only. Requests already admitted or queued finish normally.
- Model checksum/resource gate fails: the model is not started.
- Client disconnects: request context is cancelled upstream.
- Runtime crashes: the supervisor restarts the failed process tree with bounded backoff; an in-flight request fails visibly.
- Edge crashes: harness receives a connection failure and applies its own retry policy.
- Credential lookup fails: the component does not start.

## Architectural invariants

1. All network surfaces are loopback-only.
2. No automatic local-to-cloud or strong-to-weak fallback exists.
3. `GET /v1/models` is side-effect-free.
4. Client credentials never reach llama-swap or llama-server.
5. Prompts, responses, cookies, and authorization values never enter logs.
6. Several models may be deployed, but only one model and one inference are active.
7. The harness owns agent behavior; the provider owns inference and capacity.
8. Unsloth is an offline producer/evaluator, never the production supervisor.
9. Legacy panel and Unsloth Startup shortcuts remain disabled; Unsloth is launched manually only when training or export is intended.
10. Local delegation through MCP is explicit, stateless, text-only, and cannot
    select a model or perform an administrative action.
11. A runtime is identified by repository, commit, artifact hash, backend and GPU
    target — never by a directory name or a moving reference. A source-built
    runtime declares provenance or it cannot be expressed.
12. A production deployment runs only from artifacts inside the protected
    installation root. Candidate artifacts are never modified, moved, or deleted
    by a deployment operation.
13. A deployment never stops the provider before it has drained, and never
    cancels an admitted or queued request to make a cutover faster.
14. Adding a runtime never alters an existing one. The upstream baseline, its
    hashes, its paths, the models bound to it, and `provider.public_model` are
    untouched by the adoption of any experimental build.

## Concurrency is not a single setting

The system serves one inference at a time, and that is enforced in four
independent places: `provider.max_loaded_models` and `parallel` are pinned to
`1` by the manifest schema, llama-swap applies `concurrencyLimit: 1`, and the
edge gate admits one active request with a bounded queue.

Raising `CIA_EDGE_MAX_ACTIVE` alone does **not** make the system concurrent. It
widens the edge's admission window in front of a serialized runtime, which
converts queue waiting into upstream contention and makes the queue metrics
misleading without adding throughput.

Real concurrency would require a coordinated change across: llama-server slots
and `--parallel`; llama-swap's concurrency limit; the edge gate; per-slot KV
cache sizing; aggregate RAM/VRAM admission rather than the single-model budget
in `capacity.go`; continuous batching behaviour; and per-request resource
accounting. Until those move together, the single-slot invariant is what the
capacity arithmetic assumes — in particular the static device VRAM budget, which
is only sound because exactly one model is ever resident.

## Single GPU

Device selection is `--device ROCm0` with `--split-mode none`, and the VRAM
budget in the manifest is a single `runtimes[].device.vram_mib`. Multi-GPU is
out of scope and no speculative abstraction exists for it; extending later means
making the budget and the device selector plural, which is a contained change.
