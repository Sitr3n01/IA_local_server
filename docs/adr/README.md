# Architecture decision records

Each record states a decision, the context that forced it, and what it costs.
Records are never deleted: when a decision changes, a newer record amends or
supersedes it, and the older record says so in its status.

## Status vocabulary

| Status | Meaning |
|---|---|
| **Accepted** | The decision is in force. *Amended by* names a later record that changed part of it. |
| **Proposed** | The work runs on the canary deployment, but the models or runtime it introduces are still `candidate` under the [promotion gate](../MODEL_PROMOTION.md). Each record's status says what it leaves unpromoted. |
| **Superseded** | Replaced by the record named in its status, and kept as the history of the decision. |

## Index

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-thin-edge-and-llama-swap.md) | Thin Go edge with llama-swap lifecycle | Accepted |
| [0002](0002-fail-closed-autonomy.md) | Fail closed and leave agent autonomy to harnesses | Accepted |
| [0003](0003-manifest-and-promotion.md) | One versioned manifest and evidence-gated promotion | Accepted |
| [0004](0004-interactive-windows-startup.md) | Hidden per-user scheduled tasks | Accepted; amended by 0021 |
| [0005](0005-native-operator-panel.md) | Native, thin Windows operator panel | Accepted; amended by 0021 and 0022 |
| [0006](0006-explicit-stateless-inference-mcp.md) | Explicit stateless inference MCP | Accepted |
| [0007](0007-multimodel-discovery-and-hidden-unsloth.md) | Multimodel discovery and hidden Unsloth boundary | Accepted |
| [0008](0008-external-artifact-acl-boundary.md) | ACL boundary for external runtime and GGUF paths | Superseded by 0013 |
| [0009](0009-hybrid-model-offload-and-context-cache.md) | Partial weight offload and host-RAM context cache for hybrid models | Proposed; its second addendum measures context reuse working on upstream b10549 |
| [0010](0010-buun-fork-runtime-for-agentic-context-reuse.md) | A pinned buun-llama-cpp runtime for Qwen3.8 agentic context reuse | Proposed; the defect it works around is not reproduced on upstream b10549 |
| [0011](0011-workstation-memory-profiles-and-live-adapter-telemetry.md) | Workstation memory profiles and live adapter telemetry | Proposed |
| [0012](0012-three-qwen38-profile-classes-and-the-output-contract.md) | Three Qwen3.8 profile classes, and an output contract that belongs to the model | Proposed; its huge-context class superseded by 0016 |
| [0013](0013-production-artifact-boundary.md) | Production artifact boundary | Accepted |
| [0014](0014-release-transaction-and-drain.md) | Release transaction, drain, and rollback | Accepted |
| [0015](0015-named-pipe-administrative-transport.md) | Windows named-pipe administrative transport | Accepted |
| [0016](0016-one-moe-and-the-four-function-roster.md) | Four functions, four artifacts, and one MoE | Proposed |
| [0017](0017-claude-desktop-native-3p-gateway.md) | Claude Desktop uses a direct CIA gateway | Accepted; amended by 0022 |
| [0018](0018-webview-operator-console.md) | WebView operator console alongside the native tray | Superseded by 0019 |
| [0019](0019-browser-monitor.md) | A browser monitor replaces the console direction | Accepted |
| [0020](0020-monitor-describes-the-machine.md) | The monitor describes the machine, not only the edge | Accepted |
| [0021](0021-ia-local-tray-owns-startup.md) | IA Local: one startup entry and a flyout in the monitor's design | Accepted; amended by 0022 |
| [0022](0022-claude-local-beside-the-signed-in-instance.md) | Claude Local runs beside the signed-in instance; harnesses reach the server only on request | Accepted |

## Writing a new record

Number it after the last one, keep the `## Status` section first, and name any
record it amends or supersedes in both places. A change to a trust boundary,
public API, autonomy rule, deployment mechanism, or compatibility promise needs a
record — see [Contributing](../../CONTRIBUTING.md).
