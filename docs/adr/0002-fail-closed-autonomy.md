# ADR 0002: Fail closed and leave agent autonomy to harnesses

- Status: accepted
- Date: 2026-07-20

## Decision

Unknown routes/models and unavailable capacity fail locally. The provider never forwards to cloud, selects a weaker model, downloads/updates an artifact, executes tools, compacts context, or retries a task semantically. Codex and OpenCode own those choices through explicit profiles and their permission systems.

## Rationale

Transparent fallback can exfiltrate private code, spend money, change output quality, and hide capacity failures. Duplicating harness logic in MCP creates conflicting state and approval boundaries.

## Consequences

- Harness users see explicit `404`, `429`, or `503` failures.
- Cloud remains available only through the harness's separately selected official provider.
- MCP is read-only by default and stateless.
