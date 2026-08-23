# ADR 0014: Release transaction, drain, and rollback

## Status

Accepted.

## Context

`Complete-V2Canary.ps1` was a good cutover for one environment. It had three
gaps that block calling the backend production-ready.

**Final had no equivalent.** The runbook told an operator to run
`New-V2Config`, `Install-V2ScheduledTasks`, and `Install-V2Harness` for Final by
hand. That is precisely the hand-written sequence the canary script exists to
forbid — no single approval boundary, no guaranteed restart, no verification.

**Atomicity stopped at the file.** Every installer replaces its own file
atomically, but a deployment crosses configuration, six binaries, two scheduled
tasks, ACLs, and a firewall policy. A failure between those steps left new
config with old binaries, or new binaries with old tasks, and nothing recorded
what the previous state had been. The only recovery was an operator
reconstructing it from memory.

**The provider was stopped, not drained.** `Stop-ScheduledTask` kills the job
object. An inference generating its 20000th token dies mid-stream, and the
harness sees a connection reset it cannot distinguish from a crash. On the Huge
profile a single request can legitimately run for over half an hour.

## Decision

### One deployment transaction for both environments

`Complete-V2Deployment.ps1 -Environment Canary|Final` is the implementation.
`Complete-V2Canary.ps1` and `Complete-V2Final.ps1` are thin wrappers that exist
for command-line compatibility and discoverability. Final is a first-class
environment: same approvals, same drain, same release record, same rollback,
same verification. The only differences are ports, task names, and that Final
resolves artifacts from the protected production store (ADR 0013).

### Drain before stop

The edge gains a maintenance lifecycle:

```text
RUNNING --drain--> DRAINING --(active=0, queued=0)--> MAINTENANCE --resume--> RUNNING
```

A drain refuses *new* admissions and nothing else. An admitted request keeps its
slot, a queued request keeps waiting for one, and neither is cancelled. New
inference is refused with `503 maintenance_draining` and a `Retry-After`;
`/readyz` reports not ready; `/livez`, `/v1/models`, `/api/v1/status`, and
`/metrics` stay available because they are side-effect free and the deployment
transaction needs them.

Maintenance state is process-local. A restarted edge always comes back RUNNING,
which keeps the edge stateless and means a crash during maintenance needs no
operator to clear it.

Model load, unload, and switch are unchanged by draining: they already require
an idle gate, so they are refused with `409 inference_busy` while work is in
flight and admitted once drained. Draining does not grant an administrative
operation the right to interrupt an inference — nothing does.

The deployment asks for a drain, waits for `active == 0 && queued == 0` under an
explicit timeout, and only then stops the tasks. On timeout it resumes the
provider and aborts **before any binary is replaced**.

An edge that predates this API cannot be drained. That case is not silently
treated as safe: the deployment refuses unless the operator passes
`-AllowUndrainableProvider`, which is needed exactly once — for the deployment
that installs drain support.

### Release-level rollback by transaction record, not by filesystem link

The obvious Windows design is `releases\<id>` plus a `current` junction. It is
rejected here: a reparse point inside the installation root contradicts an
invariant the ACL and artifact code enforce everywhere else, and it would make
the protected tree's identity depend on a link an attacker who reaches the root
could retarget.

Instead, before mutating anything, the deployment records the bytes and
scheduled-task definitions it is about to replace under
`state\releases\<release-id>\backup`, with a journal rewritten at every stage.
On failure the same record drives the restore. Files are restored in reverse
order; a file the previous release did not have is removed rather than left
behind.

A restore never stops at the first failure — a partial restore that reports
exactly what could not be put back is more useful than one that abandons the
rest. If anything fails, the deployment reports **DEGRADED**, and the release
manifest is left absent so every consumer stays fail-closed.

`Rollback-V2Deployment.ps1` is the operator-initiated version of the same
restore, for a deployment that succeeded technically but must be undone. It
drains first for the same reason the deployment does.

Rollback covers exactly what the deployment replaced: configuration, launchers,
application binaries, and task definitions. It deliberately does not touch
candidate models, published production artifacts, benchmark results, user data,
or unrelated configuration.

### Installed release identity

`config\release.<environment>.json` names the installed release: release id,
version label, git commit, environment, previous release id, component hashes,
artifact identities, and status. `cia-edge` reads a strict, sanitized subset of
it and reports it through `/api/v1/status` and one constant `cia_edge_release_info`
metric series. Paths and hash inventories stay in the file and never reach a
response.

The manifest is written before the edge restarts, because the supervisor passes
`--release-manifest` only when the file exists at spawn.

## Consequences

- A deployment now has one command, one approval boundary, and one recorded
  outcome for both environments.
- A cutover failure has a defined recovery instead of an operator's memory, and
  a recovery failure is loud instead of silent.
- Deployments take longer: the drain wait is bounded by real generation time,
  not by the script.
- The backup set is a full copy of the replaced binaries and configuration per
  release. At a few megabytes per release under `state`, this is affordable;
  pruning old release directories is a manual operator decision, because
  automatic deletion of recovery records is exactly the wrong default.
- The release record is written by PowerShell and read by Go. The two validate
  the same field set independently, and both refuse a manifest they do not
  recognise; the coupling is real and is called out in both files.
