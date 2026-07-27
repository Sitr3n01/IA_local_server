# Security policy

## Supported code

Security fixes target the v2 Go edge, credential helper, MCP server, tracked manifests, and operational scripts on the current default branch. The v1 Python panel and MCP bridge are retained only for migration evidence and are not considered safe production surfaces.

## Reporting a vulnerability

Do not open a public issue containing credentials, prompts, responses, private source code, filesystem captures, or exploit details. Contact the repository owner privately with:

- A sanitized description and affected version/commit.
- Reproduction using synthetic data.
- Impact and required local privileges.
- Request IDs, timestamps, hashes, and redacted stack signatures.

Never attach raw v1 panel/Unsloth logs or `last-bad-body.bin`; they may contain compromised bearer material.

## Security invariants

- All listeners bind to loopback.
- Client inference, control administration, and router credentials are distinct.
- Client authorization is stripped before forwarding.
- Unknown models/routes fail locally and no cloud fallback exists.
- Logs contain metadata only and rotate at 10 MiB with seven backups and a 14-day maximum retention.
- No prompt, response, header, cookie, credential, GGUF, runtime binary, or generated secret-bearing config belongs in Git.
- Dependencies, release assets, runtimes, and models are fixed by immutable version/revision and SHA-256.
- Administrative MCP is not registered by default.
- Inference MCP is a separate, stateless, text-only executable with a pinned
  literal-loopback endpoint/model and no filesystem, tool, or administrative
  access. It may be invoked only for an explicit user-requested delegation.

## Credential handling

`cia-credential.exe init` creates missing `inference`, `admin`, and `router` values in Windows Credential Manager. `get` is used only by launchers/clients that need the value. Rotation uses `set NAME` over standard input followed by coordinated component restarts. Values must never be passed on a command line or printed.

Any credential found in a log, process command line, tracked file, issue, or chat is considered compromised and must be rotated.

## Incident response

1. Stop new ingress to the affected component.
2. Preserve sanitized metadata and hashes; do not duplicate secret-bearing evidence.
3. Rotate affected credentials before restarting service.
4. Search by fingerprint without displaying matching content.
5. Correct the root cause and add a regression test.
6. Delete contaminated artifacts only after explicit operator confirmation.
7. Re-run contract, secret, listener, and egress tests before service restoration.

See `docs/THREAT_MODEL.md` and `docs/RUNBOOK.md` for the complete controls and operational sequence.
