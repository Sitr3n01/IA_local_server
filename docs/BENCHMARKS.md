# Benchmark and acceptance protocol

Benchmark reports must contain no prompts, responses, headers, cookies, or credentials. Store only scenario IDs, timestamps, hashes, token counts, status, timing, queue state, memory measurements, and pass/fail assertions.

## Environments

Measure the same immutable model/runtime pair through:

1. Direct `llama-server` on a temporary loopback port.
2. Canary llama-swap without edge, using the router credential.
3. Full canary edge path.
4. Codex and OpenCode end-to-end sessions.

Do not duplicate real harness prompts to a shadow service. Synthetic fixtures only may be replayed.

## Metrics

- Cold load time and first-token latency.
- Warm time to first token, p50/p95/p99.
- Prompt and generated tokens per second from native counters.
- End-to-end duration and edge overhead.
- HTTP/SSE status, cancellation propagation, and retry result.
- Peak dedicated VRAM, process working set, system RAM, and committed bytes.
- Queue depth, wait time, rejection count, process starts/stops, and orphan count.

Never derive throughput by dividing an already reported tokens/second metric.

## Contract matrix

| Scenario | Required result |
|---|---|
| `GET /v1/models` idle | 200, one declared model, no process start |
| Identity/gzip/zstd requests | Same semantic response |
| Unsupported encoding | 415 |
| Wire/decoded/ratio limit | 413 without upstream request |
| Unknown route/model | Deterministic local 404 |
| Responses streaming | Native events forwarded incrementally |
| Chat streaming | Valid incremental chunks |
| Function tool | Valid name/JSON arguments and continuation |
| Client cancellation | Upstream request terminates promptly |
| Five waiters plus active | Bounded queue; overflow 429 + `Retry-After` |
| Router unavailable | Edge readiness false and request 503 |
| Idle 900 seconds | Model unloads exactly once |

Stateful Responses features that llama.cpp does not implement must return `400 unsupported_feature`; fabricated IDs or reconstructed history are failures.

## Quality suite

Use versioned, non-secret fixtures representing:

- Repository navigation and change planning.
- A focused bug fix with tests.
- Multi-file refactor without unrelated edits.
- Tool choice and valid tool arguments.
- Strict JSON/structured output.
- Long-context retrieval near the declared limit.
- Refusal to invent successful tool execution.

Score correctness, compile/test result, tool validity, instruction adherence, unsupported-feature honesty, and absence of reasoning/template artifacts. Publish fixture definitions and aggregate results, not private code or generated content.

## Performance gates

- Edge overhead p95 below 50 ms for non-load time and below 5% throughput regression versus direct serving.
- Edge and router ready within 30 seconds of interactive logon.
- First cold response completes within 60 seconds.
- No unplanned runtime exit, orphan, duplicate model load, or silent model swap.
- Admission reserves remain at least 1 GiB VRAM and 4 GiB commit beyond the measured profile peak.

## Soak

Run for 72 continuous hours with at least 500 mixed Responses/Chat requests and 20 complete load/unload cycles. Include concurrent Codex/OpenCode use, cancellation, queue overflow, router restart, edge restart, and an interactive-logon restart.

Acceptance is at least 99% success after excluding deliberate 4xx/429 policy tests, with zero unrecovered crash, external request, credential finding, or model duplication.

## Report shape

```json
{
  "schema_version": 1,
  "manifest_sha256": "...",
  "model_sha256": "...",
  "runtime_sha256": "...",
  "started_utc": "...",
  "scenario": "responses_streaming",
  "requests": 100,
  "success": 100,
  "p95_ttft_ms": 0,
  "p95_edge_overhead_ms": 0,
  "peak_vram_gib": 0,
  "peak_commit_gib": 0,
  "external_connections": 0,
  "credential_findings": 0
}
```

Reports are evidence, not configuration. Promotion values must be reviewed into the manifest deliberately.
