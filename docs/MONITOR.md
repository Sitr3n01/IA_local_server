# Browser monitor

`cia-monitor` serves a read-only page on loopback that shows, every second, what
the local AI server is doing and what the machine under it is doing. It holds no
credential and listens on `127.0.0.1` only. The page's text is in Portuguese.

![The monitor tab on the live canary](images/monitor-overview.png)

Decision records: [ADR 0019](adr/0019-browser-monitor.md) (the monitor replaces
the console direction) and [ADR 0020](adr/0020-monitor-describes-the-machine.md)
(it describes the machine, not only the edge).

## What it shows

- **The request in flight.** Its phase — queued, loading the model, reading the
  prompt, generating — with tokens per second, time to first token, cache reuse
  and context fill.
- **The machine.** GPU load, VRAM, shared memory, CPU, RAM, commit and disk,
  plus the power the GPU draws: watts, temperature, clocks and the energy
  accumulated over the session.
- **Admission.** The RAM and commit tiles carry an *admite* badge when the
  selected model would be admitted right now, using the same reserves the edge
  enforces.
- **The models.** The Models tab lists every model in the deployed manifest with
  its context, output limits, KV cache, weights, runtime, memory requirements and
  qualified capabilities, and says when a model cannot be admitted and why.

![The Models tab: four models, capability flags and live admission](images/monitor-models.png)

## Other model servers on the machine

The monitor does not depend on the edge to see the machine. It shows which
processes use the GPU and which other tools are serving a model — LM Studio /
Bionic, Ollama, standalone llama.cpp and OpenAI-compatible servers — with model,
quantization and context window when the tool's API reports them, and it flags
activity even while the edge is down.

Per-request speed for traffic through the edge comes from the edge's telemetry.
For other tools' llama.cpp servers (LM Studio / Bionic, or a `llama-server`
started with `--log-file`) the monitor reads the counters the server itself
writes to its log — prompt, cache, output, tokens per second, time to first
token — without reading any text, and shows everything in the same table, with
the serving source named under each model. Tools without a per-request log
(Ollama, for example) appear as *externa* (external: duration, peak GPU and
power, board energy), and the source's card explains why.

## The snapshot API

`/api/snapshot` includes `requests[]`, a bounded list of recent records with
per-request metrics from both origins, and `coverage`, which distinguishes
measured external sources from those that show activity only. Each record states
where each number came from in `measurements`; prompt and cache computed from the
log and estimated speeds are labeled, and missing values stay `null`. That
coverage refers to the detected sources: a tool that neither goes through the
edge nor publishes per-response metrics or a log cannot be counted exactly from
the GPU alone.

Per-request numbers come from the edge's `/api/v1/inference`; against an edge
that predates the route, the page keeps working and says telemetry is
unavailable.

## Controls

The page can load a chosen model and unload the loaded one — and nothing else.
Each request goes over the edge's administrative pipe, with no credential, and
runs only after you confirm it in a Windows dialog the page cannot reach (Cancel
is the default; with no answer in 45 s, nothing happens). The monitor accepts
these requests only from its own page, from a process of the same user that runs
the server. `-admin-pipe off` removes the buttons.

For a model another tool loaded, each source has an *Encerrar processo do
modelo* (end the model's process) button, which asks for the same native
confirmation.

Starting and stopping the server belong to `cia-tray` (IA Local); drain and
resume belong to `cia-mcp-admin` and the release transaction.

## Running it

```powershell
go build -trimpath -o bin/cia-monitor.exe ./cmd/cia-monitor
.\bin\cia-monitor.exe -open                      # canary: http://127.0.0.1:18095
.\bin\cia-monitor.exe -environment final -open   # final:  http://127.0.0.1:8095
```

Running `-open` while a monitor is already up just opens the existing page.
`-listen` serves the page on another loopback address, and `-control-url` points
it at a different edge control plane; both refuse anything that is not a literal
loopback address.

The page's lint and DOM tests live in [`frontend/`](../frontend/) and run in CI.
