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
llama-server (AMD ROCm) -> qualified GGUF

Unsloth -> export -> offline validation -> manifest promotion

cia-tray -> authenticated control API + explicit harness launchers

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
- Applies narrow, route-specific compatibility adapters required by the verified clients/runtime: Responses accepts its flat function shape and flattens/restores Codex namespace tools only on the internal hop; Chat Completions validates and preserves the standard wrapped `tools[].function` shape used by OpenCode. Initial contiguous authority messages are coalesced for the Ornith template. Interleaved authority messages, hybrid tool shapes, unsupported tool types, and non-text authority content fail closed.
- Removes the client `Authorization` header and authenticates to the router with `CIA_ROUTER_TOKEN`.
- Uses distinct `CIA_INFERENCE_TOKEN` and `CIA_ADMIN_TOKEN` credentials.
- Fails closed for unknown routes, models, encodings, and unsupported stateful behavior.
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

- `cia-tray.exe` is a native Win32 notification-area process with no listener,
  browser runtime, chat surface, or prompt history.
- Its periodic snapshot is public, metadata-only, and never reads or sends a
  credential. It reads the administrative credential directly from Windows
  Credential Manager only after an explicit load, unload, or switch action.
- It combines immutable model/capability metadata from the installed manifest
  with live lifecycle, queue, and capacity state from `cia-edge`.
- The only durable panel state is an atomically replaced selected-model file
  under the writable installation `state` directory. Selection affects new
  launches only; it is never represented as the GPU-active model.
- Load, unload, and switch run asynchronously so a cold start cannot block
  Explorer. An admitted switch finishes independently of the panel connection;
  the Exit command is disabled while an administrative operation is active.
  The edge remains responsible for inference exclusion and resource admission.
- Harness processes receive a model ID only after exact catalog and capability
  validation. Their process environments are allowlisted and contain no cloud
  credentials inherited from the tray.
- Closing the icon does not stop serving components. At most one tray instance
  per deployment is permitted by a named per-user mutex. The icon registers
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

Tracked source contains manifests, schemas, templates, code, tests, and documentation only. The installation root `C:\IA\local-ai-v2` contains generated configuration, signed or hashed binaries, launchers, writable state, and logs. GGUF files remain under `C:\IA\models` and are never copied into Git.

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

## Process ownership and startup

Two per-user scheduled tasks start at interactive logon: Router and Edge. Their direct action is `cia-supervisor.exe`, running without a console under limited user privileges. The supervisor constructs a minimal environment allowlist, obtains only the credentials needed by its child, and assigns the complete serving tree to a kill-on-close Windows Job Object. The router writes a derived API-key file under protected v2 state because llama-server cannot read Windows Credential Manager directly; no credential is placed in configuration or a command line. Stopping a task therefore cannot leave an edge, router, or model process behind.

Unexpected child exits use an in-process exponential backoff: one minute initially, doubling up to fifteen minutes, and resetting after a stable ten-minute run. Task settings retain `IgnoreNew`, restart-on-failure, and no execution timeout as a second recovery layer. Generated VBS files remain optional hidden manual launchers; they are not the supervision boundary.

The task installer is preview-only unless `-Apply` is supplied. It refuses to replace an existing task unless `-Replace` is also supplied and never starts a task automatically.

## Failure behavior

- Router unavailable: edge readiness fails and inference returns `503`; it never contacts another provider.
- Queue full or wait expired: edge returns `429` with `Retry-After`; it never swaps or downgrades the request.
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
