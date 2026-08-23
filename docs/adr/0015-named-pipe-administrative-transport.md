# ADR 0015: Windows named-pipe administrative transport

## Status

Accepted. Phase A: the pipe is the preferred transport and HTTP administrative
mutation is deprecated but retained.

## Context

`THREAT_MODEL.md` has carried this residual risk since v2 shipped:

> The HTTP administrative plane authenticates the caller but not the server
> process. […] a malicious process that wins the control port exactly when the
> operator explicitly mutates state remains a residual risk until a
> DACL-protected Windows named-pipe admin transport is implemented.

The shape of the exposure is specific. Loopback is a routing constraint, not
authentication: any process running as the serving user can bind `127.0.0.1:8091`
if it gets there first. The panel and the administrative MCP already avoid the
unattended version of this — periodic status reads are public and carry no
credential — but a load, unload, or switch still puts `CIA_ADMIN_TOKEN` on a
loopback socket, and a squatter that owns the port at that moment captures a
token it can replay for as long as it stays valid.

## Decision

Add a second administrative transport that the operating system authenticates,
and stop sending a credential over the one it cannot.

`\\.\pipe\cia-local-ai-admin-<environment>` is created by `cia-edge` with a
protected DACL:

```text
D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;0x12019f;;;<serving user SID>)
```

SYSTEM and the built-in Administrators group keep full access for recovery; the
account the edge runs as gets read and write on the pipe and nothing else. `P`
prevents an inherited entry from widening it. `PIPE_REJECT_REMOTE_CLIENTS`
refuses a remote client even if the machine is later joined to a domain that
permits remote pipe access. The environment is part of the name so canary and
final can never answer each other's commands.

**No credential travels over it.** A process that cannot open the pipe cannot
issue an operation, and a process that captures the channel captures nothing
worth replaying. That is the whole point: the exposure being closed is credential
capture, not command authorization.

The first instance is created with `FILE_FLAG_FIRST_PIPE_INSTANCE`, so a process
that already squatted the name makes edge startup fail loudly instead of
silently sharing the endpoint. Clients check the reverse direction: they resolve
the pipe server's process image and refuse anything other than the installed
`cia-edge.exe`, and they connect at `SECURITY_IDENTIFICATION` so a compromised
endpoint cannot impersonate the operator elsewhere.

The wire format is one bounded, newline-terminated JSON request and one response
per connection — 8 KiB maximum, unknown fields rejected, one message per
connection so a client cannot pipeline commands behind a single authorization
decision. Five operations: `load`, `unload`, `switch`, `maintenance.drain`,
`maintenance.resume`. Reads stay on HTTP.

Both transports enter the same transport-neutral functions, so admission policy,
capacity refusal, and the router calls cannot drift apart between them.

### Migration

Phase A keeps HTTP administrative mutation working. `mcpadmin.Client` — used by
both `cia-mcp-admin.exe` and the tray — prefers the pipe and falls back to HTTP
**only** when nothing is serving the pipe at all. Every other failure (a wrong
server executable, a refused open, a malformed reply) is a refusal, because
something answered, and sending a bearer token afterwards would undo exactly the
hardening this transport provides.

HTTP mutations are marked `X-CIA-Admin-Transport: http-deprecated` and counted in
`cia_edge_admin_http_mutations_total`. Phase B removes the surface once that
counter stays at zero across a full deployment cycle.

## Consequences

- The administrative token is no longer transmitted for a mutation on a normally
  deployed installation. It still exists and still authenticates the deprecated
  HTTP path, so it must still be protected and rotated.
- Startup gains a new failure mode: a squatted pipe name stops the edge. That is
  correct — it is the condition the transport exists to detect — and it is
  visible in the process log and the supervisor restart record.
- A process already running as the serving user can still open the pipe. That is
  unchanged and unfixable at this layer: the same account can already read
  Windows Credential Manager. What changes is that watching the channel no
  longer yields a replayable credential.
- The transport is Windows-only by construction. On any other platform it is
  absent rather than emulated, because an emulation without the DACL would be an
  unauthenticated mutation channel.
- The deployment and rollback scripts drive drain and resume over the pipe, so
  the normal cutover path handles no credential at all. They fall back to the
  HTTP path only when no pipe is listening, which is the older-edge case.
