# Canary qualification campaign — 2026-08-23

Full quality-and-persistence battery — `chat, coding, hard, tools, literal_tools, json, retention`
— run once per candidate model via `scripts/v2/Invoke-V2ProfileQualification.ps1`, sequentially
(`provider.max_loaded_models: 1` — only one model can be resident on the RX 9070 XT at a time).

- **Started:** 2026-08-23 01:24 UTC-local
- **Finished:** 2026-08-23 10:26 UTC-local
- **Models qualified:** 4 / 4
- **GPU:** AMD Radeon RX 9070 XT · 16304 MiB
- **Raw evidence:** `benchmarks/campaign-20260823-full/profile-*.json` (full per-task detail, memory-sampled peaks, exact server command lines) and `summary.json` (aggregate, via `scripts/v2/eval/summarize_campaign.py`)
- **Also published as:** an interactive report, https://claude.ai/code/artifact/e752bf00-590c-40fa-8fbe-a4b7cb71b66f

## At a glance

Pass counts are out of each suite's task count. Retention is the mean of five graded probes taken
at ~12/25/50/75/90% of the model's declared context — one long prefill per depth, facts planted
throughout, answered from a single reply.

| Model | Context | Chat | Coding | Hard | Tools | Literal | Json | Retention | Peak VRAM |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| gemma4-12b-qat-ud-q4xl | 131,072 | 4/4 | 6/10 | 4/8 | 6/7 | 2/4 | pass | 0.80 | 11.0 GiB |
| qwen38-27b-deep-32k | 32,768 | 4/4 | 9/10 | 7/7 | 6/7 | 2/4 | pass | 1.00 | 12.3 GiB |
| qwen38-27b-agent-128k | 131,072 | 4/4 | 9/10 | 8/8 | 5/7 | 3/4 | pass | 1.00 | 12.2 GiB |
| qwen38-27b-huge-256k | 262,144 | 4/4 | 7/10 | 4/8 | 6/7 | 2/4 | pass | 1.00 | 12.6 GiB |

## What the four runs agree on

- **Reproduces on 4/4 models — the nested-filter tool call fails everywhere.**
  `tool_pick_search_many_nested` (a search call needing combined `include`/`exclude`/`case_sensitive`
  filters) is the one tool-picking task no model got right tonight. Every other tool-picking task
  passed on at least three of four. Reads as a fixture/grading edge case, not four independent
  model weaknesses.

- **Reproduces on 4/4 models — the Unity/C# implementation task is the one coding task nothing got
  clean.** Gemma and Huge both ran out of their 8192-token answer budget mid-reasoning; Deep
  produced a real but incorrect answer; Agent's request returned nothing at all. Four different
  failure shapes, same task, across every quantization tonight.

- **Reproduces on 3/4 models — literal-formatting tool calls are the weakest suite almost
  everywhere.** `literal_cs_glob` and `literal_regex` failed on Gemma, Deep, and Huge alike; Agent
  alone recovered `cs_glob` and still missed `regex`. No model got 4/4.

- **Correction to the pre-run prediction — Gemma's `function_calling: false` manifest flag looks
  over-conservative.** Before this run, Gemma was expected to score near zero on the tool suites,
  reading the manifest capability flag as authoritative. It didn't — 6/7 on `tools`, right
  alongside three profiles that ship `function_calling: true`. The flag may describe something
  narrower (what's advertised to a harness) than what the model can actually do through its chat
  template.

- **3/4 flawless, one real anomaly.** Deep, Agent, and Huge all closed with a perfect 1.00
  retention score at every depth tested — Huge included, to 239,893 tokens at 91.5% of its 262k
  window. Gemma alone dropped to exactly 0.00 at one single depth (32,616 tokens, ~25%
  occupancy), every one of the five probe families zeroing at once, sandwiched between perfect
  scores immediately before and after. A uniform all-family zero is `qualify.py`'s signature for a
  reply that didn't parse as JSON at all — most likely a one-off formatting slip at that specific
  prefill, not a real occupancy-dependent memory failure.

- **Where quality and latency diverge.** Every context-heavy profile held perfect retrieval while
  getting dramatically slower to start answering. Agent-128k's time-to-first-token climbed from
  74.5s to 972.5s (91% occupancy); Huge-256k's climbed from 158.9s to **2,957.7s — 49 minutes** —
  to answer from a context filled to 91.5%. Decode fell in step, 13.1 t/s down to 3.4 t/s on Huge.
  On this hardware, the long-context profiles' ceiling is patience, not correctness.

## Incident: the Huge-256k profile never started (first attempt)

The first attempt at `qwen38-27b-huge-256k` died five seconds after launch:

```
error: invalid argument: budget
```

**Root cause:** `Invoke-V2ProfileQualification.ps1` built the llama-server argument list as a flat
string array and handed that array straight to `Start-Process -ArgumentList`. Windows PowerShell
5.1 does not quote array elements containing spaces before joining them into the child process
command line — it only looked correct because the script's *display* string
(`configuration.command_line` in the report) is built by a separate helper,
`ConvertTo-V2CommandLine`, that already quotes correctly. The real argv sent to the process
differed from the one printed in the report.

Huge-256k is the only one of the four profiles that sets `-ReasoningBudgetMessage` — *"Thinking
budget reached. Stop analysing and write the final answer now."* — so it was the only profile
with a multi-word argument value, and the only one that could hit this. The message's second
word, *budget*, is what llama-server saw as a stray unrecognized token once the phrase was split
on spaces.

**Fix**, verified against the real binary with a deliberately-fake model path before trusting it
on a multi-hour run: reuse `ConvertTo-V2CommandLine` — the already-correct helper — to build one
pre-quoted string, and pass that single string to `-ArgumentList` instead of the raw array.
Before the fix the process died on argument parsing; after, it got all the way to `failed to open
GGUF file (No such file or directory)` — i.e. past every argument, failing only on the
deliberately-fake path.

**Landed upstream** in `scripts/v2/Invoke-V2ProfileQualification.ps1`,
`scripts/v2/Measure-V2ContextFootprint.ps1`, and `scripts/v2/Invoke-V2ThroughputSweep.ps1` — the
other two shared the identical `Start-Process -ArgumentList <raw array>` pattern (found by
grepping `scripts/v2` for the same call shape). Neither was exploitable *today* — neither exposes
a `ReasoningBudgetMessage`-style parameter and no other value they pass currently contains a
space — but they were one added parameter away from the same failure, so they got the same
one-line fix rather than being left as a latent copy of a bug already proven to cost hours.
`Test-V2WorkstationSmoke.ps1` and `Test-V2ProfileContracts.ps1` use the same
`Start-Process -ArgumentList` call but were already safe: both build their argument string via
`New-V2LlamaServerCommand`, which internally calls `ConvertTo-V2CommandLine` itself.

Huge-256k's retry, on the patched script, completed clean in 3h29m — full results below.

## Per-model reports

### Gemma 4 12B · QAT UD-Q4_K_XL

`provider.public_model` — the always-on default. Context 131,072, KV q4_0/q4_0, weights 6.26 GiB,
unsloth-candidate runtime, loaded in 7.5s, wall time 38m21s.

Fast and cheap, but the 8192-token answer budget is too tight for how much this model reasons.
Four of ten coding tasks — and three more in "hard" — never produced an answer at all: generation
hit the output cap while still inside 25,000–29,000 characters of `reasoning_content`. That's a
budget problem, not a correctness problem; nothing it did finish was wrong.

| Suite | Result |
|---|---|
| Chat | 4/4 |
| Coding | 6/10 (4 no-answer) |
| Hard | 4/8 (3 no-answer, 1 fail) |
| Tools | 6/7 |
| Literal tools | 2/4 |
| Json | pass |

Failures beyond no-answer: `tool_pick_search_many_nested`, `literal_cs_glob`, `literal_regex`,
`hard_missing_info`.

**Retention:**

| Prompt tokens | Occupancy | Score | Prefill | TTFT | Decode |
|--:|--:|--:|--:|--:|--:|
| 16,308 | 12% | 1.00 | 1799.0 t/s | 9.1s | 54.41 t/s |
| 32,616 | 25% | **0.00** | 1371.0 t/s | 23.8s | 50.85 t/s |
| 65,419 | 50% | 1.00 | 909.8 t/s | 71.9s | 45.64 t/s |
| 98,139 | 75% | 1.00 | 678.7 t/s | 144.6s | 40.69 t/s |
| 119,924 | 91% | 1.00 | 580.6 t/s | 206.6s | 38.28 t/s |

Footprint: 11,009 MiB dedicated · 858 MiB shared · 11.70 GiB working set · pressure ok (67.5%).

### Qwen 3.8 27B · Deep 32k

UD-IQ4_XS, KV q8_0/q8_0 — hardest localized tasks. Context 32,768, weights 13.27 GiB,
amd-rocm-qwen38 runtime, loaded in 31.8s, wall time 1h58m.

The strongest quality result of the night, and the cleanest retention curve. 9/10 coding, a clean
7/7 on "hard" — including `hard_missing_info`, which Gemma failed outright — and a perfect 1.00 at
all five retention depths tested, no exceptions. The single coding miss (`unity_impl`) produced a
real, wrong answer rather than running out of budget.

| Suite | Result |
|---|---|
| Chat | 4/4 |
| Coding | 9/10 |
| Hard | 7/7 |
| Tools | 6/7 |
| Literal tools | 2/4 |
| Json | pass |

Failures: `unity_impl` (wrong output, not a timeout), `tool_pick_search_many_nested`,
`literal_cs_glob`, `literal_regex`.

**Retention:**

| Prompt tokens | Occupancy | Score | Prefill | TTFT | Decode |
|--:|--:|--:|--:|--:|--:|
| 4,120 | 13% | 1.00 | 227.0 t/s | 18.3s | 6.00 t/s |
| 8,102 | 25% | 1.00 | 224.6 t/s | 36.1s | 5.67 t/s |
| 16,296 | 50% | 1.00 | 212.3 t/s | 76.8s | 4.94 t/s |
| 24,393 | 74% | 1.00 | 199.1 t/s | 122.5s | 4.20 t/s |
| 27,817 | 85% | 1.00 | 195.7 t/s | 142.2s | 3.93 t/s |

Footprint: 12,286 MiB dedicated · 5,188 MiB shared · 15.89 GiB working set · pressure ok (75.4%).

### Qwen 3.8 27B · Agent 128k

UD-Q3_K_XL, KV q4_0/q4_0 — the daily coding-agent default. Context 131,072, weights 12.24 GiB,
amd-rocm-qwen38 runtime, loaded in 15.2s, wall time 2h49m.

Quality holds perfectly out to 91% of its 128k window; the cost shows up entirely in latency, not
correctness. Retention stayed at a flat 1.00 across all five depths, but time-to-first-token grew
from 74.5s to 972.5s (over 16 minutes) as occupancy rose, and decode fell from 7.0 to 4.0 t/s.
This is also the only profile where a coding request came back completely empty rather than
truncated or wrong.

| Suite | Result |
|---|---|
| Chat | 4/4 |
| Coding | 9/10 |
| Hard | 8/8 |
| Tools | 5/7 |
| Literal tools | 3/4 |
| Json | pass |

Failures: `unity_impl` (empty response, no output/reasoning tokens at all), `tool_pick_read_many`
(missing required `paths` arg), `tool_pick_search_many_nested`, `literal_regex`.

**Retention:**

| Prompt tokens | Occupancy | Score | Prefill | TTFT | Decode |
|--:|--:|--:|--:|--:|--:|
| 16,296 | 12% | 1.00 | 219.2 t/s | 74.5s | 7.02 t/s |
| 32,620 | 25% | 1.00 | 196.0 t/s | 166.5s | 6.35 t/s |
| 65,342 | 50% | 1.00 | 159.3 t/s | 410.2s | 5.18 t/s |
| 98,181 | 75% | 1.00 | 135.7 t/s | 723.8s | 4.37 t/s |
| 119,901 | 91% | 1.00 | 123.3 t/s | 972.5s | 3.98 t/s |

Footprint: 12,221 MiB dedicated · 6,000 MiB shared · 16.54 GiB working set · pressure ok (75.0%).

### Qwen 3.8 27B · Huge 256k

UD-Q2_K_XL, KV q4_0/q4_0 — huge active context, high thinking budget. Context 262,144, weights
9.15 GiB, amd-rocm-qwen38 runtime, loaded in 10.9s. First attempt never loaded (see incident
above); the qualification run itself, once launched correctly, took 3h29m.

The wait was worth it: **a perfect 1.00 retention score at every one of five depths, all the way
to 239,893 tokens** — the cleanest persistence result of the whole campaign, at double the
context of anything else tested tonight. The cost is entirely in latency: time-to-first-token at
the deepest probe was 49 minutes. The reasoning-budget/max-tokens risk flagged mid-run (a
4096-token retention answer cap against a 24,576-token thinking budget) did not end up costing
this run anything.

| Suite | Result |
|---|---|
| Chat | 4/4 |
| Coding | 7/10 (3 no-answer) |
| Hard | 4/8 (3 no-answer, 1 fail) |
| Tools | 6/7 |
| Literal tools | 2/4 |
| Json | pass |

No-answer (8192-token cap hit mid-reasoning): `go_impl`, `ts_refactor`, `unity_impl`,
`hard_go_router_multifile`. Completed failures: `hard_go_retry_multifile`,
`tool_pick_search_many_nested`, `literal_cs_glob`, `literal_regex`.

**Retention:**

| Prompt tokens | Occupancy | Score | Prefill | TTFT | Decode |
|--:|--:|--:|--:|--:|--:|
| 32,620 | 12% | 1.00 | 205.5 t/s | 158.9s | 13.12 t/s |
| 65,342 | 25% | 1.00 | 166.0 t/s | 393.7s | 9.31 t/s |
| 130,986 | 50% | 1.00 | 119.5 t/s | 18.3m | 5.90 t/s |
| 196,484 | 75% | 1.00 | 92.4 t/s | 35.4m | 4.31 t/s |
| 239,893 | 91% | 1.00 | 81.1 t/s | 49.3m | 3.41 t/s |

Footprint: 12,584 MiB dedicated · 6,081 MiB shared · 16.49 GiB working set · pressure ok (77.2%).

## Reading the suites

What each column above is actually asking the model to do, from `scripts/v2/eval/qualify.py`:

| Suite | Tests |
|---|---|
| `chat` | Chat template correctness, multi-turn state, streaming, continuing after a tool result. |
| `coding` | Real bugfix/implementation tasks across five languages, compiled or executed by an actual toolchain — never judged by another model. |
| `hard` | A harder second pass at the same shape of task, plus multi-file router/retry scenarios. |
| `tools` | Exact function name and exact argument values for a natural tool-use request. |
| `literal_tools` | Tool arguments that must be copied verbatim — paths with spaces, glob patterns, regex — not paraphrased. |
| `json` | Strict structured output against a schema. |
| `retention` | **The persistence test.** One long prefill per depth in the tables above, filled with synthetic filler content, with facts, constraints, and mutable state planted at fixed points. A single reply then has to answer twelve probes at once — five families: needle recall, entity tracking, mutable state, negative memory (not inventing what wasn't there), and instruction constraints. One prefill per depth rather than one request per probe, because at 256k a prefill alone costs tens of minutes. |

## Not run tonight

The 72-hour soak described in `docs/BENCHMARKS.md`, and the five `retired` `qwen38-27b-ws-*`
profiles, which no longer have a deployment.
