# Sanitized incident record: panel zstd failure and credential exposure

Date observed: 2026-07-20 (America/Sao_Paulo)

## Impact

- The legacy Python panel attempted to decode zstd-compressed Codex request bodies as UTF-8.
- Its uncommitted exception logging path serialized complete request headers and preserved the undecodable request body.
- A bearer credential was consequently copied into local logs. Treat the affected Codex/OpenAI session credential as compromised even though the listeners were loopback-only.
- The legacy unknown-model fallback could forward a request and its incoming authorization header to a remote OpenAI endpoint.

## Sanitized evidence

- `panel-error.log`: 199,580 bytes at inspection, 37 lines containing a bearer marker, and 191 `UnicodeDecodeError` occurrences.
- `last-bad-body.bin`: 23,580 bytes; its magic bytes identify a zstd frame.
- Four legacy Unsloth stdout logs each contain three authorization/bearer marker lines.
- Git history contains only dynamic bearer-header construction references; no literal bearer value was found by the repository-history scan.
- No credential value or request body is retained in this report.

## Required containment

- Replace the panel ingress with the local-only v2 edge before normal client use.
- Revoke or refresh the affected Codex/OpenAI session outside this repository.
- Preserve only this sanitized record, then delete the five contaminated text logs and `last-bad-body.bin` after explicit operator confirmation.
- Never reactivate the legacy panel as a rollback target.

## Status

- New v2 inference, admin, and router secrets were generated independently in Windows Credential Manager.
- The legacy `8090` listener was stopped and its Startup shortcut was renamed with a `.disabled` suffix. The Unsloth automatic Startup shortcut was disabled separately; the current interactive Unsloth process was not terminated.
- After explicit operator approval, the five contaminated text logs were deleted without reading their contents. `last-bad-body.bin` remains because binary deletion was blocked by the agent execution policy and must be removed manually by the operator.
- The legacy source diff remains untouched and uncommitted for forensic preservation.
- Revocation/refresh of the exposed external Codex/OpenAI session remains an operator action.
- The reviewed firewall policy is now applied: nine outbound block rules are enabled and the two broad Python inbound rules are disabled.
- Installed v2 ACL hardening is now applied and passes audit for all 51 exact targets. External runtime and GGUF paths remain outside that ACL boundary.

## Follow-up — 2026-07-21

- `panel-error.log`, all identified contaminated Unsloth text logs, and
  `last-bad-body.bin` are absent after the operator-approved cleanup.
- The completed v2 canary has eleven outbound deny rules and its current
  36-target installation ACL audit reports zero drift.
- The external runtime and GGUF effective ACLs were checked independently and
  expose no broad local-user write rule; only `Sitr3n`, `Administrators`, and
  `SYSTEM` are present.
- No local check can prove server-side revocation of the historical external
  Codex/OpenAI session. That remains a manual account-security action and is
  not silently marked resolved by this report.
