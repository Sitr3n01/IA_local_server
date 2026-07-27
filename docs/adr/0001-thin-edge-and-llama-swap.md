# ADR 0001: Thin Go edge with llama-swap lifecycle

- Status: accepted
- Date: 2026-07-20

## Decision

Use llama-swap v240 for lazy model lifecycle and a small Go edge for authentication, request decoding/limits, allowlisting, queueing, cancellation, observability, and control. Forward native Responses and Chat Completions without translating between them.

## Rationale

The current llama.cpp runtime already implements native Responses and tool calls. The v1 translation layer introduced protocol loss, simulated streaming, zstd failure, leaked credentials, and coupled lifecycle/UI/MCP concerns. An upstream lifecycle component plus a narrow policy edge minimizes custom code while retaining machine-specific safety controls.

## Consequences

- llama-swap version/config become qualified dependencies.
- Edge contains no agent or chat state.
- Runtime protocol limitations are reported honestly rather than emulated.
- There is no v2 web UI.
