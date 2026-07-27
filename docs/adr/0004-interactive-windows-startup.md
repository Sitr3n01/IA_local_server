# ADR 0004: Hidden per-user scheduled tasks

- Status: accepted
- Date: 2026-07-20

## Decision

Run Router and Edge as separate limited scheduled tasks at the user's interactive logon. Each task directly invokes `cia-supervisor.exe`, which launches the component without a console, injects process-local credentials, and places the serving tree in a kill-on-close Windows Job Object. Do not use Session 0 services in the first release.

## Rationale

The ROCm stack and Credential Manager access are already validated in the user's interactive environment. A direct PowerShell or VBS child escaped `Stop-ScheduledTask` during canary testing, and Scheduler restart-on-failure did not recover it reliably. The small supervisor makes ownership explicit: task stop kills every descendant, unexpected exits restart after a one-minute bounded backoff, and no secret is written to a task definition or command line.

## Consequences

- Serving is unavailable before logon and normally stops at logoff.
- Task definitions, the supervisor, and generated launchers are security-sensitive and require restricted ACLs.
- Installation is preview-only by default and never starts tasks automatically.
- Generated VBS files remain hidden manual launchers, not scheduled-task actions.
