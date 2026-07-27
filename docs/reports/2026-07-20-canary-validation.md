# Canary validation report — 2026-07-20

This report is intentionally content-free: it contains no prompt, response, header, cookie, credential, or secret fingerprint.

## Qualified artifacts under test

- Public ID: `local-coding`
- Model state: `candidate`
- Runtime identity: executable reports build `2500 (312cf033)`; directory label is not trusted
- Runtime SHA-256: `D427939A79AABAAA26B98361CBF3BB3DDE658ACBB9ACD1F59C5A95C60567B085`
- Deployed edge SHA-256: `47A2AC601B085B2FFC713175323369FA5204638DE79ACE2EAF3376272890A39F`
- Router: llama-swap `v240`, checksum verified from the manifest
- Context configuration: 131,072 tokens, one slot, q4 KV cache

## Passed evidence

| Area | Sanitized result |
|---|---|
| Responses non-stream | Completed successfully through edge/router/runtime |
| Responses streaming | Native incremental SSE; first event observed at approximately 127 ms in the measured warm test |
| Chat Completions | Non-stream, stream, and function-call contracts passed |
| OpenCode tool schema | Standard Chat Completions `tools[].function.name` is validated independently from the flat Responses schema |
| Authority compatibility | `instructions + developer` (Responses) and `system + developer` (Chat) passed after deterministic prefix coalescing |
| Namespaced tools | Flatten/restore adapter and SSE unit contracts passed |
| Compression | Real zstd request passed; identity/gzip unit contracts passed |
| Admission | One active plus four waiting succeeded; the next waiter returned `429` with bounded policy |
| Cancellation | Client cancellation propagated; metadata recorded `499` |
| Discovery | `/v1/models` remained side-effect-free and did not load a model |
| MCP | Official Go SDK listed exactly five read-only tools without loading a model |
| Supervision | Scheduled-task stop killed complete child trees; forced edge crash recovered after approximately 60.7 seconds |
| Direct-port protection | Unauthenticated direct inference on the dynamic llama-server port returned `401` |
| Static quality | Go tests, vet, staticcheck, govulncheck, PowerShell parsing, schema validation, harness validation, and diff checks passed |

## Performance and capacity observations

- Forty warm request pairs measured direct p95 at 58.08 ms and edge p95 at 57.11 ms; the -0.97 ms delta is measurement noise and satisfies the sub-50 ms overhead gate.
- A measured cold response completed in 9.28 seconds; an earlier canary observation completed in 4.75 seconds. Both satisfy the 60-second gate.
- Unloaded committed memory was approximately 31.82 GiB and loaded committed memory approximately 39.88 GiB, a delta near 8.06 GiB.
- Commit limit was approximately 42.3 GiB; observed loaded headroom fell as low as 0.54–2.42 GiB.
- Model-process working set was approximately 5.327 GiB and private bytes approximately 5.791 GiB.
- Dedicated VRAM was approximately 6.657 GiB with approximately 0.347 GiB shared in the sampled run.

The configured profile peak plus the required 4 GiB commit reserve does not fit the pre-load headroom. Resource fields remain deliberately unpromoted rather than encoding a value that would falsely qualify the model.

## Harness results

- Codex profile isolation, command-backed authentication, disabled compression, disabled web search, and local-only catalog loaded successfully.
- A real Codex session edited a small Go fixture and made `go test ./...` pass through the local provider.
- The same Codex session failed to terminate and repeated tool turns until the five-minute harness timeout. This is a model-quality/termination failure and remains a promotion blocker.
- OpenCode configuration and launcher validation passed statically.
- A real OpenCode Desktop session streamed through the local provider, requested its own one-time filesystem permission, completed one read-only tool call, and returned the expected final marker.
- The first desktop attempt exposed a route-mixing defect in the edge tool validator (`Responses` shape applied to `Chat Completions`). The route-specific correction was covered by regression tests and then passed the same live harness test.
- After firewall and ACL application, a cold Chat request still returned HTTP 200 and started exactly one runtime process, but failed an exact-response assertion. Transport remained healthy while model instruction adherence remained unqualified.

## Security state

- Legacy panel `8090` listener stopped; its Startup shortcut is disabled.
- Unsloth automatic Startup shortcut is disabled; the interactive process was not terminated.
- Installed model inference requires router authentication on the dynamic port.
- Installed manifest/schema are validated copies under the v2 installation root; scheduled tasks no longer consume the development worktree.
- The regenerated installed CycloneDX SBOM has SHA-256 `2FC0ACF2DD16E5EEAAD2C18FF0A3FAEF2673BB0C69E232DBB2C063FA280B1FA7` and includes the direct JSON Schema validator dependency.
- Firewall hardening is applied and verified: nine outbound block rules are enabled, with zero unsafe rules in the v2 group, and both broad Python inbound rules are disabled.
- Installed ACL hardening is applied and independently audits clean for all 51 exact targets. External runtime and GGUF paths remain outside its boundary.
- Five contaminated text logs were deleted after explicit approval. `last-bad-body.bin` remains pending manual operator deletion because agent-side binary deletion was blocked. The exposed external session credential still requires manual revocation/refresh.

## Promotion decision

`local-coding` remains `candidate`. Qualification is blocked by:

1. committed-memory reserve failure at 128k;
2. non-terminating real Codex session;
3. protection or relocation of external runtime/GGUF artifacts beyond the now-hardened installation root;
4. source and installed binaries still deriving from an uncommitted dirty revision rather than a reviewed clean tag;
5. pending external credential revocation and explicit incident-artifact disposition;
6. missing 72-hour, 500-request, 20-load-cycle soak.
