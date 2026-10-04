# Security policy

## Supported code

Security fixes target the current default branch and the latest release: the Go edge, credential helper, MCP servers, tray, monitor, tracked manifests, and operational scripts. The v1 Python panel and MCP bridge are no longer part of this repository and are not supported.

## Reporting a vulnerability

Report it privately through GitHub's private vulnerability reporting: open the
repository's **Security** tab and choose **Report a vulnerability**, or go
straight to <https://github.com/Sitr3n01/local-ai-provider/security/advisories/new>.
Only you and the maintainer see the report until an advisory is published.

Do not open a public issue containing credentials, prompts, responses, private source code, filesystem captures, or exploit details. Include:

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
- Administrative mutations travel over a DACL-protected Windows named pipe that
  transmits no credential. The bearer-authenticated HTTP mutation API is
  retained for compatibility and is deprecated; a client falls back to it only
  when no pipe is listening.
- A production deployment runs only from model and runtime bytes inside the
  protected installation root, verified by size and SHA-256 before and after
  publication. Candidate artifacts are never modified, moved, or deleted by a
  deployment.
- A deployment drains the provider and waits for it to finish in-flight work
  before stopping anything. It never cancels an admitted or queued request to
  make a cutover faster, and a drain that does not complete aborts the
  deployment before any binary is replaced.
- A failed cutover restores the previous release from a record written before
  any mutation. A restore that cannot complete is reported as DEGRADED, never
  hidden, and the release manifest is withdrawn so consumers stay fail-closed.
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

## Release distribution

`.github/workflows/release.yml` packages Windows binaries, schemas, an SBOM,
`SHA256SUMS`, and release metadata. It never deploys: there is no runner with
access to the workstation, no update channel, and no downloader. An operator
moves the files and approves every SHA-256 by hand, which is the boundary the
deployment transaction depends on.

Model weights, private configuration, local state, and logs are refused by an
explicit check before packaging. Code signing is not performed because no
certificate exists for this project; `SHA256SUMS` and the release metadata are
the integrity evidence, and signing remains a documented future capability.

See `docs/THREAT_MODEL.md` and `docs/RUNBOOK.md` for the complete controls and operational sequence.
