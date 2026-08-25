# Final roster, 2026-08-25

One round, seven objectives: get CI green for a legitimate reason, reclaim the
SSD, fix Ornith's `developer` contract, decide the MoE, and find out what
context Gemma can actually hold.

Every number below comes from a file in this repository. Where a claim rests on
evidence gathered before this round, the report says so and says why that
evidence is still valid.

---

## CI

### What the twelve findings were

The `Secret scan` job failed with `leaks found: 12` and nothing else. That is a
gate that accuses the repository without naming anything in it: `--redact`
blanks the finding, and with no report there is no rule, no file, and no line
either.

Reproduced locally with the pinned `gitleaks v8.30.1`, all twelve were **one
rule and one string**:

| | |
|---|---|
| Rule | `generic-api-key`, 12/12 |
| Secret | `ReflectionScanV2`, 12/12 |
| Files | 5, all in `benchmarks/campaign-ornith15-35b-a3b/qualification/capture-iq2m-retention-ctx262144-ncpumoe6-retry/` |
| Commit | `f3d8f0a`, 12/12 |

`ReflectionScanV2` is defined at
[fixture_corpus.py:43](../../scripts/v2/eval/fixture_corpus.py:43). The retention
suite manufactures credential-shaped needles so a long-context probe has an
answer the model cannot guess, and the `negative_memory` family asks the model
*not* to name that particular one. Every hit is a capture of the model doing
exactly that.

**Classification: 12/12 TEST_FIXTURE / INERT. No real secret, no rotation
needed, no history rewrite needed.**

### What was changed

Two things, and neither of them is "make the scanner quieter".

**The job now says what it found.** `gitleaks` writes a JSON report to
`$RUNNER_TEMP`, the exit code is captured before anything is printed and
re-raised after, and the summary prints `RuleID / File / StartLine / Commit`
plus a per-rule count. `.Secret` and `.Match` stay in the report file and never
reach a public build log. The step is not `continue-on-error`; a scan that finds
something still fails.

**The allowlist is scoped to a value, not to a path.** The generator was already
allowlisted; the graded evidence quoting it back was not. The new entry in
`.gitleaks.toml` joins three criteria with `condition = "AND"`:

```toml
targetRules = ["generic-api-key"]
regexTarget = "secret"
regexes = ['''^ReflectionScanV2$''', '''^H7Q9-42AX-731$''', ... ]
```

Four exact literal strings. No path, no file type, no directory —
`benchmarks/**` stays fully scanned.

**Verified by negative control.** An `api_token`-shaped value was planted in the
very directory the twelve findings came from, and the scan still caught it. The
allowlist cannot mask a credential that happens to sit next to a fixture.

### One more finding, caught and fixed before it was published

A later scan surfaced a thirteenth: `generic-api-key` on
`Run-FinalMoECampaign.ps1:100` — a PowerShell hashtable field literally named
`key`, assigned a model nickname. A **FALSE_POSITIVE**, and an avoidable one.

(Deliberately paraphrased rather than quoted. Writing that assignment out
verbatim in this report reproduced the finding *in the report*, on the first
scan after this section was drafted — which is a small, funny demonstration that
the rule does what it says.)

It was not allowlisted. The field was renamed to `id`, and because the commit
that introduced it was local and unpublished, the two affected commits were
rewritten before any push. Nothing was force-pushed over anything anyone had:
the branch had no upstream. Spending an allowlist entry on a naming mistake
would have made the gate permanently weaker in exchange for saving a rename.

### Status

| scan | result |
|---|---|
| `gitleaks git` (full history, 46 commits, 29.3 MB) | **no leaks found**, exit 0 |
| `gitleaks dir` (working tree, 131.5 MB) | **no leaks found**, exit 0 |
| negative control (planted secret) | **caught**, exit 1 |

The rest of the CI-equivalent battery was run locally and passes: `gofmt`,
`go vet`, `go test ./...`, `staticcheck ./...`, manifest schema validation (39
semantic policy tests), harness config, config generation (36 request-budget and
27 runner-wiring tests), artifact store, release transaction, telemetry, agentic
harness, the tracked-binary gate, the PowerShell parse gate, and the four
Python self-tests including the new chat-template contract.

---

## Disk cleanup

| Model | File | Size | Action | Reason |
|---|---|---:|---|---|
| Qwen3.6 35B-A3B | `Qwen3.6-35B-A3B-UD-Q3_K_S.gguf` | 15.36 GB | **deleted** | Intermediate rung; lost its own ladder, superseded by Q2_K_XL |
| Qwen3.6 35B-A3B | `Qwen3.6-35B-A3B-UD-IQ4_XS.gguf` | 17.73 GB | **deleted** | Intermediate rung; no admissible 256k placement worth its bytes |
| Ornith 1.5 35B-A3B | `Ornith-1.5-35B-A3B-IQ3_XXS.gguf` | 15.34 GB | **deleted** | Intermediate rung; IQ2_M was the finalist |
| Ornith 1.5 35B-A3B | `Ornith-1.5-35B-A3B-Q3_K_XL.gguf` | 17.80 GB | **deleted** | Intermediate rung; heaviest and slowest of the three |
| Qwen3.8 27B | `Qwen3.8-27B-UD-Q2_K_XL.gguf` | 9.83 GB | **deleted** | Artifact of `qwen38-27b-huge-256k`; profile retired first (see below) |
| Qwen3.8 27B | `Qwen3.8-27B-UD-Q4_K_M.gguf` | 16.46 GB | **deleted** | No manifest entry at all; referenced only by a 2026-08-21 report |
| Gemma 4 12B QAT | `gemma-4-12B-it-qat-UD-Q4_K_XL.gguf` | 6.72 GB | **kept** | Public model; now also the artifact of the 256k candidate |
| Qwen3.8 27B | `Qwen3.8-27B-UD-IQ4_XS.gguf` | 14.25 GB | **kept** | `qwen38-27b-deep-32k`; hardest localized tasks |
| Qwen3.8 27B | `Qwen3.8-27B-UD-Q3_K_XL.gguf` | 13.15 GB | **kept** | `qwen38-27b-agent-128k`; daily agentic default |
| Qwen3.6 35B-A3B | `Qwen3.6-35B-A3B-UD-Q2_K_XL.gguf` | 12.29 GB | **kept** | Wins the long-context slot as `qwen36-35b-a3b-huge-256k` |
| Ornith 1.5 35B-A3B | `Ornith-1.5-35B-A3B-IQ2_M.gguf` | 12.54 GB | **kept** | Loses long-context, takes the latency slot as `ornith15-35b-a3b-fast-128k` |

`Qwen3.6-35B-A3B-UD-Q3_K_XL` was on the delete list but was **not present** — it
had been removed in an earlier round. Its SHA-256 is still on record in the
campaign download log.

**Space**

| | bytes | GB (decimal) | GiB |
|---|---:|---:|---:|
| Free before | 34,209,873,920 | 34.21 | 31.86 |
| Free after | 126,734,802,944 | 126.73 | 118.03 |
| **Recovered** | **92,524,929,024** | **92.52** | **86.17** |

The sum of the six file sizes is 92,525,464,640 bytes; the measured free-space
delta is 535,616 bytes smaller, which is ordinary filesystem churn during the
run. Both figures are recorded rather than one being presented as the other.

**Method.** No wildcards, no directory deletions. Each file was cleared
individually against the KEEP list *and* against every non-retired artifact
entry in `config/models.yaml` before being removed by exact path. The check
initially refused `Qwen3.8-27B-UD-Q2_K_XL` because a profile still named it —
which is the guard working. `qwen38-27b-huge-256k` was moved to `state:
retired` with no deployment first, so what it lost was the claim to be servable,
not its measurements or its configuration. A retired profile naming a deleted
file is the documented outcome; an active one would not be.

Every deleted artifact is re-downloadable from a pinned revision, and its
SHA-256 is on record in the campaign download logs. The evidence — reports,
metrics, captures, footprints — was not touched.

---

## Qwen3.6 Q2_K_XL vs Ornith 1.5 IQ2_M

Four phases, one pair of artifacts, on the same card, the same runtime, the same
KV type, the same harness, the same prompts, the same grader, the same seeds.
Placement is each model's own smallest admissible `--n-cpu-moe` from its own
census, because forcing a shared number would measure the offload rather than
the model.

### A. Contract gate — the hard gate

| | Qwen3.6 Q2_K_XL | Ornith IQ2_M |
|---|---|---|
| graded probes | **9/9 PASS** | **9/9 PASS** |
| informational probes | 3 recorded | 3 recorded |
| `developer_role` | 200, instruction honoured | 200, instruction honoured |
| `developer_after_content` | 200 | 200 |
| verdict | **PASS** | **PASS** |

Ornith passes **only with** `config/chat-templates/ornith15-35b-a3b.jinja`. A
control run on the same server, same probes, with the GGUF's embedded template
instead, is checked in beside the other two and still returns **HTTP 500** on
`developer_role` — the same Jinja exception as 2026-08-24. Both halves are
evidence: without the control, 12/12 would show only that the gate passes, not
that the template is why.

**Gate result: both models pass.** This no longer separates them.

### B. Quality

The first pass measured both at the harness default output ceiling. Qwen3.6
scored 27/34 — and three of its five failures were not wrong answers but
**`NO_ANSWER` / `REASONING_EXHAUSTED`**: 8,192 output tokens consumed with
30,010 and 32,632 characters still sitting in `reasoning_content`, and nothing
returned. That is the exact failure `qwen38-27b-huge-256k` already solves with
`reasoning_budget`. Scoring an untuned model against a tuned one and calling the
difference quality would have been a measurement error, so both were re-run
symmetrically at `n_predict 16384` with `reasoning_budget 6144`.

| suite | Qwen3.6 Q2_K_XL | Ornith IQ2_M | winner |
|---|:---:|:---:|---|
| chat | 4/4 | 4/4 | tie |
| coding | 10/10 | 10/10 | tie |
| hard | 6/8 | 6/8 | tie |
| tools | 6/7 | 6/7 | tie |
| literal_tools | **4/4** | 2/4 | **Qwen3.6** |
| json (structured) | 1/1 | 1/1 | tie |
| **total** | **31/34** | **29/34** | **Qwen3.6** |

The budget moved Qwen3.6 from 27/34 to 31/34 and left Ornith unchanged at 29/34
— Ornith never approached the cap; its longest reasoning in the whole suite was
12,775 characters against Qwen3.6's 32,632. It is not that the budget helped one
model and not the other; it is that only one model needed it. That the budget
was the cause and not the higher ceiling is visible in the numbers: at **double**
the ceiling Qwen3.6's total reasoning went **down**, 245,934 characters to
219,821, which only a budget that cuts thinking short can do.

Both models are deterministic here. Three seeds each at the first setting
produced **identical** scores case by case, so the spread being reported is real
and not a sample.

**What actually separates them, case by case:**

- **Ornith drops tool arguments.** Both `literal_tools` failures are one defect:
  it omits the `exclude` filter from the tool call while its own reasoning names
  it — *"excluding `Library/**`"*, then a call with no `exclude`. Eight
  occurrences out of eight: two cases across four runs (three seeds plus the
  budgeted diagnostic), every one of them `name_ok: true`, `json_ok: true`,
  `args_ok: false`. The tool name is right and the JSON is well formed; only the
  argument is missing. That is systematic, not stochastic. For an agentic model
  it is the worst shape of failure: a search that silently includes `vendor/**`
  returns wrong results that look right. It is Ornith's own behaviour and not an
  artifact of the template patch — the 2026-08-24 run, with the embedded
  template, failed the same two cases the same way.
- **`hard_missing_info` is a grader artifact, not a difference.** Ornith is
  scored as failing it; its answer is correct and arguably more specific than
  Qwen3.6's. Running `verify_missing_info` directly against both real answers:

  | | names `archive.json` | invents a checksum | contains "read" | verdict |
  |---|:---:|:---:|:---:|---|
  | Qwen3.6 | yes | no | **yes** | PASS |
  | Ornith | yes | no | **no** | FAIL |

  Both refuse to guess and both name the source that must be read, which is what
  the task's own system prompt asks for. The only difference the verifier can
  see is the literal word `read`; Ornith wrote "provide" and "supply". Counted as
  a tie here, and filed as a separate defect rather than quietly corrected
  mid-comparison — changing a grader retroactively changes what every earlier
  campaign's report would say.

  *(That fix has since landed, in the commit after this report: the keyword
  match is replaced by the two properties that actually matter — the answer
  names `archive.json`, and it invents no checksum — and both real answers above
  are pinned as regressions so the defect cannot come back. It does not move
  this verdict. Crediting Ornith the point takes it to 30/34 against Qwen3.6's
  31/34. The scores in this report are the ones the grader produced **on the day
  of the campaign**, deliberately left as measured rather than retroactively
  regraded.)*
- **Both fail `hard_go_retry_multifile` and `tool_pick_search_many_nested`.**
  Suite gaps, not discriminators.

Even crediting Ornith the grader artifact, it reaches 30/34 against 31/34.

### C. Retention

Not re-measured, and deliberately so. Both models already have a full ramp from
this harness, on this hardware, at both windows.

The reason that evidence survives the template change is not an inference. Every
retention request the harness sends carries the roles `["system", "user"]` —
`qualify.py` contains no `developer` role at all — and rendering the five
captured retention exchanges through **both** templates produces byte-identical
prompts, 5 of 5. The patch cannot have moved a retention number because it
cannot have changed a retention prompt.

| occupancy | Qwen3.6 Q2_K_XL | Ornith IQ2_M |
|---:|:---:|:---:|
| 120,000 (ctx 131072) | 12/12 | 12/12 |
| 240,000 (ctx 262144) | 12/12 | 12/12 |
| all ten levels, both windows | **120/120** | **120/120** |

Every level parsed, none truncated, on both models. **An exact tie**, across the
needle, constraint, mutable-state, negative-memory and entity families.

### D. Performance with the context filled

Three repetitions per cell. Relative standard deviation 0.07–0.55%, so these are
reproducible figures rather than single readings.

| measurement | Qwen3.6 Q2_K_XL | Ornith IQ2_M | winner |
|---|---:|---:|---|
| prefill `pp:8192` @ ctx 131072 | 1243.90 t/s | **1450.67 t/s** | Ornith **+16.6%** |
| decode `tg:128` @ 120,000 | 50.40 t/s | **54.65 t/s** | Ornith **+8.4%** |
| prefill `pp:8192` @ ctx 262144 | 855.37 t/s | **971.43 t/s** | Ornith **+13.6%** |
| decode `tg:128` @ 240,000 | 32.92 t/s | **34.64 t/s** | Ornith **+5.2%** |
| peak dedicated VRAM @ 120k decode | **11,925.5 MiB** | 12,373.4 MiB | Qwen3.6 by 448 MiB |
| peak dedicated VRAM @ 240k decode | **11,922.0 MiB** | 12,283.0 MiB | Qwen3.6 by 361 MiB |
| peak shared VRAM | 728.9 MiB | 727.9 MiB | tie |
| system RAM at peak | 22.14 GiB | 22.18 GiB | tie |
| stability | no failure, no timeout | no failure, no timeout | tie |

**And the figure the throughput numbers hide.** Completing the same 34-case
suite took Qwen3.6 **757.0 s** and Ornith **218.6 s** — Ornith is **3.5× faster
end to end**, because it answers with 17,999 output tokens where Qwen3.6 spends
60,659. Per token Ornith is 5–17% quicker; per *task* it is more than three
times quicker. The reasoning budget did not close this: it improved Qwen3.6's
score without materially changing its time (787 s → 757 s).

### Decision

Applying the criteria as they were set for this round:

| criterion | result |
|---|---|
| developer role fixed, 100% on the contract gate | **met** — 12/12, control confirms the fix is why |
| no new template/API problem | **met**, but Ornith now depends on a repo-maintained template override |
| retention at least equivalent | **met** — exact tie, 120/120 each |
| quality at least equivalent | **NOT met** — 29/34 against 31/34 under identical configuration |
| performance/VRAM equal or better, reproducibly | **partly** — faster on every throughput cell, but 361–448 MiB more VRAM at both windows |

Quality was set as the priority, with performance deciding only when quality is
equivalent. It is not equivalent, so speed does not get to decide.

# KEEP QWEN3.6

**`C:\IA\models\Qwen3.6-35B-A3B-GGUF\Qwen3.6-35B-A3B-UD-Q2_K_XL.gguf` stays.**

**`C:\IA\models\Ornith-1.5-35B-A3B-GGUF\Ornith-1.5-35B-A3B-IQ2_M.gguf` loses the
long-context slot.** Whether it is deleted is a separate question, answered
below.

Worth saying plainly, because it is the one number that argues the other way:
**Ornith finishes the same work 3.5× faster.** Under the rule set for this
round, Qwen3.6 wins on quality, wins on VRAM, ties on retention and contract,
and needs no maintained template override to do it.

### Ornith is kept, in a different slot

The operator's call after reading the above: keep the artifact for a
**low-latency coding agent** beside the Qwen3.8 Agent profile. The speed number
that lost the long-context argument is the whole point of a latency role, so
that is a coherent use of the same evidence rather than a reversal of it.

`ornith15-35b-a3b-fast-128k` is added as a candidate on canary — 131072, KV
`q4_0`, `--n-cpu-moe 2`, and the `chat_template_file` without which it cannot be
given a `developer` message. Smoke-tested from the manifest after the field was
added: **9/9 graded contract probes, developer instruction honoured**, evidence
in `contract/contract-ornith15-fast-128k-PROFILE-smoke.json`.

**The `exclude` defect follows it into the new slot and is documented there.**
It is why this is a latency profile and not a tool-loop profile: the failure is
a silently-wrong search, not a visibly-wrong one. `README.md` and
`docs/RUNBOOK.md` both carry the warning next to the row.

That makes the roster five profiles over six artifacts rather than four over
five, and the disk stays at 58.95 GB rather than 46.41 GB. Deliberate, measured,
and the operator's decision to make.

The chat-template fix stays in the repository regardless. It is correct, it is
tested, and it cost nothing to keep; deleting the weights does not make the
lesson — that bartowski ships the upstream template and unsloth ships a patched
one — worth forgetting.

### The winner, qualified at the window it is declared with

The head-to-head ran at 131072, where both models had comparable placements and
where the discriminating cases lived. But a profile should not claim a window
its quality evidence never touched, so the winner was run again at 262144 with
`--n-cpu-moe 8` — the placement the census found admissible there.

| | at 131072 | at 262144 (declared) |
|---|:---:|:---:|
| chat | 4/4 | 4/4 |
| coding | 10/10 | 9/10 |
| hard reasoning | 6/8 | 6/8 |
| tools | 6/7 | 6/7 |
| literal tool contract | 4/4 | 3/4 |
| structured JSON | 1/1 | 1/1 |
| **total** | **31/34** | **29/34** |
| truncated / no-answer | 0 | **0** |

**Doubling the window costs about two cases** — `unity_impl` and
`literal_regex`. Nothing truncated, nothing ran out of budget, and the profile
holds 240,000 tokens with 12/12 retention. That is the honest price of the
long-context slot, and it is recorded on the profile rather than assumed away.

The envelope written into the manifest is the **worst** of the two cells
measured at this exact context and placement, per the promotion rules:

| | retention cell | quality cell | recorded |
|---|---:|---:|---:|
| `peak_vram_gib` | 11.75 | 12.49 | **12.49** |
| `peak_commit_gib` | 16.71 | 20.61 | **20.61** |
| `peak_ram_gib` | 15.39 | 16.20 | **16.20** |

Headroom at the declared window: 16,304 − 12,792.9 = **3,511 MiB**, against a
policy floor of 1,536. `gpu_pressure: ok`, load 8.8 s, no failure.

---

## Gemma 4 12B at 256k

The question was never whether `llama-server` can start with `--ctx-size
262144` — it can start with a great many numbers. It is whether the profile
still remembers, still answers, and still fits with the context actually
occupied.

Same GGUF, same path, same SHA-256 as the public 128k profile: no second copy on
disk. The candidate differs from the public profile in exactly one field,
`context_tokens`, so what is measured is context and not a bundle of changes.
Measured on the runtime the profile declares — `unsloth-runtime-candidate`, the
same one the 2026-08-23 baseline ran on — because putting 256k on a different
build would compare two windows across two runtimes and then blame the window.

### Does it start, and what does the window cost

| | 128k (2026-08-23 baseline) | 256k (this round) | delta |
|---|---:|---:|---:|
| marginal VRAM | 7,988.8 MiB | 9,899.4 MiB | **+1,910.6 MiB** |
| commit delta | 13.49 GiB | 14.91 GiB | +1.42 GiB |
| load time | 7.5 s | 4.8 s | — |
| GPU pressure | ok | ok | — |
| failure | none | none | — |

Admission at 262144 clears comfortably: peak dedicated **8,940.8 MiB** of
16,304, **7,363.2 MiB of headroom** against a policy floor of 1,536, shared-memory
delta 354.3 MiB against a 400 MiB ceiling, `gpu_pressure: ok`. Doubling the
window costs about 1.9 GiB and leaves roughly 6.4 GiB unused.

The KV cache for the whole window is allocated at load, so that cost is paid
once at startup and does not grow with use — and a short prompt is not slower
for having a large window declared.

### Retention up the ramp

| occupancy | prompt tokens | score | parsed | truncated | TTFT | prefill | decode |
|---:|---:|:---:|:---:|:---:|---:|---:|---:|
| 32,768 | 32,616 | 12/12 | yes | no | 22.9 s | 1,428.0 t/s | 55.52 t/s |
| 65,536 | 65,419 | 12/12 | yes | no | 69.3 s | 943.5 t/s | 48.63 t/s |
| 120,000 | 119,924 | 12/12 | yes | no | 197.5 s | 607.3 t/s | 41.44 t/s |
| 196,608 | 196,472 | **11/12** | yes | no | 497.1 s | 395.2 t/s | 29.80 t/s |
| 240,000 | 239,849 | 12/12 | yes | no | 763.7 s | 314.1 t/s | 27.61 t/s |
| | | **59/60** | | | | | |

By family, across the whole ramp:

| family | score |
|---|:---:|
| needle | 25/25 |
| entities | 15/15 |
| negative_memory | 10/10 |
| constraint | 5/5 |
| mutable_state | **4/5** |

**The single miss is worth naming precisely.** At 196,608 tokens the
`add_item_call` probe was graded `stale`: the model returned
`InventoryManager.AddItem`, which is the *superseded* value, rather than the one
that replaced it later in the context. That is the classic long-context failure
— an earlier definition winning over a later update — and it is the one family
built to catch it.

Two things keep it from reading as a ceiling. The deeper level, 240,000, scored
**12/12 including its own mutable-state probe**, so this is not monotonic
decay. And every level parsed cleanly, none truncated, every `finish_reason` was
`stop` — so no answer was lost to generation running out, which is the failure
the 2026-08-23 baseline hit at 32,768 and the reason this campaign passes
`NPredict 8192` explicitly.

**A parse failure is not a memory failure, and neither happened here.** The
one miss is a genuine recall error, recorded as such.

### What the window actually costs to fill

The number that decides how 256k should be described to a user is not VRAM, it
is **time to first token**.

| occupancy | TTFT | prefill rate |
|---:|---:|---:|
| 32,768 | 22.9 s | 1,428 t/s |
| 120,000 | 197.5 s | 607 t/s |
| 196,608 | 497.1 s | 395 t/s |
| 240,000 | **763.7 s** | 314 t/s |

Prefill throughput falls by 4.5× between an empty window and a full one, so
filling 240,000 tokens costs **12.7 minutes before the first token appears**.
That is a property of the window rather than a defect — nothing failed, nothing
timed out — but it is the fact that decides whether 256k is a contract to
advertise or a ceiling to have available.

For contrast, the MoE that just won the long-context slot reaches 240,000 in
655.2 s at 366.1 t/s. A 12B dense model is *slower* to fill a 240k window than a
35B sparse one, which is exactly why the huge-context job went to a MoE.

### Quality at 256k, against the 128k baseline

The same six suites the 2026-08-23 campaign ran against the public 128k profile,
so the two are directly comparable.

| suite | 128k baseline | 256k candidate |
|---|:---:|:---:|
| chat | 4/4 | 4/4 |
| coding | 6/10 | 6/10 |
| hard reasoning | 4/8 | 4/8 |
| tools | 6/7 | **7/7** |
| literal tool contract | 2/4 | **4/4** |
| structured JSON | 1/1 | 1/1 |
| **total** | **23/34** | **26/34** |

**Quality does not degrade at 256k — it improves.** The two suites that moved,
`tools` and `literal_tools`, both moved up. Nothing regressed.

Running the tools suite does **not** license flipping
`capabilities.function_calling`. That flag means a forced tool call demonstrated
end to end through `internal/edge/namespace.go`, which has not been done. It
stays `false`, exactly as it stayed after the 2026-08-23 campaign scored 6/7 on
the same suite.

### The eight failures are seven of one thing

| failure | finish_reason | output tokens | reasoning chars | answer |
|---|---|---:|---:|---|
| `go_bugfix` | length | 8,192 | 29,443 | none |
| `ts_refactor` | length | 8,192 | 27,585 | none |
| `cs_bugfix` (coding) | length | 8,192 | 28,294 | none |
| `unity_impl` | length | 8,192 | 25,159 | none |
| `hard_go_router_multifile` | length | 8,192 | 28,119 | none |
| `hard_go_retry_multifile` | length | 8,192 | 29,818 | none |
| `cs_bugfix` (hard) | length | 8,192 | 27,102 | none |
| `hard_missing_info` | stop | — | — | answered |

Seven of the eight are not wrong answers. They are the model spending its entire
8,192-token output budget inside `reasoning_content` and returning nothing —
the identical failure Qwen3.6 showed earlier in this report, and the identical
failure a `reasoning_budget` fixed there, taking it from 27/34 to 31/34.

`gemma4-12b-qat-ud-q4xl` declares no reasoning budget. And these seven failures
are **present in the 2026-08-23 baseline too**, case for case, so this is a
property of the profile and not of the larger window.

### So the same diagnostic was run on it

Diagnosing one model's thinking budget and not the other's would have made the
two verdicts incomparable. Same knobs, same seed, same window: `n_predict
16384`, `reasoning_budget 6144`.

| suite | no budget | `reasoning_budget 6144` |
|---|:---:|:---:|
| chat | 4/4 | 4/4 |
| coding | 6/10 | **10/10** |
| hard reasoning | 4/8 | **5/8** |
| tools | 7/7 | 7/7 |
| literal tool contract | 4/4 | 4/4 |
| structured JSON | 1/1 | 1/1 |
| **total** | **26/34** | **31/34** |
| no-answer cases | **7** | **0** |

**All seven disappear.** Coding goes from 6/10 to a clean 10/10. The three that
still fail are `hard_go_router_multifile` and `hard_go_retry_multifile` — the
two nobody in this round passes — and `hard_missing_info`, which is the grader
artifact described earlier.

31/34 is the same score the winning MoE reaches at 131072, and better than the
MoE manages at 262144.

**The public model has been leaving five of thirty-four cases on the table for
want of one field.** That is the largest single finding of this round outside
the MoE decision, and it was reachable only because the same question was asked
of both models.

`reasoning_budget: 6144` under `n_predict: 16384` is written onto
`gemma4-12b-qat-ud-q4xl-256k`, the candidate this campaign qualified.
`max_output_tokens` stays at 8192 — the answer contract does not change, only
the room the model is given to think inside. **The public 128k profile was not
touched.** The same field would very likely do the same thing there, and that is
a recommendation, not a change made on its behalf.

### Verdict

| PASS criterion | result |
|---|---|
| starts consistently | **yes** — three loads, 4.8–6.8 s, no failure |
| no OOM or instability | **yes** — `gpu_pressure: ok` throughout, no timeout |
| retention holds to ~240k | **yes** — 59/60; the one miss is at 196k and 240k is perfect |
| answers are usable | **yes** — 31/34 with a reasoning budget, 0 no-answers |
| generation still operational | **yes** — nothing truncated on any retention level |
| resources within the machine | **yes** — 9,899 MiB marginal, 6,405 MiB headroom |
| reproducible | **partly** — one ramp, n=1. The quality suite was run twice, and both runs agree on every case that did not involve the budget. |

# PROMOTE 256K

Gemma 4 12B QAT holds 262144 on this machine. It is not a stretch: 54.8% VRAM
occupancy, 6.4 GiB unused, retention 59/60 to 240,000 tokens, and quality at the
larger window is **better** than at 131072, not worse.

Two things belong in the contract alongside it:

1. **Say what filling it costs.** 240,000 tokens is 12.7 minutes to first token.
   The window is real; it is not fast to fill. A user who does not know that
   will think the server has hung.
2. **The budget is not optional.** Without `reasoning_budget`, this profile
   returns an empty answer on seven of thirty-four qualification cases. A 256k
   window on a profile that cannot finish a coding task is the wrong trade.

`provider.public_model` still resolves to the 128k profile. Whether the 256k
candidate takes over, and whether the 128k profile is then retired, is a
promotion decision — and promotion is a decision, not a side effect of a
measurement.

---

## Final roster

Four jobs, four artifacts, 58.95 GB on disk.

| # | Job | Profile | Artifact | Context | State |
|---|---|---|---|---:|---|
| 1 | general / public | `gemma4-12b-qat-ud-q4xl` | Gemma 4 12B QAT UD-Q4_K_XL, 6.72 GB | 131072 | candidate, canary, **`provider.public_model`** |
| 1b | general, larger window | `gemma4-12b-qat-ud-q4xl-256k` | *the same file* | 262144 | candidate, canary |
| 2 | dense, hardest localized | `qwen38-27b-deep-32k` | Qwen3.8 27B UD-IQ4_XS, 14.25 GB | 32768 | candidate, canary |
| 3 | dense agentic default | `qwen38-27b-agent-128k` | Qwen3.8 27B UD-Q3_K_XL, 13.15 GB | 131072 | candidate, canary |
| 4 | low-latency MoE | `ornith15-35b-a3b-fast-128k` | Ornith 1.5 35B-A3B IQ2_M, 12.54 GB | 131072 | candidate, canary, **needs `chat_template_file`** |
| 5 | long-context MoE | `qwen36-35b-a3b-huge-256k` | Qwen3.6 35B-A3B UD-Q2_K_XL, 12.29 GB | 262144 | candidate, canary |

Six GGUFs for five artifacts on disk — the two Gemma profiles share one file:
same path, same bytes, same SHA-256, no duplication.

The original target for this round was four jobs and one MoE. It ends at five
jobs and two, because the head-to-head produced a number — Ornith finishing the
same work 3.5× faster — that describes a role the roster did not have. Keeping
the artifact for that role is using the evidence, not overriding it.

**Retired, not deleted:** `qwen38-27b-huge-256k` and the five
`qwen38-27b-ws-*` entries keep their ids, their measurements and their
configuration; they carry no deployment and the Q2_K_XL they pointed at is gone.

**`provider.public_model` was not changed.** It still resolves to
`gemma4-12b-qat-ud-q4xl`. Promotion is a decision, not a side effect of a
measurement.

### What is left to do by hand

Nothing on disk. Every artifact that stays is referenced by a profile, and every
profile that references an artifact can be served.

The one deletion this report originally proposed — Ornith's IQ2_M — is
**withdrawn**: it lost the long-context slot and took the latency slot instead.
The volume sits at roughly 129 GB free with 58.95 GB of weights on it.

Two things are recommendations rather than changes, and both are promotion
decisions:

1. **`reasoning_budget` on the public Gemma profile.** Measured worth 5 of 34
   cases and 7 empty answers. Written onto the 256k candidate; the public 128k
   profile is untouched.
2. **Whether `gemma4-12b-qat-ud-q4xl-256k` takes over as `public_model`,** and
   whether the 128k profile is then retired. The measurement says it can; the
   measurement does not get to decide.
