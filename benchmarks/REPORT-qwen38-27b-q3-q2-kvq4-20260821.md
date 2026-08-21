# Qwen3.8-27B at Q3 and Q2 with KV `q4_0` — weight-quantization qualification

Date: 2026-08-21, America/Sao_Paulo.
Evidence labels per `docs/BENCHMARKS.md`. Everything is **measured** on this
machine unless marked otherwise.

This report does not supersede
`benchmarks/REPORT-qwen38-27b-gfx1201-20260821.md` or
`benchmarks/REPORT-workstation-memory-20260821.md`. Their figures stand; this
adds the weight-quantization axis those two left open, and the KV `q4_0`
quality evidence that `REPORT-workstation-memory` §7 explicitly listed as
missing.

## Scope

The campaign was started as a full progressive qualification toward a 256k
agentic profile and was narrowed by the operator, after the baseline envelope
was measured, to: **Q3 and Q2 weights with the KV cache at `q4_0`**. Sections
that the narrowed scope dropped are listed under "Not measured" rather than
quietly omitted.

## 1. Bill of materials

| Item | Value |
|---|---|
| GPU | AMD Radeon RX 9070 XT, gfx1201, 16304 MiB |
| CPU | AMD Ryzen 7 7700X, 8C/16T |
| RAM | 31.11 GiB DDR5; commit limit 61.1 GiB |
| OS | Windows 11 Pro 10.0.26200, pt-BR |
| Runtime | llama.cpp `b10549`, commit `b2e5e9b28`, ROCm 7.14 + ROCm 7.2.1 rocBLAS closure |
| Runtime path | `C:\IA\runtimes\llama.cpp\b10549-rocm-7.14` |
| Device isolation | `HIP_VISIBLE_DEVICES=1`, `--device ROCm0`, `--split-mode none` |
| Desktop idle VRAM | 3477–4146 MiB across the campaign (it moves; see §5) |

Held constant across every cell unless the cell is explicitly a sweep of it:
`-ngl 99`, `-fa on`, `-b 2048`, `-ub 288`, `-t 8`, `--parallel 1`,
`--no-context-shift`, `--jinja`, and the 4-block CPU split
`-ot "blk\.(6[0-3])\.ffn_.*=CPU"`.

## 2. GGUFs evaluated

Source repo `unsloth/Qwen3.8-27B-GGUF`, immutable revision
`4ca720788d1e01f1bff70c033e0d0028fd02e502`, Apache-2.0.

| Filename | Bytes | GiB | SHA-256 | Downloaded |
|---|---:|---:|---|---|
| `Qwen3.8-27B-UD-IQ4_XS.gguf` | 14252845984 | 13.27 | `40FAC405…70E6199` | pre-existing |
| `Qwen3.8-27B-UD-Q3_K_XL.gguf` | 13146393504 | 12.24 | `8C2A45FF85E7674CA185EC8EB6CDEAB0E617ED9D8018CAED0B64380EB2A67A5E` | yes |
| `Qwen3.8-27B-UD-Q2_K_XL.gguf` | 9828981664 | 9.15 | `FD4730DD8AAD070517978752B63D530AEB1740D2283CAB9FA24F1E404032DDB0` | yes |

## 3. MTP-head gate, before any download

`model-test-matrix.json` requires the `blk.64` multi-token-prediction head at
Q5_K or better; at Q4_K, draft acceptance measures 0% and speculation fails
silently. A GGUF's tensor-info block sits at the head of the file, so this is
answerable over HTTP range requests. `scripts/v2/eval/gguf_probe.py` reads
~13 MB per artifact and reports every `blk.64` tensor's quantization.

The probe was validated against the local `UD-IQ4_XS`, whose head was already
characterized in the prior report, and reproduced it exactly.

| Candidate | GiB | `blk.64` weight quants | Verdict |
|---|---:|---|---|
| `UD-IQ4_XS` *(baseline)* | 13.27 | Q6_K / Q8_0 | PASS |
| `UD-Q3_K_XL` | 12.24 | Q6_K / Q8_0 | PASS |
| `UD-IQ3_S` | 11.21 | Q6_K / Q8_0 | PASS |
| `UD-IQ3_XXS` | 10.18 | Q6_K / Q8_0 | PASS |
| `UD-Q2_K_XL` | 9.15 | Q6_K / Q8_0 | PASS |
| `UD-IQ2_S` | 7.80 | **no `blk.64` at all** | **NO_MTP_HEAD** |
| `UD-IQ2_XXS` | 6.77 | **no `blk.64` at all** | **NO_MTP_HEAD** |

**The two smallest Q2 variants do not carry the head at any precision.** Three
independent signals agree: `qwen35.block_count` is 64 rather than 65, the
tensor count is 851 rather than 866 — a difference of exactly the 15 `blk.64.*`
tensors — and there are zero `nextn` tensors. They were not quantized down;
they were converted without the head. They can serve normally and can never
speculate.

That collapses the Q2 tier to `UD-Q2_K_XL` alone, and it cost 25 MB of
bandwidth rather than 14 GB of download to establish. Total probe cost for all
seven candidates: ~88 MB.

**No candidate was disqualified by the MTP gate itself.** Every artifact that
carries a head carries it at Q6_K/Q8_0, comfortably above the Q5_K floor. The
gate's value here was structural, not precision-related.

## 4. Method

Nothing in the coding suite is graded by a model or by a regular expression over
prose. Each task is compiled or executed by a real toolchain:

| Language | Verifier | What it proves |
|---|---|---|
| Python | hidden test battery, `python` | runs and is correct on edge cases |
| Go | `go test ./...`, Go 1.26.6 | compiles and passes concurrency/order tests |
| TypeScript | `node` native type stripping, Node 24.18 | runs and is correct |
| C# | `dotnet build` + run, SDK 10.0.302 | compiles and produces the right answer |
| Unity C# | `dotnet build` against a `UnityEngine` shim | the MonoBehaviour type-checks |

`scripts/v2/eval/test_verifiers.py` exercises every task twice — once with a
reference solution that must pass and once with a plausible-but-wrong solution
that must fail. **20/20 graded correctly**, run before any model was asked a
question. A grader that accepts everything would have reported Q2 as equal to
IQ4 and the whole comparison would have rested on it.

Long-context retention is generated from a fixed seed by
`scripts/v2/eval/fixture_corpus.py` as a synthetic repository briefing, with
five probe families planted at controlled depths: exact needles at 5/25/50/75/90%,
a frozen-file constraint, a superseded API, a rejected approach, and four
similarly-named pools with different rules. Grading is per-probe and nuanced —
`exact`, `semantic`, `partial`, `stale`, `incorrect`, `hallucinated`, `missing` —
because a model that answers `UNKNOWN` is recoverable (the agent goes and looks)
and one that returns a confident wrong value is not.

Raw artifacts, per-cell JSON and the full model outputs are under
`benchmarks/campaign-256k/`.

## 5. Memory envelope at load, measured

One `llama-server` start per cell, sampled after `/health` reports ok.
`marginal` is peak dedicated minus the idle baseline taken immediately before
that cell, so the desktop's own share is removed. `shared` is the paging
signal — the quantity that predicts the prefill collapse documented in
`docs/TUNING.md` §1.6.

### 5.1 Baseline `UD-IQ4_XS` across all three KV pairs

| ctx | K/V | dedicated | shared | process WS | private | marginal | state |
|---:|---|---:|---:|---:|---:|---:|---|
| 32768 | q8_0/q8_0 | 16053 | 2480 | 13.23 | 12.73 | 12038 | pressured |
| 65536 | q8_0/q8_0 | 15594 | 3731 | 13.37 | 12.75 | 11447 | pressured |
| 131072 | q8_0/q8_0 | 15843 | 5981 | 16.59 | 17.05 | 12172 | pressured |
| 262144 | q8_0/q8_0 | 15867 | **10976** | 19.00 | 21.38 | 12227 | pressured |
| 32768 | q8_0/q4_0 | 15962 | 2384 | 13.53 | 12.79 | 11951 | pressured |
| 65536 | q8_0/q4_0 | 16048 | 3171 | 13.56 | 12.86 | 11910 | pressured |
| 131072 | q8_0/q4_0 | 15831 | 4986 | 16.87 | 16.26 | 11930 | pressured |
| 262144 | q8_0/q4_0 | 15868 | **8437** | 19.70 | 19.79 | 11965 | pressured |
| 32768 | q4_0/q4_0 | 15851 | 1875 | 13.31 | 12.73 | 11911 | pressured |
| 65536 | q4_0/q4_0 | 15854 | 2614 | 13.33 | 12.75 | 11971 | pressured |
| 131072 | q4_0/q4_0 | 15857 | 4094 | 13.37 | 12.79 | 11971 | pressured |
| 262144 | q4_0/q4_0 | 15868 | **7054** | 17.94 | 17.37 | 11985 | pressured |

Dedicated VRAM is pinned at 15.6–16.1 GiB in **every** cell and does not move
with context. The card is full at the weights alone, so the entire KV cache
lands in shared memory and is reached over PCIe. This is why `shared` rather
than `dedicated` is the number that decides whether a context level is
servable, and it is why 256k on IQ4_XS is out of envelope on every KV setting
including the cheapest.

Halving V alone (q8_0/q4_0) removes 2539 MiB at 256k; halving both removes
3922 MiB. Neither is enough to bring IQ4_XS under the 6 GiB shared line.

### 5.2 Q3 and Q2 at KV `q4_0/q4_0`

| ctx | model | dedicated | shared | process WS | private | marginal | state |
|---:|---|---:|---:|---:|---:|---:|---|
| 32768 | Q3_K_XL | 15886 | 1089 | 11.70 | 11.74 | 11885 | pressured |
| 65536 | Q3_K_XL | 15661 | 2027 | 11.71 | 11.76 | 11665 | pressured |
| 131072 | Q3_K_XL | 15347 | 3507 | 11.75 | 11.79 | 11607 | **ok** |
| 262144 | Q3_K_XL | 15556 | **5946** | 15.83 | 16.38 | 11817 | pressured |
| 32768 | Q2_K_XL | 12850 | **511** | 8.73 | 8.86 | 9372 | **ok** |
| 65536 | Q2_K_XL | 13575 | **529** | 8.75 | 8.88 | 10095 | **ok** |
| 131072 | Q2_K_XL | 15118 | **565** | 8.79 | 8.92 | 11639 | **ok** |
| 262144 | Q2_K_XL | 15518 | 2874 | 9.86 | 13.50 | 12039 | pressured |

**These are two different regimes, not two points on one curve.** Q2_K_XL's
*dedicated* usage rises with context — 12850 → 13575 → 15118 — while its shared
stays flat at 511–565 MiB. The KV cache is landing in real VRAM because the
weights left room for it. Q3_K_XL and IQ4_XS are saturated at the weights, so
their dedicated figure is constant and their KV goes straight to shared.

The practical consequences:

- **Q3_K_XL is the largest quantization that fits 256k inside the campaign's
  6 GiB shared ceiling**, and it fits with 198 MiB to spare. That is a real
  result, not a comfortable one.
- **Q2_K_XL is the only candidate that reaches 128k without paging at all**,
  at 565 MiB shared and an 8.79 GiB working set — roughly a third of the host
  memory the IQ4_XS 128k profile needs (16.59 GiB).
- At 256k, Q2_K_XL's process working set is 9.86 GiB against Q3_K_XL's 15.83
  and IQ4_XS's 17.94. On a 31 GiB workstation that difference is the
  difference between running Unity alongside the model and not.

## 6. A harness defect found and corrected mid-campaign

Recorded because it changed a result, and because any future run of this suite
inherits the correction.

The first Q3_K_XL pass scored `ts_refactor` and `cs_bugfix` as FAIL. Replaying
one of those prompts against the live server and dumping the raw message
envelope showed why:

```
finish_reason      : length
completion_tokens  : 2560        (the cap, exactly)
content            : len=0
reasoning_content  : len=11709
```

llama.cpp routes a thinking model's chain of thought into `reasoning_content`
and leaves `content` empty until the answer begins. A generation that reaches
`max_tokens` while still reasoning therefore returns an empty answer, and the
grader was compiling the empty string and recording a compile failure the model
never caused. The tool-calling suite was worse: at a 512-token budget it would
have reported "no tool call" on every task and the campaign would have concluded
that tool calling breaks at Q3.

Corrections, applied before any comparative number was taken:

- Output cap raised to **8192** for every task, matching `max_output_tokens` on
  the shipped manifest profiles; tools and structured output raised to 4096.
- `reasoning_content` is captured, and a generation that hits the cap with an
  empty answer is recorded as **`no_answer`** — never graded as broken code.
- Reasoning left **unrestricted**. `--reasoning-budget` exists on this build and
  would bound it, but capping thinking would measure a profile the repository
  does not ship. Reasoning to the cap is itself a low-bit failure mode and is now
  counted rather than hidden.

The partial artifacts from the contaminated run were deleted rather than kept,
so all three quantizations are compared under identical conditions.

**The correction was load-bearing.** Re-run at 8192, `ts_refactor` passes using
**5687** output tokens and `cs_bugfix` passes using **5547** — both more than
double the cap that had scored them as failures.

### 6.1 The output-reserve number this produces

Across ten tasks at Q3_K_XL, reasoning ranged from 2089 to 34896 characters for
work of broadly similar difficulty. Two consequences for the harness contract:

- **Qwen3.8-27B needs an output reserve of ~6k tokens, and 8k to be safe**,
  before it emits a single line of code. A harness configured with a 2–4k output
  budget will see empty replies and misdiagnose the model as broken.
- OpenCode's `limit.output: 8192` on the `qwen38-*` profiles is adequate, but
  only just. Auto-compaction thresholds (campaign §25) must reserve 8k for
  output rather than the 1–2k a non-reasoning model would need.

## 7. Weight quality at 32k, KV `q4_0/q4_0`

Held constant: ctx 32768, `-ctk q4_0 -ctv q4_0`, `-ub 288`, `-t 8`,
`--parallel 1`, 4-block CPU split, `--jinja`, temperature 0, seed 20260821,
output cap 8192.

### 7.1 `UD-Q3_K_XL` — 12.24 GiB

| Suite | Result |
|---|---|
| Coding | **9/10 pass**, 1 `no_answer` |
| Tool calling | **4/4 pass** |
| Structured JSON | **pass** |
| Session peak | dedicated 15919 MiB, shared 900 MiB, WS 16.35 GiB, private 18.99 GiB, `elevated` |

| Task | Verdict | Output tokens | Reasoning chars |
|---|---|---:|---:|
| `py_bugfix` | PASS | 722 | 2139 |
| `py_impl` | PASS | 623 | 2089 |
| `go_bugfix` | PASS | 2402 | 8396 |
| `go_impl` | PASS | 1709 | 4295 |
| `ts_impl` | PASS | 709 | 2252 |
| `ts_refactor` | PASS | 5687 | 23869 |
| `cs_bugfix` | PASS | 5547 | 21846 |
| `unity_impl` | **NO-ANSWER** | 8192 | 34896 |
| `constraint_frozen_file` | PASS | 1555 | 6262 |
| `no_invented_api` | PASS | 673 | 2545 |

Three results carry more weight than the headline count.

`constraint_frozen_file` passed: told `DO NOT MODIFY NetworkManager.cs`, the
model wrote a new `TelemetrySender` that truncates the over-long opcode on the
caller side and left the frozen file alone. Verified twice — by compiling
against the harness's own copy of the frozen file, and by scanning the model's
output for markers that would indicate it had rewritten it.

`tool_refuse_edit` passed: handed a bug that appears to live in the frozen file,
the model declined `edit_file` and called `list_dir` on the correct path. That
is the agentic behaviour the campaign is actually shopping for, and it survived
3-bit weights with a `q4_0` cache.

`unity_impl` returned no answer. This is a real finding rather than a harness
artifact: 8192 is the shipped output budget, and on this task the model reasons
past its own configured limit without emitting code. It is **not** evidence that
Q3 writes bad Unity code — nothing was produced to grade. The cap was left at
the shipped value rather than raised to accommodate the model's verbosity,
because raising it would measure a profile that is not served.

Note the gap between the load-time footprint (§5.2: WS 11.70 GiB) and the
serving peak (WS 16.35 GiB, private 18.99 GiB). Admission control gates on what
a model costs while answering, so the serving figure is the one a manifest entry
must carry.

### 7.2 `UD-Q2_K_XL` — 9.15 GiB

| Suite | Result |
|---|---|
| Coding | **7/10 pass**, 3 `no_answer` |
| Tool calling | **4/4 pass** |
| Structured JSON | **pass** |
| Session peak | dedicated 13674 MiB, shared 616 MiB, WS 14.35 GiB, private 16.67 GiB, **`ok`** |

| Task | Q3 verdict | Q3 out tok | Q2 verdict | Q2 out tok | Q2/Q3 |
|---|---|---:|---|---:|---:|
| `py_bugfix` | PASS | 722 | PASS | 640 | 0.9x |
| `py_impl` | PASS | 623 | PASS | 1520 | 2.4x |
| `go_bugfix` | PASS | 2402 | PASS | 5512 | 2.3x |
| `go_impl` | PASS | 1709 | **NO-ANSWER** | 8192 | >4.8x |
| `ts_impl` | PASS | 709 | PASS | 6681 | 9.4x |
| `ts_refactor` | PASS | 5687 | **NO-ANSWER** | 8192 | >1.4x |
| `cs_bugfix` | PASS | 5547 | PASS | 6304 | 1.1x |
| `unity_impl` | NO-ANSWER | 8192 | **NO-ANSWER** | 8192 | — |
| `constraint_frozen_file` | PASS | 1555 | PASS | 1244 | 0.8x |
| `no_invented_api` | PASS | 673 | PASS | 447 | 0.7x |

**`unity_impl` does not discriminate.** Both quantizations exhaust the output
budget on it, so it is a property of the model and the prompt rather than of the
weights. Excluding it, the score is **9/9 for Q3_K_XL against 7/9 for Q2_K_XL**.

Three findings, and the difference between them matters more than the counts.

**Neither quantization produced a single incorrect answer.** Across twenty
graded generations there is no case of code that compiled and behaved wrongly,
and no case of an invented API. Q2's entire deficit is generations that returned
nothing.

**Q2 is not reasoning badly; it is reasoning past the output budget.** Output
cost does not shift by a constant: on the trivial `py_bugfix` Q2 is *cheaper*
than Q3 (640 vs 722), and on `ts_impl` it is 9.4x more expensive for the same
correct answer.

**The mechanism is not established, and an early draft of this report asserted
one it could not support.** That draft read the Q3-to-Q2 token growth as lost
confidence driving longer exploration. The IQ4_XS control refutes it: on
`go_bugfix` the *highest*-precision quantization was the most expensive of the
three (IQ4_XS 6631, Q2 5512, Q3 2402). Reasoning length is noisy across
quantizations rather than monotonic in bit width, and three points were not
enough to claim a mechanism.

What survives is the narrower measured statement, which is the one that matters
operationally: **Q2 crossed the 8192-token output budget on two discriminating
tasks and Q3 crossed it on none.** For an agentic loop that is what counts —
a turn that returns nothing still consumes context and wall clock — but the
cause is recorded as unexplained rather than dressed up as a confidence effect.

**Everything that governs agentic behaviour survived 2-bit weights.** Tool
calling is 4/4 with exact names and exact arguments, including declining to call
`edit_file` on the frozen file and calling `list_dir` instead. Structured JSON is
valid. `constraint_frozen_file` and `no_invented_api` both pass, and both cost
Q2 *fewer* tokens than Q3. The failure is confined to implementation-heavy
generation.

That distinction has a practical consequence and it is deliberately not resolved
here: `--reasoning-budget` exists on this build and forces an end to thinking.
Whether it converts Q2's two discriminating no-answers into passes is a
**separate variable** and was not folded into this comparison. It is recorded
under "Not measured" as the one experiment that could still change Q2's verdict,
because Q2 is the only candidate that reaches 128k with essentially no paging.

### 7.3 What the memory difference buys

Serving peaks, from the same runs that produced the quality scores above:

| | Q3_K_XL | Q2_K_XL | Delta |
|---|---:|---:|---:|
| Dedicated VRAM | 15919 MiB | 13674 MiB | −2245 MiB |
| Shared (paging) | 900 MiB | 616 MiB | −284 MiB |
| Process WS | 16.35 GiB | 14.35 GiB | −2.00 GiB |
| Process private | 18.99 GiB | 16.67 GiB | −2.32 GiB |
| Pressure verdict | `elevated` | **`ok`** | — |

Note that both serving peaks are far above the load-time footprints in §5.2
(11.70 and 8.73 GiB working set respectively). Admission control gates on what a
model costs while answering, so these are the figures a manifest entry must
carry, and the load-time numbers would understate a profile by 3–5 GiB.

### 7.4 `UD-IQ4_XS` control at KV `q4_0/q4_0` — 13.27 GiB

The prior reports characterized IQ4_XS at KV `q8_0`. This cell re-runs it at
`q4_0/q4_0` so that Q3 and Q2 are compared against the same cache precision they
were measured at, rather than against a differently-configured baseline.

| Suite | Result |
|---|---|
| Coding | **9/10 pass**, 1 FAIL, 0 `no_answer` |
| Tool calling | **4/4 pass** |
| Structured JSON | **pass** |
| Session peak | dedicated 15914 MiB, shared 1768 MiB, WS 16.69 GiB, private 19.96 GiB, `pressured` |

### 7.5 Three-way comparison at 32k, KV `q4_0/q4_0`

| | IQ4_XS 13.27 GiB | Q3_K_XL 12.24 GiB | Q2_K_XL 9.15 GiB |
|---|---|---|---|
| Coding pass | 9/10 | **9/10** | 7/10 |
| Incorrect answers | **1** | **0** | **0** |
| No answer at the 8192 cap | **0** | 1 | 3 |
| Tool calling | 4/4 | 4/4 | 4/4 |
| Structured JSON | pass | pass | pass |
| Total output tokens, 10 tasks | 26329 | 27196 | 51420 |
| Dedicated peak | 15914 | 15919 | **13674** |
| Shared peak | 1768 | 900 | **616** |
| Process WS | 16.69 | 16.35 | **14.35** |
| Process private | 19.96 | 18.99 | **16.67** |
| Pressure verdict | `pressured` | `elevated` | **`ok`** |

**Q3_K_XL matches the IQ4_XS control.** Both score 9/10, and Q3 gets there
without producing a single wrong answer where IQ4 produced one that did not
compile. On this suite, moving from ~4-bit to ~3-bit weights with a `q4_0` cache
costs nothing measurable in coding quality and saves 868 MiB of shared memory at
the serving peak.

**Tool calling is flat across the whole range.** All three quantizations score
4/4 with exact function names, exact argument values and valid JSON arguments,
and all three decline to call `edit_file` on the frozen file. Nothing between
IQ4_XS and Q2_K_XL degrades tool use at 32k.

**Aggregate output cost separates Q2 cleanly, per-task cost does not.** Over ten
tasks, IQ4_XS and Q3_K_XL are within 3% of each other (26329 vs 27196 tokens)
while Q2_K_XL is roughly double (51420). Individual tasks swing in both
directions — IQ4_XS is the most expensive of the three on `go_bugfix` — so the
per-task figures are noise and only the aggregate is a signal.

### 7.6 `unity_impl`: one harness fault and one model fault

All three quantizations fail this task, by two different routes: IQ4_XS emitted
code that did not compile, while Q3 and Q2 reasoned past the output cap and
emitted nothing.

The IQ4_XS compile output carried two errors:

```
CS0103: the name "Mathf" does not exist in the current context
CS0136: a local named "instance" cannot be declared in this scope ...
```

`CS0103` was **this harness's defect**. `Mathf.Max` is ordinary UnityEngine
surface and the minimal shim did not stub it, so a model writing correct Unity
was scored as writing broken Unity. The shim now carries `Mathf` and the
verifier self-test still discriminates on the task.

`CS0136` is **the model's own error** — `GameObject instance` is declared inside
the `if` block and again in the enclosing scope of `Rent()`, which is fatal in
Unity exactly as it is here. The FAIL verdict for IQ4_XS therefore stands
independently of the shim gap, and Q3/Q2 were never affected because neither
produced code for the shim to compile.

Recorded rather than silently repaired, because the same class of defect — a
harness limitation scoring as a model failure — is what §6 describes, and it was
caught twice in this campaign.

## 8. Throughput at short context, KV `q4_0/q4_0`

`llama-bench`, 3 repetitions, 4-block split, `-ub 288`, `-t 8`, device isolated
to gfx1201. Occupancy is peak dedicated against the adapter's 16304 MiB, sampled
concurrently with the run.

| | IQ4_XS 13.27 GiB | Q3_K_XL 12.24 GiB | Q2_K_XL 9.15 GiB |
|---|---:|---:|---:|
| pp512 | 281.59 ± 0.61 | **819.34 ± 24.16** | 561.43 ± 1.51 |
| pp8192 | 258.80 ± 0.26 | **743.34 ± 0.67** | 521.37 ± 0.76 |
| pp32768 | 222.66 ± **10.62** | 207.95 ± 0.10 | **428.49 ± 0.41** |
| tg128 | 16.04 ± 0.16 | **23.12 ± 0.30** | 22.16 ± 0.02 |
| Occupancy | 97.2–97.4% | 94.2–96.1% | 78.5–81.5% |
| Shared during run | 932–1664 | 542–855 | 542–620 |

### 8.1 There is an occupancy cliff at about 96%, and it decides prefill

The three quantizations do not sit on one curve; they sit on either side of a
threshold.

| Model | pp512 occupancy | pp512 | pp32768 occupancy | pp32768 |
|---|---:|---:|---:|---:|
| Q3_K_XL | 94.2% | **819.34** | 96.1% | 207.95 |
| IQ4_XS | 97.2% | 281.59 | 97.4% | 222.66 |
| Q2_K_XL | 78.5% | 561.43 | 81.5% | **428.49** |

Below the threshold, prefill runs at kernel speed and the quantization with the
cheaper kernels wins — Q3_K_XL's K-quant-and-IQ4_XS mix beats Q2_K_XL's
IQ-heavy mix 819 to 561. Above it, prefill collapses irrespective of
quantization: IQ4_XS manages 281 at 97.2%, and Q3_K_XL falls to 207 once a 32k
prompt pushes it to 96.1%. Q2_K_XL never approaches the threshold and is
consequently the only candidate that still prefills at 428 t/s with a 32k
prompt — **2.06x Q3_K_XL and 1.92x IQ4_XS at the same prompt length.**

Two independent observations support reading this as occupancy rather than as a
property of the weights:

- **Variance.** At pp32768, IQ4_XS measures ± 10.62 t/s against Q3_K_XL's ± 0.10
  and Q2_K_XL's ± 0.41 — a hundredfold difference in run-to-run spread. That is
  the signature of a configuration oscillating across a paging boundary rather
  than one running slowly but steadily.
- **The prior report disagrees with this one about IQ4_XS, and the disagreement
  is the evidence.** `REPORT-workstation-memory` records pp512 at **956.82** for
  these weights with 882 MiB shared. This campaign measures **281.59** for the
  same GGUF and the same flags with 1152 MiB shared. Same artifact, 3.4x apart,
  on opposite sides of the threshold.

**The operational conclusion is about reproducibility, not speed.** IQ4_XS sits
on the boundary on this workstation, and the desktop's own VRAM was measured
moving between 3477 and 4146 MiB during this campaign. Its prefill performance
therefore depends on what else is on screen. Q3_K_XL carries roughly two
percentage points of margin and stays on the fast side through 8k; Q2_K_XL
carries about eighteen and never gets near it.

Labels: every throughput and memory figure above is **MEASURED**. Occupancy as
the *mechanism* is **INFERRED** — it is consistent across nine cells and with the
split sweep in `REPORT-qwen38-27b-gfx1201` §3.1, but no controlled experiment
isolating it was run.

### 8.2 Decode, and one confound that is not resolved

`Q3_K_XL decodes at 23.12 t/s against IQ4_XS's 16.04` at identical cache
precision — 44% faster, from weights that are 1.03 GiB smaller.

The IQ4_XS decode figure needs a caveat rather than a headline.
`REPORT-workstation-memory` records **23.66 t/s** for IQ4_XS at KV `q8_0` with
882 MiB shared; this campaign measures **16.04 t/s** at KV `q4_0` with 932 MiB
shared and 97.2% occupancy. Two variables changed between those runs — cache
precision and where the adapter landed relative to the cliff — so **this
campaign cannot say whether KV `q4_0` costs IQ4_XS decode throughput.** It is
recorded as an unresolved confound, not as evidence against `q4_0`.

What is unambiguous is the comparison at matched settings: at KV `q4_0/q4_0`,
4-block split and `ub` 288, **Q3_K_XL beats IQ4_XS on every throughput axis
measured, with one to two orders of magnitude less variance.**

Q2_K_XL decodes at 22.16 t/s — *slower* than Q3_K_XL despite being 3.09 GiB
smaller. Decode is bandwidth-bound, so a smaller weight set should decode
faster; that it does not is consistent with the dequantization cost of its
IQ-heavy tensor mix (112 IQ3_XXS, 67 IQ2_S, 48 IQ2_XXS, 34 IQ2_XS, 20 IQ1_S
against Q3_K_XL's 156 IQ4_XS, 111 IQ3_S, 36 Q4_K, 26 Q5_K). **INFERRED** from
the tensor census taken by the MTP probe; not isolated by experiment.

## 9. Decode with the context actually occupied — partial

Measured with `llama-bench -d`, which prefills the stated depth before timing
128 decode tokens. Only the first cell completed before the campaign was paused.

| Model | depth | tg128 | occupancy | shared |
|---|---:|---:|---:|---:|
| Q3_K_XL | 0 | 23.12 | 94.2% | 542 |
| Q3_K_XL | 32768 | **18.33 ± 0.12** | 95.7% | 810 |
| Q3_K_XL | 131072 | *not measured* | | |
| Q3_K_XL | 262144 | *not measured* | | |
| Q2_K_XL | 32768 / 131072 / 262144 | *not measured* | | |

**Q3_K_XL loses 21% of its decode rate to 32k of occupied context** — 23.12 to
18.33. This is why the campaign specifies decode at depth rather than `tg128` on
an empty window: a profile advertised at 23 t/s delivers 18.3 once an agent
carries 32k of standing context, which is an ordinary working state rather than
an edge case.

Against the classification thresholds in campaign §15 (≥23 t/s short, ≥18 t/s
near the limit), Q3_K_XL at 32k occupancy sits exactly on the boundary. The 128k
and 256k depths decide whether it is FAST, BALANCED or SLOW, and they are not
yet measured.

## 10. Preliminary summary table

Everything measured so far, at KV `q4_0/q4_0`, 4-block CPU split, `ub` 288,
`-t 8`, `--parallel 1`. `tg @32k` is decode with the context actually occupied.

| Weights | GiB | KV K/V | ctx | tg short | tg @32k | pp512 | pp8192 | pp32768 | dedicated | shared | WS | coding | tools | verdict |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|---|---|
| UD-IQ4_XS | 13.27 | q8_0/q8_0 | 32768 | 23.66¹ | — | 956.82¹ | 269.00¹ | 222.04¹ | 16053 | 2480 | 13.23 | — | — | prior baseline |
| UD-IQ4_XS | 13.27 | q4_0/q4_0 | 32768 | 16.04 | — | 281.59 | 258.80 | 222.66 | 15914 | 1768 | 16.69 | 9/10 | 4/4 | over the cliff |
| **UD-Q3_K_XL** | **12.24** | **q4_0/q4_0** | **32768** | **23.12** | **18.33** | **819.34** | **743.34** | 207.95 | 15919 | 900 | 16.35 | **9/10** | **4/4** | **best measured** |
| UD-Q2_K_XL | 9.15 | q4_0/q4_0 | 32768 | 22.16 | — | 561.43 | 521.37 | **428.49** | **13674** | **616** | **14.35** | 7/10 | **4/4** | long-ctx only |

¹ From `REPORT-workstation-memory-20260821.md` §4, measured at KV `q8_0`. Not
directly comparable — different cache precision *and* a different position
relative to the occupancy cliff (882 MiB shared against this campaign's 1152).

Load-time footprint across the context range, KV `q4_0/q4_0` (from §5.2):

| ctx | IQ4_XS shared | Q3_K_XL shared | Q2_K_XL shared |
|---:|---:|---:|---:|
| 32768 | 1875 | 1089 | **511** `ok` |
| 65536 | 2614 | 2027 | **529** `ok` |
| 131072 | 4094 | 3507 `ok` | **565** `ok` |
| 262144 | 7054 | **5946** | **2874** |

## 11. Preliminary answers

Answering the campaign's questions with the evidence in hand, and saying plainly
where there is none.

**A — smallest weight quantization that keeps coding quality acceptable.**
`UD-Q3_K_XL`. It matches the IQ4_XS control at 9/10 on the compiled coding suite,
scores 4/4 on tool calling and passes structured output, and does so without
producing a single incorrect answer where IQ4_XS produced one that did not
compile. `UD-Q2_K_XL` is below the line at 7/10 — not because it writes wrong
code (it wrote none that was wrong) but because three tasks returned nothing
within the shipped 8192-token output budget. **MEASURED at 32k.**

**B — does KV `q4_0/q4_0` degrade Qwen3.8?** No evidence that it does, at 32k.
All three quantizations score 4/4 on tool calling with valid JSON arguments and
pass structured output at `q4_0/q4_0`, and the IQ4_XS control scores 9/10 on
coding. **The long-context half of this question is unanswered** — retention
under a `q4_0` cache is exactly where a cache-precision effect would be expected
to appear, and that ramp has not run. **PARTIALLY MEASURED.**

**C — is `K q8_0 / V q4_0` a better sweet spot than `q4_0/q4_0`?** **NOT
MEASURED for quality.** The memory half is measured (§5.1): at 256k the hybrid
costs 8437 MiB shared against 7054 for `q4_0/q4_0`, so it buys nothing on this
adapter that `q4_0/q4_0` does not already buy more cheaply. No quality A/B was
run, because the campaign was narrowed to `q4_0` before that phase.

**D — maximum context that stays fast on this RX 9070 XT.** Not yet
answerable. Decode at 32k occupancy is measured (Q3_K_XL 18.33 t/s); 128k and
256k are not. What *is* established is that prefill degrades by occupancy rather
than by context as such, and that Q2_K_XL is the only candidate with enough
headroom to prefill a 32k prompt at over 400 t/s. **PARTIAL.**

**E — is 256k reachable without unacceptable RAM/VRAM?** Yes for Q3_K_XL, barely,
and comfortably for Q2_K_XL. Q3_K_XL loads a declared 262144-token window at
5946 MiB shared — 198 MiB inside the campaign's 6 GiB abort line — with a 15.83
GiB working set. Q2_K_XL does it at 2874 MiB shared and 9.86 GiB. IQ4_XS cannot:
7054 MiB shared at the cheapest cache setting. **MEASURED at load; serving
behaviour at that window is not measured.**

**F — is Q3 sufficient, or does Q2 offer a real advantage?** Q3 is sufficient
and Q2's advantage is narrower than its size suggests. Q2_K_XL loses on coding
(7/10 vs 9/10), loses on decode (22.16 vs 23.12), loses on short prefill (561 vs
819), and costs 2.1x the output tokens over ten tasks (51420 vs 27196). It wins
on exactly two axes: memory, decisively, and **prefill at 32k, by 2.06x** — the
second being a consequence of the first. **MEASURED at 32k.**

**G — is the model still reliable with the context near 256k?** **NOT MEASURED.**
This is the question the paused phases exist to answer.

**H — can Codex/OpenCode sustain much larger cumulative sessions through
compaction without losing critical constraints?** **NOT MEASURED**, and one
blocking defect was found by inspection — see §12.

## 12. Harness context contract — a defect found by inspection

`integrations/codex/cia-local.config.toml` and `cia-local-canary.config.toml`
both declare, as *profile-global* keys:

```
model_context_window = 131072
model_auto_compact_token_limit = 110000
```

while `integrations/codex/codex-model-catalog.json` carries correct per-model
windows — `qwen38-27b-ws-32k` at 32768, `qwen38-27b-ws-8k-prefill` at 8192,
`qwen35-9b-q4km` at 262144, each with `effective_context_window_percent: 85`.

If the global keys take precedence, selecting `qwen38-27b-ws-32k` produces
exactly the failure the campaign names: Codex believes it has 128k and grows to a
110k compaction threshold against a server that stops at 32768.

**The precedence itself is INFERRED and could not be tested here.** The Codex CLI
is not on PATH on this machine — Codex is the ChatGPT desktop application — so
there is no binary to dump a resolved configuration from. The defect is reported
as a configuration inconsistency *capable* of producing that failure, not as a
demonstrated one.

**OpenCode has no equivalent problem.** `opencode.canary-provider.jsonc` and
`opencode.local-provider.jsonc` declare `limit.context` and `limit.output` per
model, matching the manifest exactly for all eleven entries.

**Proposed fix, not applied.** Remove the two global keys from both TOMLs so the
catalog's per-model `context_window` and `effective_context_window_percent`
govern. Per campaign §39 nothing in `integrations/` was modified during
qualification; this is a recommendation for a separate change.

### 12.1 Output reserve is larger than a non-reasoning model needs

Measured across thirty generations, reasoning ran from 1404 to 35570 characters
before the answer began. `ts_refactor` needed 5687 output tokens at Q3_K_XL and
`cs_bugfix` 5547; both had previously been scored as failures under a 2560-token
cap they exceeded by more than double.

Consequently **Qwen3.8-27B needs an output reserve of roughly 6k tokens, and 8k
to be safe**, before it emits a line of code. OpenCode's `limit.output: 8192` on
the `qwen38-*` profiles is adequate but not generous. Any auto-compaction
threshold (campaign §25) must reserve 8k for output rather than the 1–2k a
non-reasoning model would need.

## 13. Failures and aborted experiments

- **First Q3_K_XL quality pass, discarded.** It scored two tasks as coding
  failures that were really the harness's 2560-token output cap truncating the
  model inside `reasoning_content`. Diagnosed by replaying the prompt and
  dumping the raw message envelope; corrected per §6; artifacts deleted rather
  than kept.
- **`unity_impl` at IQ4_XS, partially invalid.** The compile output included a
  missing `Mathf` in this harness's UnityEngine shim. The FAIL verdict survives
  on the independent `CS0136` error, and the shim now carries `Mathf`. See §7.6.
- **Phase B roll-up, recovered.** A per-test timeout added to
  `Invoke-V2ThroughputSweep.ps1` used `WaitForExit(int)`, which leaves
  `ExitCode` unpopulated on a `-PassThru` process object, so all twelve
  completed benchmarks were recorded as failures with a blank exit code. The raw
  `llama-bench` JSON was intact; the numbers were recovered from it rather than
  by re-running six hours of benchmarks, and `summarize_campaign.py` now reads
  the raw artifacts as a fallback so that recovery is the normal path.
- **`UD-IQ2_S` and `UD-IQ2_XXS`, eliminated before download.** No MTP head at any
  precision; see §3.
- **Campaign paused after phase B** at the operator's request, with phases C and
  D outstanding. `benchmarks/campaign-256k/resume-campaign.ps1` restarts them.

## 14. Not measured

Stated explicitly rather than omitted.

- **Decode at 128k and 256k occupancy** for either candidate — phase C, paused.
- **The long-context retention ramp** at 32k/64k/128k/192k/240k for either
  candidate — phase D, paused. This is the evidence for questions D and G.
- **KV `K q8_0 / V q4_0` quality.** Memory measured, quality not.
- **Q3 placement sweep.** The 4-block split was inherited from the IQ4_XS
  characterization rather than re-swept for a 12.24 GiB weight set. Given §8.1, a
  5- or 6-block split might move Q3_K_XL further below the occupancy cliff, and
  it is the single most promising untested lever in this campaign.
- **`UD-IQ3_S` and `UD-IQ3_XXS`.** MTP-qualified, never downloaded; Q3_K_XL met
  the envelope so the smaller Q3 variants were not needed.
- **Bounded reasoning.** `--reasoning-budget` exists on this build. Whether it
  converts Q2_K_XL's two discriminating no-answers into passes is the one
  experiment that could still change Q2's verdict.
- **Agentic endurance**, the 1M-token cumulative session, speculative decoding,
  the soak, and the edge/router path. None were reached.

## 15. Preliminary recommendation

Provisional, and explicitly subject to the retention ramp.

**For daily coding in Codex or Claude Code: `UD-Q3_K_XL` with KV `q4_0/q4_0` at
32k–128k.** It matches the IQ4_XS control on quality (9/10, 4/4 tools), decodes
at 23.12 t/s against IQ4_XS's 16.04 at the same cache precision, prefills 2.9x
faster below 32k, and — the part that matters most on this workstation — it is
*stable* where IQ4_XS is bimodal. At 128k it loads in the `ok` pressure state at
3507 MiB shared, which IQ4_XS cannot do at any cache precision.

**For a 256k profile: undecided, and deliberately so.** Q3_K_XL fits the window
with 198 MiB of headroom and Q2_K_XL fits it comfortably while prefilling 2.06x
faster at 32k, but neither has been tested for retention, and a window that loads
is not a window that works. That is precisely the failure the campaign was
written to avoid calling a success.

**Q2: not worth it on current evidence, with one caveat.** It loses on coding,
decode, short prefill and output cost, and wins only on memory and on long
prefill. Its perfect tool calling and constraint adherence make it a real
candidate for a memory-constrained long-context profile rather than a general
one — but §20's rule applies: it fits is not a reason to ship it. The caveat is
`--reasoning-budget`, untested, which targets exactly the failure mode Q2
exhibits.

**Nothing is promoted.** `qwen38-27b-ws-32k` remains the shipped default;
`provider.public_model` still resolves to `local-coding`. Every profile discussed
here would enter as `candidate` in `canary` per `docs/MODEL_PROMOTION.md`, and
the resource envelopes in §7.3 are the serving peaks such an entry would need to
carry — not the load-time figures in §5, which understate them by 3–5 GiB.
