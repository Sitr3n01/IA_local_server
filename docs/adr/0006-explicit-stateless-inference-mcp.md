# ADR 0006: Explicit stateless inference MCP

## Status

Accepted.

## Context

The local provider is useful both as a complete OpenAI-compatible backend and
as a secondary model consulted from a stronger hosted harness. The original v2
boundary correctly removed the legacy MCP chat/session/compaction behavior,
because it duplicated the harness agent loop and mixed inference with
administration. It did not, however, provide a narrow way for a user to ask a
SOTA Codex, Claude, or OpenCode session to obtain one answer from the local
model without switching the session's primary provider.

## Decision

Add `cia-mcp-inference.exe` as a fourth, independent MCP surface. It exposes
exactly one tool, `local_ai_delegate`, over stdio. The caller may provide a
bounded prompt, bounded reference context, and bounded output limit. The MCP
process pins the literal-loopback Edge URL and model through operator-generated
process configuration, obtains the inference token directly from Windows
Credential Manager, performs one non-streaming request, returns structured
text/usage metadata, and retains no state.

Both the server instructions and tool description say that delegation is
allowed only when the user explicitly asks to use, consult, or delegate to the
local model. The tool does not expose model selection, files, roots, resources,
sampling, shell execution, nested tools, conversation history, or lifecycle
administration. `cia-mcp.exe` remains read-only observability and
`cia-mcp-admin.exe` remains unregistered by default.

Client installation merges only the named MCP entry. It must not change the
normal provider, model, login, or cloud defaults of Codex, Claude, or OpenCode.

## Consequences

- A hosted SOTA harness remains responsible for planning, permissions, context
  selection, and deciding whether the user's request calls for delegation.
- Local inference is reusable without pretending the local model is a nested
  autonomous agent.
- Prompt text explicitly delegated by the harness reaches only the local Edge;
  it is bounded and never logged or persisted by the provider.
- Model loading and GPU use may occur as a transient consequence of a call, but
  no persistent or administrative state change is available through this MCP.
- Supporting additional models requires an operator-generated pinned
  integration, not a caller-supplied arbitrary model ID.
