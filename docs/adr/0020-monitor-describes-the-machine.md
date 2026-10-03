# ADR 0020: The monitor describes the machine, not only the edge

## Status

Accepted, 2026-09-29. Extends ADR 0019; nothing in it is reversed.

## Context

The operator ran a model in LM Studio's new Bionic app while the monitor was
open. The page showed nothing about the model and nothing useful about what the
machine was doing. The GPU, VRAM, CPU and memory cards were machine-wide and did
read the hardware, but every card that says what is being served - the phase,
the speed, the context, the request list - came from the cia-edge, which never
saw that traffic, and the only process the monitor could name was
`llama-server.exe`. Bionic runs its models through LM Studio's own daemon, so
the page read "Ocioso" while the GPU was at ninety percent.

The operator's goal is a monitor that reports what is happening on the PC
whatever tool is using the model, and that also reports what the GPU draws.

## Decision

### 1. Power comes from the GPU driver

Windows' performance counters carry no power. On AMD hardware the driver ships
`atiadlxx.dll`, whose power-management log returns power, temperatures and
clocks per adapter. The monitor loads it from System32, matches the adapter to
the one the counters describe by name, and reads the log once a second.

Which log index is the board's power was found by measurement rather than
assumed: on the reference workstation (Radeon RX 9070 XT) index 73 read 55 to
70 W at the desktop and 270 to 285 W while a model generated, tracking the
card's clock and activity, while index 23, the ASIC power of earlier
generations, was unsupported. D3DKMT's adapter performance query answered for
the integrated GPU and returned zero for the discrete one, so it is not used.
Values outside plausible ranges are dropped, and the page says which interface
produced a figure. Energy is the integral of those readings since the monitor
started; a pause longer than five seconds is not integrated.

NVIDIA and Intel are not implemented. The interface is `sensorReader`, and a
machine without the AMD library reports no power, which the page shows as
unavailable and never as zero.

### 2. Who is using the GPU is answered for every process

The per-process GPU counters (dedicated memory and engine utilization) are
joined with the process table, so the page lists the programs holding the
adapter whatever they are. Known inference tools are labelled. Only the image
name is sent to the page; paths are used for recognising a tool and stay in the
process.

### 3. Other servers are found by asking, read-only

A discovery pass runs every two seconds. It looks at the sockets that listen on
loopback, keeps those owned by a process of this user that is a known inference
tool, holds at least 512 MiB of GPU memory, or is the endpoint LM Studio's
daemon recorded for itself in `~/.lmstudio/.internal/http-server.json`, and
asks each, with GET only, what it serves:

| Tool | Route | Reveals |
|---|---|---|
| LM Studio / Bionic | `/api/v0/models` | loaded models: architecture, quantization, loaded and maximum context |
| Ollama | `/api/ps` | models in memory: family, size, quantization, VRAM, context |
| llama.cpp | `/props`, `/slots` | model file name, window, quantization; whether a slot is working and how many tokens it has decoded |
| Anything OpenAI-compatible | `/v1/models` | the model names |

The first route that answers with the expected shape decides the kind.

The rules that keep this safe: loopback only; the edge's own ports, its model
processes (anything below `llama-swap`, `cia-edge` or the supervisor) and the
monitor itself are never probed; no credential is sent, and an API that asks for
one is reported as protected; redirects are not followed; a body over 1 MiB is
discarded; responses are decoded into small fixed shapes; at most sixteen
probes a pass, six at a time. A known tool that holds GPU memory but offers no
API is still listed, with its VRAM and utilization and a note saying the model
could not be read.

### 4. Activity is derived from the evidence there is

A new pair of phases, `external` and `external_ready`, describes a machine on
which another tool is working or holds a model while the edge has nothing to
say. The edge still leads whenever it is doing something, with the other tool
noted beside it. Whether a source is generating comes from its own report when
it gives one (llama.cpp's slots, which also give a token rate from the change in
decoded tokens between looks) and from the tool's share of the adapter when it
does not, and the page says which. When the edge does not answer, the page no
longer says "Edge indisponível" and stops: it says so in a notice and keeps
describing the machine.

### 5. The models list follows the disk

The edge reports whether each model's weights file is still where the manifest
says and whether it has the deployed size, from `stat` alone, so a deleted or
truncated file shows on the next read instead of after the next release. A file
the edge cannot stat is unknown, never missing. The paths stay out of the
status. The Modelos tab also lists, from each tool's own API, what other tools
hold: loaded models and, for LM Studio / Bionic (`/api/v0/models` lists every
downloaded model) and Ollama (`/api/tags`), the installed library.

### 6. A protected server's model is read from its launch arguments

LM Studio and Bionic start their model process with an HTTP API behind a key of
their own, so the probes of section 3 are answered with a refusal and the page
knew nothing about the model. The launch arguments still name it.

The first version of this record rejected reading command lines on the ground
that it requires opening a process for memory reads. That was wrong:
`NtQueryInformationProcess` with `ProcessCommandLineInformation` (Windows 8.1
and later) needs only `PROCESS_QUERY_LIMITED_INFORMATION`, the same access the
monitor already uses for a process's image path, and the kernel copies the string
out.

What makes it acceptable is what is done with the line, because it is also where
such a tool puts its key. It is decomposed and reduced inside one function to an
allowlist (`--model`/`-m`, `--alias`, `--ctx-size`/`-c`, `--parallel`/`-np`,
`--n-gpu-layers`/`-ngl`), the file to its base name, and only that struct leaves.
The raw line is never stored, logged, sent to the page or used to call the API;
the key is not read into any field. It is used only for a source with no model
from an API, and shown with a note that says where the name came from. The
monitor also reports the working set and start time of the process holding the
model.

### 7. Ending the process that holds another tool's model

The edge cannot unload a model it did not load, and the monitor holds no
credential for the tool that did. So the page can ask the monitor to end the
process that holds it, with the same guards as the edge's own actions: the same
origin and peer checks, one operation at a time, and a native confirmation that
defaults to Cancel and expires. Beyond them:

- the page names a source and never a process: the monitor resolves the
  process from its own last discovery, which is the process holding the most
  dedicated GPU memory among the source's, and never one that descends from the
  edge;
- just before the dialog it re-identifies the process (same user, same program
  name) and again through the handle it ends, where it also compares the
  creation time, so a pid reused by another program is not ended;
- a fixed list of system, desktop and deployment programs, the monitor's own pid
  and pids up to 4 are never ended;
- the dialog says that the action does not go through the edge and what will
  happen to the tool; it follows `-admin-pipe off`, which removes every control.

### 8. Other tools' activity is written down

A tool the monitor cannot see inside leaves no request record, so the monitor
records what it did see: a stretch of time in which a source was working, its
peak GPU use and board power, the board's energy in that period (the whole
board's, desktop baseline included) and, for a server that reports slots, the
mean token rate. These appear in the requests table marked "externa". They are
not requests: there is no prompt and no token count.

### 9. Requests of a server that has its own log are read from that log

Section 8 gave up on numbers for a tool the monitor cannot see inside. The
llama.cpp server is what LM Studio and Bionic run underneath, and it writes each
request's counts to its log whatever the front end is, so the numbers exist; the
monitor only has to read them.

- **Where.** Bionic and LM Studio write the server's output under
  `~\.lmstudio\apps\bionic\server-logs` (or `~\.lmstudio\server-logs`), one file
  a day; a llama-server started by hand names its own with `--log-file`, which
  the launch arguments of section 6 already carry. The newest file is followed.
- **What is kept.** Four kinds of line are recognised - a slot taking a task, the
  slot choosing how much of its cache to reuse, the prompt and generation timings,
  and the slot's release with its token count - and only their numbers are read.
  A line that is anything else, which is where a server at high verbosity may
  write prompt or response text, is dropped by the same pass that reads the
  others, unread. No text from the log reaches the snapshot, the page or a file.
- **Derivation, not scraping of a summary.** The server never prints "cached
  tokens", so the prompt is derived: tokens held when the slot was released, plus
  one, minus what was generated, is the whole prompt, and what was not evaluated
  is what came from the cache. Run against a standalone server, this matched the
  server's own `usage` and `timings` for every request, cached and cold. Speeds
  are the server's own figures.
- **Time.** Bionic's lines carry a wall-clock stamp. A server's own `--log-file`
  does not, so a request is dated by when it was read minus its duration, and what
  the file already held when the monitor arrived is not reported at all rather
  than given today's date.
- **Following.** The tailer reads the last MiB on its first look, then what was
  appended, holds back a half-written line, and starts again on rotation or
  truncation. A server whose log cannot be found or read is left as it was.
- **One table.** These requests join the edge's in one table, oldest last, with a
  "via" line that says which tool served each and whether the figures were
  measured at the edge or in a server log. The speed, context and cache figures
  above the table follow the newest measured request from either. A source that
  is metered this way gets no GPU-only estimate for the same period, and an
  estimate that overlaps a logged request of the same source is dropped.
- **Probing.** Reading a protected server's log means the page no longer needs
  its API, so a refusal is remembered - 60 s for a 401/403, 30 s for anything
  else unusable - instead of being repeated every pass. Bionic's log had filled
  with a 401 every two seconds; a success is never cached.
- **What it does not cover.** A server whose front end is not llama.cpp keeps
  section 8: Ollama, and any OpenAI-compatible server that writes no per-request
  log, show activity, GPU and energy, with a note that says so and that pointing
  the tool at the edge gives the full view. llama.cpp writes these lines at its
  default logging level, so a server started with logging turned down or off is
  in this group too, and the source card says no timing lines were seen.

### 10. One feed, explicit evidence and bounded coverage

`/api/snapshot.requests[]` merges the edge's recent requests with finished
requests read from another llama.cpp server's log. It is bounded to 64 records
and excludes GPU-only activity: GPU load cannot establish a request or token
count. The older `edge.inference.recent` and `external_activity` fields remain
for clients that already use them.

Each record's `measurements` labels the origin of prompt, cache, output and
speeds separately. The edge distinguishes runtime `usage`, runtime `timings`,
stream-event estimates and wall-clock estimates. For an external server, prompt
and cache are derived from its slot state in the log; output and speeds come
from timing lines. Missing fields stay null. `coverage` counts detected sources
with a readable per-request log separately from sources with activity only;
it makes no claim about undetected programs.

When a streamed response has no runtime token count, its event count is an
estimate, not a token count: it is shown on the request with that label and
accumulated in `estimated_output_events`, outside the exact output-token total.
An older edge may still include those events in its total; the page identifies
that older telemetry rather than silently asserting exactness.

The router's current `/metrics` response carries system and GPU counters but
does not expose the unloaded model's token counters. No request numbers are
invented from it. When an external model is loaded and the edge refuses
admission for physical memory, the page explains the existing confirmed
"Encerrar processo do modelo" operation; it never ends that process on its own.

## Consequences

- **Exact per-request numbers come from the edge, or from a llama.cpp server's
  own log (section 9).** Ollama and other servers that expose no live token rates
  and write no request log show the model, its window, the GPU and the energy,
  and say the speed is not reported. Pointing the tool at the edge's
  OpenAI-compatible URL is what buys the full view, and the Connection tab shows
  that URL.
- **LM Studio's API must be on.** If the local server is off, the tool is still
  listed from its GPU use, without the model.
- **The snapshot discloses more.** Any local process that can read the monitor's
  loopback port now learns which programs use the GPU and which models other
  tools have loaded. The threat model records this.
- **Off Windows** discovery and the GPU list are empty; the edge's view is
  unchanged.

## Alternatives considered

- **Reading command lines** to learn the model file. It requires opening a
  process for memory reads, which the monitor's design forbids; the tool's own
  API gives the same fact without it.
- **A proxy in front of every tool** for exact per-request numbers. It would
  make the monitor a component in the data path, which ADR 0019 rejected.
- **ETW or WMI for power.** Neither exposes board power for this hardware.
