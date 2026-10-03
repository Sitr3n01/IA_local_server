# ADR 0017: Claude Desktop uses a direct CIA gateway

## Status

Accepted. Amended by ADR 0022: the local instance now runs beside the
signed-in one, and CIA no longer restarts Desktop to switch between them.

## Context

The Windows Claude Desktop MSIX has native third-party inference with isolated
`Claude-3p` state. Ollama would add another model lifecycle authority and a
second protocol translation layer.

## Decision

`cia-edge` implements Anthropic Messages directly and forwards to the
configured llama-swap upstream without HTTP loopback through OpenAI routes.
llama-swap remains the one-model lifecycle authority.

Desktop receives an exclusive loopback credential. Its Gateway profile omits
`inferenceModels`, so it discovers eligible model IDs from `GET /v1/models`.
The signed-in 1P profile is untouched; switching only changes the dedicated
3P registry and selector.

Claude Desktop 1.37937.1.0 calls discovery with `limit=1000`. Its discovery
pass accepts a valid `anthropic_family_tier`, but its resolved-model pass still
rejects IDs containing known non-Anthropic family names. The CIA
Claude-credential projection therefore accepts only that bounded pagination
parameter and generates one deterministic opaque `claude-local-<sha256>` wire
alias per catalog entry. It returns the real ID in `cia_real_model_id`, keeps
the real display name, and maps the alias back to the exact real model before
admission and routing. This is generated for an arbitrary catalog rather than
a three-tier allowlist; an unknown or colliding alias fails closed. The
OpenAI-credential projection is unchanged.

## Consequences

- No Ollama, LAN listener, cloud fallback, sidecar, Node dependency or database.
- Missing capability fails clearly; the adapter never substitutes `public_model`.
- Profile write is not a generation proof; a Desktop smoke is required after deployment.
- Managed Claude inference policy wins; CIA refuses to mutate local 3P state.
- Desktop restart is scoped by the exact MSIX executable path, and success
  requires the package renderer to report the requested deployment mode.
- The MSIX owns a separate `Claude-3p` Electron user-data directory in native
  3P mode. CIA neither creates a second installation nor controls that choice.
