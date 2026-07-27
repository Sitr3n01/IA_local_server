# v2 canary completion report — 2026-07-21

This report is intentionally content-free. It records no prompt, response,
header, cookie, credential, secret value, or secret fingerprint.

## Installed boundary

- Data plane: `127.0.0.1:18090`
- Control plane: `127.0.0.1:18091`
- Internal router: `127.0.0.1:19292`
- Public model: `local-coding`
- Runtime: pinned AMD baseline `D427939A79AABAAA26B98361CBF3BB3DDE658ACBB9ACD1F59C5A95C60567B085`
- Model: Ornith 1.0 9B Q4_K_M `5720D1F671B4996481274FFFE01868C3C36E87C135CC8538471CC7BD6087B106`

| Installed executable | SHA-256 |
|---|---|
| `cia-edge.exe` | `6DE396622D49D26B426568BFBDB45B24ACF6B8E513F987CE7771272F3399BAEE` |
| `cia-mcp.exe` | `E4D8670DFA65F52E98DB2602E2881F6905ECC20149E1CF066148A344A6C1EE86` |
| `cia-mcp-admin.exe` | `666C4973442F9C38BCC6029CC1A9A01BBA437CFB5BCACB3665D7310AE2A84419` |
| `cia-mcp-inference.exe` | `EA8D38BA558616C08212CA8F1C553D1FD63C3D6E4A926A9B7A70CF2BC7DF9E67` |
| `cia-tray.exe` | `1F1855BA57310BE0D5903DCA1D987D6390328F15FD78E14C5B48FB9084559402` |
| `llama-swap.exe` | `60362E63EBAF97CF0DEC791986479022AB87FE7D1350A64D55B256821A437BAA` |

Router and Edge scheduled tasks were running after the transactional cutover.
The current-user panel Startup shortcut was installed separately and an
immediate second preview reported `unchanged`.

## Functional evidence

| Area | Sanitized result |
|---|---|
| Installation | Manifest/runtime/model hashes, generated-file bindings, listeners, public status, discovery side effects and installed files passed `Test-V2Installation -VerifyHashes -Online`. |
| MCP inference | The installed stdio executable exposed only `local_ai_delegate`; the SDK live probe returned the exact synthetic marker through `local-coding`. |
| Codex SOTA delegation | A new ephemeral, read-only Codex session explicitly invoked `local_ai_delegate`, observed the local marker and returned it to the primary SOTA session. |
| OpenCode | The global config parsed, retained its normal default behavior and a fresh desktop launch spawned the installed inference MCP as a child process. |
| Claude | Claude Desktop and Claude Code retained their existing settings and loaded the managed MCP entry; no Claude prompt was transmitted for this validation. |
| Panel | The native diagnostic reported provider/router ready, `local-coding` selected and active, queue `0/4`, capacity available and `local-fast` disabled. |
| Lifecycle | A live administrative `unload -> load` cycle left zero then exactly one `llama-server`; load completed in about 3.3 seconds. |
| Fail closed | A live unknown-model request returned `404` without changing the runtime-process count. |
| Unsloth | The installed launcher started Studio on literal loopback `127.0.0.1:8888`; HTTP returned `200` and no private Studio state was edited. |

## Security and quality evidence

- ACL audit passed for all 36 installed v2 targets. Only `state` and `logs` are
  writable by the serving user; immutable targets are read/execute.
- Runtime and GGUF ACLs inherit only `Sitr3n`, `Administrators`, and `SYSTEM`;
  no broad local-user write rule was present.
- Eleven enabled outbound block rules cover all v2 executables and both runtime
  candidates, including Edge, router, and inference MCP.
- Four pre-change harness configurations were backed up with DPAPI
  `CurrentUser`; all four decrypted for the current user and no plaintext
  backup was present.
- Current local credential values had zero matches across 287 relevant text
  files. Synthetic MCP/Codex markers had zero matches in all seven live log
  files, which were readable with sharing and below 10 MiB.
- `go test ./...`, `go vet ./...`, `gofmt`, Staticcheck 2026.1 and
  `govulncheck` v1.1.4 passed.
- Gitleaks v8.30.1 found no leak in Git history or the current directory scan.
- The regenerated CycloneDX 1.6 SBOM contains nine components and has SHA-256
  `BFD7C8E0895CBC54089DC9BA2874C4619A164EF16299A657CA464D79A0E73869`.

## Promotion decision

The implementation is functionally complete as a local-only canary, but
`local-coding` remains `candidate`; this report does not relabel it as a
production-qualified model. Final promotion still requires the documented
72-hour/500-request/20-cycle soak, sustained 99% success, resource-headroom
qualification and the seven-day post-cutover observation. Those elapsed-time
gates cannot be inferred from a successful completion run.
