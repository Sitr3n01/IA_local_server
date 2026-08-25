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

---

# Errata and post-run audit — added 2026-08-23, after re-reading the raw evidence

Everything above this line is the campaign as it was written on the night. Nothing in it has been
edited, and no measured number anywhere in `benchmarks/campaign-20260823-full/` has been touched.
This section records what a second pass over the raw JSON says about the *interpretation* — three
findings that change what the results mean, and two transcription errors.

**None of it requalifies anything.** Where a conclusion depends on a re-measurement, that is said
plainly and the measurement is scheduled, not assumed.

## E1. The campaign measured a generation contract no profile actually deploys

The largest finding, and it is a harness defect rather than a model result.

`Invoke-V2ProfileQualification.ps1` forwarded `--max-tokens` to `qualify.py` **only when an
operator passed `-MaxTokens` explicitly**. All four cells ran without it —
`diagnostic_config.max_tokens_override` is `null` in every one of the four `qualify-*.json` files
— so every suite fell back to its own fixture default: 8192 for coding and hard, 4096 for tools,
json and retention.

For `qwen38-27b-huge-256k` that is not a tuning detail, it is a contradiction:

| | Value | Where from |
|---|--:|---|
| `max_output_tokens` | 32,768 | manifest |
| `n_predict` (`--n-predict`) | 32,768 | manifest → llama-server |
| `reasoning_budget` (`--reasoning-budget`) | 24,576 | manifest → llama-server |
| Answer reserve the profile intends | 8,192 | 32768 − 24576 |
| **HTTP `max_tokens` the benchmark actually sent** | **8,192** | coding-fixture default |

The server was told it could think for up to 24,576 tokens and then answer inside a 32,768-token
ceiling. The benchmark's own request cut generation off at 8,192 — below the thinking budget
alone. Huge's reasoning budget never had a chance to hand over to an answer, because the request
ended 16,384 tokens before the budget was even reachable.

The three Huge `NO_ANSWER` rows in `coding` and the four in `hard` all stopped at exactly
`output_tokens: 8192` with `finish_reason: length`. They are evidence about an 8,192-token
request, and they say nothing about how Huge behaves under the 32,768-token contract it is
actually served with. The report's line above — *"The reasoning-budget/max-tokens risk flagged
mid-run … did not end up costing this run anything"* — was written about the retention suite and
is correct for retention; it does not hold for coding and hard, where the same mismatch produced
seven no-answers.

Gemma is unaffected by the *mismatch* (it declares no `n_predict`, so 8,192 was both the fixture
default and a fair reading of its contract) but is affected by the *ceiling*; see E4.

**Fixed.** The request ceiling is now derived rather than defaulted, by one function
(`Resolve-V2QualificationRequestBudget`) that the manifest validator and the qualification runner
share: an explicit `-MaxTokens` wins, otherwise a positive `n_predict` becomes the ceiling,
otherwise the fixtures keep their defaults. *(Sharpened later the same day: the explicit ceiling
still wins for what the **request** asks, but every budget rule is now evaluated against
`min(request_max_tokens, n_predict)` — the server stops at `n_predict` regardless. See
[`HARDENING-post-qualification-20260823.md`](HARDENING-post-qualification-20260823.md) §1.)*

A ceiling that cannot hold the profile's answer reserve is refused **before the model loads**;
measuring one on purpose now requires `-ConstrainedRequestBudgetDiagnostic`, and stamps the report
as a diagnostic cell. Every qualification report now carries `request_budget` — the effective
ceiling, where it came from, the reasoning budget, and the resulting answer reserve — so this can
never again be a thing a reader has to reconstruct.

Coding fixtures were **not** raised from 8192 to 32768. The request limit is a profile contract,
not a property of the coding problem; raising the fixture would have changed what every other
profile is measured against in order to fix one.

## E2. "4/4 failed `unity_impl`" is four different failures, not one fixture defect

The summary above groups the four Unity failures as one reproducing signal. The raw rows do not
support that reading — they have four distinct causes, and only one is a statement about Unity
code quality:

| Profile | `finish_reason` | out tok | Raw verdict | What it actually was |
|---|---|--:|---|---|
| `qwen38-27b-deep-32k` | `stop` | 1,766 | `COMPILE_ERROR` | **A real model defect.** See below. |
| `gemma4-12b-qat-ud-q4xl` | `length` | 8,192 | `NO_ANSWER` | Budget exhausted at 25,159 reasoning chars |
| `qwen38-27b-huge-256k` | `length` | 8,192 | `NO_ANSWER` | Budget exhausted — and under the E1 cap |
| `qwen38-27b-agent-128k` | *(none)* | *(none)* | `request failed: timed out` | **A 900-second HTTP timeout**, not a model reply |

The report describes Agent's result as *"empty response, no output/reasoning tokens at all"*. The
raw row is `{"error": "request failed: timed out"}` — the request never returned. It carried no
`failure_taxonomy` at all, which is how a transport failure came to be written up as a model
behaviour. (The same thing happened to Deep's `hard_go_retry_multifile`.)

So one model produced a wrong answer, two ran out of budget, and one never answered. That is not
four models agreeing about a fixture.

### Deep's failure is genuine, and stays genuine

`qwen38-27b-deep-32k` declared `GameObject instance` inside the `if (pool.Count > 0)` block of
`Rent()` and again at method scope. C# rejects that with **CS0136** regardless of order, and
`dotnet build` said so. The fixture was not relaxed, not rewritten around the answer, and given no
model-specific exception.

What was added is a way to settle the question by running something rather than by re-reading a
report. `scripts/v2/eval/test_verifiers.py` now carries three named `unity_impl` regressions:

1. a known-good `ProjectilePool` **compiles** against `UNITY_SHIM` — the fixture is passable;
2. Deep's 2026-08-23 answer, verbatim, **does not compile**, and the toolchain output must contain
   `CS0136` — the failure is real and it is the model's;
3. the same answer with only the second declaration renamed **compiles** — the gate rejected the
   scoping defect, not the approach.

Gemma's, Huge's and Agent's Unity quality remains **unmeasured**. Two hit a budget ceiling that E1
has now corrected and one never completed a request; all three need re-measurement before anything
is said about them.

## E3. The nested-tool failures are partly the fixture's fault, and the score stands anyway

`tool_pick_search_many_nested` asked for *"all Go files … excluding vendor"* and graded against
`include: ["*.go"]`, `exclude: ["vendor/**"]`. The synthetic user never said either glob. What the
four models emitted:

| Profile | `include` | `exclude` |
|---|---|---|
| `gemma4-12b-qat-ud-q4xl` | `["**/*.go"]` | `["vendor/**"]` ✓ |
| `qwen38-27b-deep-32k` | `["*.go"]` ✓ | `["vendor/"]` |
| `qwen38-27b-agent-128k` | `["*.go"]` ✓ | *(omitted)* |
| `qwen38-27b-huge-256k` | `["*.go"]` ✓ | *(omitted)* |

Every one selected `search_files` correctly and built the nested `filters` object correctly. Two
of them were then failed on a literal the prompt never supplied. `literal_regex` had the identical
defect (*"excluding vendor"* → wants `vendor/**`), and `literal_cs_glob` demanded
`case_sensitive: true` — a *required* field in the tool schema — from a prompt that never
mentioned case sensitivity at all.

This is worth separating from a model weakness because the same task also failed on all four
Qwen3.6-35B-A3B quantizations in the 2026-08-22 campaign
(`benchmarks/REPORT-qwen36-35b-a3b-gfx1201-20260822.md` §11.1), where it was read as *"a property
of the model rather than of the bits"*. Eight model configurations across two campaigns have now
failed a task that asks a model to guess a literal and then grades it for guessing wrong.

**Corrected for the future, not for the past.** The three prompts now state every literal the
grader demands, byte-for-byte. **No expected value changed**, no verifier was loosened, and
`vendor`, `vendor/`, `vendor/*` and `vendor/**` remain four different arguments — detecting
argument mutation is the entire point of the strict contract. The 2026-08-23 pass/fail counts are
unchanged and remain the record of what was measured on the night.

A new `scripts/v2/eval/test_tool_grading.py` asserts, deterministically and without a model, that
every literal a fixture grades byte-exactly is supplied byte-exactly in its prompt — so this class
of defect cannot come back quietly.

While writing those tests the comparator itself turned out to be looser than the contract it
advertises: tool-argument strings were compared through `norm()`, which folds case and strips
whitespace, so `Vendor/**` matched `vendor/**` and `FOO[0-9]+` matched `foo[0-9]+`.
`literal_identifier` asks a model to preserve case exactly and the grader could not see case.
Tool-argument strings are now compared byte-for-byte. Re-graded against all 44 tool rows in the
raw 2026-08-23 arguments this changes **no historical verdict** — the hole was latent, not
load-bearing — so the scores above stand as measured under either comparator.

## E4. Gemma's no-answers are a budget behaviour, and a candidate fix is staged but unproven

Seven of Gemma's failures across `coding` and `hard` are the same event: `finish_reason: length`
at exactly 8,192 output tokens, with 25,159–29,818 characters still in `reasoning_content`. None
of them is a correctness result.

The separation is clean in the raw data:

| | reasoning chars | output tokens |
|---|---|---|
| 10 passing rows | 1,386 – 17,202 | 503 – 6,125 |
| 7 no-answer rows | 25,159 – 29,818 | 8,192 (all seven) |

No passing run came close to the ceiling; every failing run sat exactly on it.

`benchmarks/REPORT-gemma4-26b-a4b-gfx1201-20260821.md` §14 already measured the shape of the fix
on Gemma 4 26B A4B: raising the output cap alone does **not** help (8192 → no answer at 17,147
reasoning chars; 16384 → no answer again at 33,795), while pairing a raised ceiling with a bounded
reasoning budget does (16384/8192, 16384/4096, 16384/2048 and 16384/0 all reached 6/6 diagnostic
coding). That report also warns explicitly against copying Qwen Huge's `reasoning_budget: 24576`
into Gemma.

A candidate configuration is therefore **staged and left unqualified** in
`docs/reports/HARDENING-post-qualification-20260823.md`. `config/models.yaml` is unchanged:
`gemma4-12b-qat-ud-q4xl` is `provider.public_model`, the always-on default, and changing what it
serves on the strength of static analysis would alter real user behaviour with no physical
evidence behind it. The evidence for the candidate comes from a different Gemma size, and that
gap is exactly what the next physical run is for.

## E5. Two transcription errors in the summary tables above

Both are in the hand-written per-model tables; the raw JSON is correct and unchanged.

| Model | Report says | `qualify-*.json` says |
|---|---|---|
| `qwen38-27b-deep-32k` | Hard **7/7** | `passed: 7, total: 8` — `hard_go_retry_multifile` was a 900s request timeout |
| `qwen38-27b-agent-128k` | Hard **8/8** | `passed: 7, total: 8` — `hard_go_retry_multifile` failed on test output |

Neither model ran a 7-task or a 9-task hard suite; both ran 8. Deep's missing row is a transport
failure (`REQUEST_TIMEOUT`) and Agent's is a real test failure, so the two 7/8s do not mean the
same thing either.

The counts in the tables above are left as written, because this section is an erratum and not a
rewrite. `summarize_campaign.py` does not summarise the `hard` suite, which is why nothing caught
this at the time.

## What changed in the harness because of this

Deterministic, fast, and all covered by tests that run without a GPU:

| Change | Answers |
|---|---|
| `Resolve-V2QualificationRequestBudget` derives the request ceiling from the profile | The harness was measuring a different configuration from deployment |
| An impossible reasoning/ceiling pair is refused before the model loads | A deterministic configuration defect |
| `request_budget` recorded in every report | Reporting could not distinguish a deployment budget from a benchmark cap |
| `REQUEST_TIMEOUT` / `REQUEST_ERROR` / `MODEL_OUTPUT_FAILURE` and friends | Reporting was misclassifying a transport failure as a model result |
| Locale-aware `COMPILE_ERROR` matching | A pt-BR `dotnet` failure graded as a bare `TEST_FAILURE` |
| Three fixture prompts now state their literals | The fixtures were invalid: they graded literals they never supplied |
| Byte-exact tool-argument strings | The benchmark contract was internally inconsistent |
| `unity_impl` regressions, tool-grading self-test, argv-quoting regression | Nothing; these lock in verdicts that were argued about once |

Nothing here was changed because it made a model pass. The two changes that *could* raise a future
score — the fixture prompts — raise it only by asking the question the grader was already marking,
and the historical counts are untouched.

The changes themselves, the staged Gemma candidate, the Qwen3.6-35B-A3B status, and the exact
commands for the physical re-run are in
[`HARDENING-post-qualification-20260823.md`](HARDENING-post-qualification-20260823.md). **No model
was requalified by that work** — the re-run has not happened.
