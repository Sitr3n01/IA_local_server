# ADR 0003: One versioned manifest and evidence-gated promotion

- Status: accepted
- Date: 2026-07-20

## Decision

Treat `config/models.yaml` as the sole model/runtime source of truth. Generate router/deployment configuration from it. Require immutable provenance, local hashes, capability tests, resource measurements, benchmarks, and soak evidence before final deployment.

The file uses JSON serialization that is valid YAML 1.2, allowing deterministic parsing with stock PowerShell and Go.

## Rationale

The v1 system duplicated profiles and paths across scripts, UI state, SQLite, catalogs, and client configs. Names such as `b8407` did not reliably identify the executable build.

## Consequences

- Hand-edited generated configuration is unsupported.
- Any model/runtime/template/flag change invalidates qualification.
- Unsloth exports remain offline candidates until promoted.
