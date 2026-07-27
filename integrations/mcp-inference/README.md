# MCP inference bridge

`cia-mcp-inference.exe` is the optional, stateless bridge for delegating a
bounded subtask to the local model while the harness keeps its existing primary
provider and model.

The global MCP registration is named `cia-local-inference` and exposes only
`local_ai_delegate`. A prompt can request it explicitly, for example:

> Use `local_ai_delegate` from `cia-local-inference` to ask `local-coding` for a
> second implementation proposal, then review the result yourself.

This does not replace the harness model, route ordinary prompts locally, or add
automatic cloud-to-local fallback. The SOTA model remains responsible for the
agent loop, permissions, context and deciding whether a requested tool call is
appropriate.

## Registration

The Windows installer detects and merges the registration into:

- Codex global `~/.codex/config.toml`;
- Claude Desktop `claude_desktop_config.json` when Desktop is installed;
- Claude Code user state when `~/.claude.json` or the CLI is present;
- OpenCode global `~/.config/opencode/opencode.json`.

It changes only the exact managed MCP entry. Codex receives an explicit tool
allowlist and `prompt` approval; OpenCode receives the matching `ask`
permission. Claude clients use their normal tool-confirmation UI. The files
contain only the loopback data URL and model ID. The executable reads the
inference credential from Windows Credential Manager itself.

Preview first and approve the exact plan hash:

```powershell
$preview = & C:\IA\local-llama\scripts\v2\Install-V2McpInferenceIntegrations.ps1 |
    ConvertFrom-Json
$preview

& C:\IA\local-llama\scripts\v2\Install-V2McpInferenceIntegrations.ps1 `
    -Apply -ExpectedPlanSha256 $preview.plan_sha256
```

Changed configurations receive persistent DPAPI `CurrentUser` backups plus
atomic rollback protection. Close or restart the harness after installation so
new sessions discover the MCP server.

Formats follow the current official documentation for
[Codex MCP](https://developers.openai.com/codex/mcp),
[Claude Code MCP](https://docs.anthropic.com/en/docs/claude-code/mcp), and
[OpenCode MCP](https://opencode.ai/docs/mcp-servers/).

## Known limitations of the pinned executor model

`local_ai_delegate` always calls `local-coding` (Ornith 1.0 9B). It is a small
model used strictly as a directed executor under an orchestrating SOTA
session, not as an autonomous problem-solver — the guidance below assumes the
orchestrator scopes each call, the way the tool description asks.

**Reliable for**: one well-specified, self-contained subtask per call — a
single function, a focused explanation, reviewing or extending a short
snippet, answering one design question. Simple bounded requests (e.g. "write
`fibonacci(n)`") have been 100% correct across repeated tests.

**Not reliable for**: a single call that must satisfy several interacting
requirements at once (e.g. thread-safety + TTL expiry + a decorator + a test
suite, all together). Five separate canary trials of exactly that shape each
produced plausible-looking, well-structured code with a different real,
would-break-at-runtime bug:

1. An import that shadowed the `time` module, breaking every `time.sleep` call.
2. A cache decorator that deadlocked on first use (re-acquired a non-reentrant
   lock it already held).
3. A TTL check that compared the wrong operands, so items never expired.
4. An eviction method that only raised "cache full" instead of evicting.
5. A `get()` that silently *wrote* a phantom entry on a cache miss instead of
   reporting one.

None of these were caught by the model itself — `local_ai_delegate` has no
file, tool, or execution access by design (see the top of this document), so
it cannot run or test what it writes. Raising the output-token ceiling,
KV-cache precision, weight quantization (Q4_K_M → Q8_0), and sampling
temperature each changed *which* bug showed up, not whether one did.

**Practical guidance for the orchestrator**: decompose delegated work into
narrow, single-concern calls instead of one broad one; always verify or
actually execute the returned code before trusting it (this is exactly what
an agentic loop with real tool execution — Codex, OpenCode, or the orchestrator
itself — is for, and it is a materially safer path than this delegate for
anything with real correctness stakes); expect that an elaborate multi-part
prompt, or one that explicitly asks the model to reason at length before
answering, can consume the entire output-token budget before it reaches a
final answer.

**Rating, as a directed executor under SOTA orchestration: 7/10.** Not a 9-10
— it still needs its output checked, and it should never be handed a
multi-constraint task in one call. Not a 4-5 either — that number reflected
judging it as an autonomous solver of open-ended problems, which is not the
role this tool exists for. Scoped correctly, it is a genuinely useful, fast,
local first-pass executor.

Current tuning (2026-07-22 canary session): `CIA_MCP_INFERENCE_MAX_OUTPUT_TOKENS`
up to 65536 (compiled ceiling in `internal/mcpinference/config.go`), delegate
`temperature` 0.2, edge upstream timeout 30 minutes, `local-coding` KV cache
`q8_0`/`q8_0`, weights `Q8_0` (9.53 GB, ~10.8 GiB VRAM measured with full
131072 context resident).
