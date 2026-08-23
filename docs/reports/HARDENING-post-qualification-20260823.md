# Post-qualification hardening — 2026-08-23

What changed in the harness after re-reading the raw evidence from the 2026-08-23 full
qualification campaign, what is staged and still unproven, and the exact commands for the physical
run that has to happen before any of it is called qualified.

**No model was requalified in this pass.** No GPU battery was run, no retention prefill, no
throughput sweep, no soak, no download. Everything below is either a deterministic code change
covered by fast tests, or a documented plan.

The findings that motivated each change are recorded as an errata section appended to
[`QUALIFICATION-CAMPAIGN-20260823.md`](QUALIFICATION-CAMPAIGN-20260823.md). The raw evidence in
`benchmarks/campaign-20260823-full/` is untouched.

### Amended, same day — second hardening pass

This document was revised after a re-read found four defects in its own first version. No model was
run, no result changed, no grader moved, and `config/models.yaml` is still untouched.

| | Was | Now |
|---|---|---|
| Budget arithmetic | an explicit `-MaxTokens` outranked `n_predict` outright, so a 32,768-token request on an 8,192-token profile was checked as if 32,768 tokens were available | every rule uses `effective_generation_ceiling = min(request_max_tokens, n_predict)`; §1 |
| Baseline classification | an explicit ceiling unlike the profile's contract still reported `policy_profile: baseline` | `budget_profile = deployment / constrained / expanded`, and anything but `deployment` is `diagnostic`; §2 |
| Gemma plan | step 4 A/B-tested the candidate at 16384/8192 and step 5 then ran the *old* Gemma as the full qualification | step 5 is split into a control cell and a candidate cell carrying 16384/8192 in every suite; §8 step 5 |
| Retention floor | a prefill leaving less than 4,096 tokens still produced a 4,096-token request, which the server had to reject | `INSUFFICIENT_CONTEXT_RESERVE`: no request is sent and the row says why; §1 |

Two documentation claims were wrong and are corrected in place: 8,192 was described as "the
smallest default any suite uses" (it is the minimum *answer reserve*; four of the six scored suites
default to 4,096), and step 3 was described as issuing 4,096-token requests (a profile's
`n_predict` raises the request ceiling to 8,192 or 32,768).

## 1. The generation-budget contract

The campaign's central defect: `Invoke-V2ProfileQualification.ps1` forwarded `--max-tokens` to
`qualify.py` only when an operator typed `-MaxTokens`. None of the four cells did, so every suite
used its own fixture default and `qwen38-27b-huge-256k` — a profile served with
`n_predict: 32768` and `reasoning_budget: 24576` — was benchmarked through an 8,192-token request
cap, below its thinking budget alone.

### The effective-ceiling algorithm

One function, `Resolve-V2QualificationRequestBudget` in `scripts/v2/Common.ps1`. First, what the
HTTP request will ask for:

```
1. explicit   -MaxTokens > 0            -> that value.        source = "explicit"
2. profile    -NPredict  > 0            -> that value.        source = "profile"
3. fixture    neither                   -> 0, meaning "each suite keeps its own default".
                                                              source = "fixture"
```

**What the request asks for is not what the server will emit.** llama-server stops at `n_predict`
whatever `max_tokens` says, so the number every budget rule is evaluated against is the smaller of
the two:

```
effective_generation_ceiling = min(request_max_tokens, n_predict)
```

with 0 on either side meaning "not stated" (a request of 0 leaves the suites their own fixture
defaults; an `n_predict` of 0 means the profile declares none). Then, before anything expensive
happens, the same answer-reserve invariant the manifest already enforces — against the *effective*
ceiling, never against the request:

```
minimum_answer_reserve = min(8192, effective_generation_ceiling)
answer_reserve         = effective_generation_ceiling - reasoning_budget   (reasoning_budget > 0)
ok                     = answer_reserve >= minimum_answer_reserve
```

8,192 is the **minimum answer reserve the output contract chose** — the number of answer tokens
every fixture in this repository fits in. It is explicitly *not* "the smallest default any suite
uses": `coding` and `hard` ask for 8,192, but `tools`, `literal_tools`, `json` and `retention` all
ask for 4,096. When neither a request nor an `n_predict` is stated, the ceiling is per-suite and
unknown to this function, so the invariant falls back to that floor.

`min(8192, ceiling)` rather than a flat 8,192 so a profile whose whole ceiling is below the floor
is judged against itself. The consequence is deliberate and inherited from the manifest rule: a
profile with `n_predict: 8192` cannot fund *any* bounded reasoning until its ceiling is raised.

Not ok, and no `-ConstrainedRequestBudgetDiagnostic`? It throws, before the model loads.

#### Why `min()` and not "the operator wins"

An explicit `-MaxTokens` above the profile's `n_predict` used to outrank it outright, so:

```
-NPredict 8192 -MaxTokens 32768 -ReasoningBudget 24576
    was:  ceiling 32768 -> reserve 32768 - 24576 =   8192  -> accepted as a baseline
    now:  ceiling  8192 -> reserve  8192 - 24576 = -16384  -> refused before the model loads
```

The server would have stopped at 8,192 tokens with 24,576 of them already committed to thinking.
There is no answer at the end of that; the request ceiling was arithmetic, not headroom. This is
now a deterministic regression on both sides — `test_impossible_expanded_budget_is_refused` in
`scripts/v2/eval/test_qualify_budget.py` and the `$expandedRefused` block in
`Test-V2ConfigGeneration.ps1`.

### What the current profiles resolve to

| Profile | `n_predict` | `reasoning_budget` | Request | Effective ceiling | Source | `budget_profile` | Answer reserve |
|---|--:|--:|--:|--:|---|---|--:|
| `gemma4-12b-qat-ud-q4xl` | — | — | fixture defaults | fixture defaults (8192 / 4096) | `fixture` | `deployment` | n/a |
| `qwen38-27b-deep-32k` | 8,192 | — | 8,192 | 8,192 | `profile` | `deployment` | n/a |
| `qwen38-27b-agent-128k` | 8,192 | — | 8,192 | 8,192 | `profile` | `deployment` | n/a |
| `qwen38-27b-huge-256k` | 32,768 | 24,576 | 32,768 | **32,768** | `profile` | `deployment` | 8,192 |

The Huge row is the fix: 32,768 instead of the 8,192 it was measured at.

### Retention is the one suite that cannot simply honour the ceiling

At 91% occupancy of a 262,144-token window there is no room for a 32,768-token answer. Asking for
one would fail the prefill rather than measure it. `resolve_retention_reserve` in `qualify.py`
clamps the ceiling to the room actually left (minus a 1,024-token margin) and never drops below the
4,096-token floor the probes need, reporting which of `fixture` / `profile` / `context-clamped`
applied.

The floor is a floor on *what to ask for*, and never a promise that it fits. Past the point where
even 4,096 tokens do not fit — a 261,000-token prefill in a 262,144-token window leaves 120 after
the margin — the reserve resolves to `(0, INSUFFICIENT_CONTEXT_RESERVE)`, **no request is sent**,
and the row records the window arithmetic with a `failure_taxonomy` of
`["INSUFFICIENT_CONTEXT_RESERVE"]`. Previously it returned 4,096 regardless and sent a request the
server had to reject for exceeding `n_ctx`; a rejected request measures nothing, and a row carrying
a rejection reads like a model failure. The fix is a hard invariant — `prompt + reserve + margin <=
n_ctx` for every reserve this function returns — asserted at the exact boundary from both sides in
`test_retention_insufficient_context_reserve`. The remedy for such a cell is to lower the retention
depth or raise `n_ctx`; the harness will not silently shrink the corpus, because a probe at 240k
that quietly becomes one at 220k is a different measurement wearing the same label.

### Reporting

Every qualification report now carries `request_budget`: `request_max_tokens`, `n_predict` and the
`effective_generation_ceiling` derived from them, plus the source, the `budget_profile`, the
reasoning budget, the answer reserve, the minimum, whether the reserve is satisfied, and whether
the cell was a deliberate constrained diagnostic. `qualify.py` records the per-suite fixture
defaults alongside, so a reader can see what *would* have been used. The request the operator made
is recorded verbatim and never rewritten to the effective ceiling — both numbers are kept, because
the gap between them is the thing worth seeing.

A `NO_ANSWER` in a future report is therefore always attributable to either the deployed contract
or a named benchmark cap, with no reconstruction required.

### Deliberately measuring a constrained cap

Still possible, but it has to be asked for by name:

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -ConstrainedRequestBudgetDiagnostic -MaxTokens 8192 ...
```

The plan and the report both label it, and `policy_profile` in the `qualify.py` output reads
`diagnostic` rather than `baseline`.

## 2. Baseline versus diagnostic policy — keep them apart

`qualify.py` already supported `--system-policy agentic`, `--strict-tool-policy` and
`--tool-schema-policy`. They stay **off** for the baseline, because baseline means the profile as
actually served, and a score measured under a different system prompt is not comparable to one
that was not.

The report now says which it was, in one field:

```json
"policy_profile": "baseline"   // served policies AND the served generation budget
"policy_profile": "diagnostic" // anything else
```

**Baseline means the profile as served, and the generation budget is half of "as served".** A
score measured under a request ceiling the deployment does not grant is no more comparable than one
measured under a different system prompt, so `budget_profile` sits beside the policy flags and
feeds the same field:

| `budget_profile` | When | Baseline? |
|---|---|---|
| `deployment` | no override at all, a ceiling derived from `n_predict`, or an explicit ceiling that restates the contract exactly | yes |
| `constrained` | an explicit ceiling **below** the contract — a `NO_ANSWER` may be the benchmark cap rather than the model | no |
| `expanded` | an explicit ceiling **above** the contract — the server truncates back to `n_predict`, so a pass is not evidence about the deployed profile | no |

With no declared `n_predict` the per-suite fixture defaults are the contract, and a uniform
explicit ceiling is compared against the widest of them (the coding suite's 8,192). `policy_profile`
therefore reads `diagnostic` when *any* of these holds: a non-`current` system policy, a strict
tool policy, a tool schema policy, a `budget_profile` other than `deployment`, or
`--allow-constrained-request-budget`. The runner also prints a warning naming the cell as a
diagnostic before it loads anything, so it is caught at plan time rather than in a report someone
re-reads a week later.

If the next run wants to know whether the literal-argument weakness lives in the model, the system
policy, or the tool schema, that is an A/B of three separately labelled cells — never a baseline
quietly re-run under a stricter prompt. The commands are the optional cells in §8 step 3.

Worth knowing before spending a night on it: `benchmarks/REPORT-gemma4-26b-a4b-gfx1201-20260821.md`
already ran that A/B on Gemma 4 26B (agents G, H, I) and none of the three policies recovered the
`search_files` literal-argument failure. That failure has since turned out to be substantially a
fixture defect (errata E3), which is a better explanation than any of them.

## 3. Unity — the fixture stays strict

No verifier was relaxed, no task rewritten, no model-specific exception added.
`benchmarks/campaign-20260823-full` records `qwen38-27b-deep-32k` producing C# that declares
`GameObject instance` twice in incompatible scopes; `dotnet build` returned **CS0136**. That is a
model defect and it stays classified as one.

Three deterministic regressions now pin it, in `scripts/v2/eval/test_verifiers.py`:

| Regression | Asserts |
|---|---|
| known-good `ProjectilePool` | compiles against `UNITY_SHIM` — the fixture is passable without any model |
| Deep's 2026-08-23 answer, verbatim | does **not** compile, and the output contains `CS0136` |
| the same answer, second declaration renamed | compiles — the gate rejected the defect, not the approach |

`unity_impl` results for Gemma, Huge and Agent remain unmeasured: two hit the budget ceiling
corrected in §1, and Agent's request timed out after 900 seconds.

## 4. Tool fixtures — literals must be supplied, exactness must not be relaxed

Three prompts were asking models to guess a literal and then grading them for guessing wrong.
**Expected values did not change.**

| Fixture | Was | Now |
|---|---|---|
| `tool_pick_search_many_nested` | "all Go files … excluding vendor" | "including the glob `` *.go `` and excluding the glob `` vendor/** ``" |
| `literal_regex` | "all Go files … excluding vendor" | "including the glob `` *.go `` and excluding the glob `` vendor/** ``" |
| `literal_cs_glob` | globs stated; case sensitivity not | "case-sensitive" stated; globs unchanged |

`literal_cs_glob`'s globs were already explicit and stay strict — Deep and Huge omitting `exclude`
is a genuine literal-argument failure. What was added is the `case_sensitive` value, a *required*
field in the tool schema that the prompt never mentioned, so the grader was scoring a coin flip.

`scripts/v2/eval/test_tool_grading.py` now asserts this deterministically for every fixture: any
string the grader compares byte-exactly (globs, regexes, paths) must appear verbatim in the prompt,
and any required boolean must be stated. That is the regression that stops this class of defect
returning.

The same test also pins the strict side: `vendor`, `vendor/`, `vendor/*`, `vendor/**` and
`**/vendor/**` are five different arguments and always will be; reordered arrays, comma-joined
strings, flattened nested objects, and missing nested required fields all fail. The one documented
latitude — a string-encoded scalar like `{"verbose": "true"}` — is asserted to be accepted, and
asserted not to extend to paths, globs or regexes.

Writing those tests exposed that tool-argument strings were being compared through `norm()`, which
folds case and strips whitespace. They are now compared byte-for-byte. Re-graded against all 44
tool rows in the raw 2026-08-23 arguments, this changes no historical verdict.

## 5. Failure taxonomy

Preserved and extended. Output exhaustion is still never converted into a compile failure.

The gap that mattered: a failed HTTP request produced a row with an `error` string and **no
`failure_taxonomy` at all**, which is how a 900-second timeout came to be written up as a model
returning an empty response. Every suite now tags transport failures.

| Category | Means |
|---|---|
| `MODEL_OUTPUT_FAILURE` | the model answered and the answer was wrong |
| `COMPILE_ERROR` | the answer did not build (matched in English **and** pt-BR — the host locale) |
| `NO_ANSWER` | generation ended with an empty `content` field |
| `REASONING_EXHAUSTED` | …and it ended inside `reasoning_content` |
| `OUTPUT_LENGTH` | generation stopped at the request ceiling |
| `REQUEST_TIMEOUT` | the HTTP request never returned in time |
| `REQUEST_ERROR` | the HTTP request failed for any other reason |
| `TOOL_ARGUMENT_ERROR` | the right tool, with mutated or missing arguments |
| `STRUCTURED_OUTPUT_ERROR` | the reply was not the object the schema asked for |

Sub-reasons (`CONSTRAINT_VIOLATION`, `INVENTED_API`, `SYNTAX_ERROR`, `TEST_FAILURE`,
`VERIFIER_TIMEOUT`, `INVALID_TOOL`, `PARSER_FAILURE`) accompany a canonical tag rather than
replacing it. `VERIFIER_TIMEOUT` — the toolchain hanging on the model's code — is deliberately a
different event from `REQUEST_TIMEOUT`.

## 6. Gemma — candidate staged, deliberately not deployed

`gemma4-12b-qat-ud-q4xl` is `provider.public_model`, the always-on default. **`config/models.yaml`
is unchanged.** Changing what the default serves on the strength of static analysis would alter
real user behaviour with no physical evidence behind it.

### The evidence

Seven of Gemma's `coding` and `hard` failures on 2026-08-23 are one event, and the separation in
the raw data is clean:

| | reasoning chars | output tokens |
|---|---|---|
| 10 passing rows | 1,386 – 17,202 | 503 – 6,125 |
| 7 no-answer rows | 25,159 – 29,818 | **8,192 — all seven** |

Every failing run sat exactly on the ceiling; no passing run came near it.

`benchmarks/REPORT-gemma4-26b-a4b-gfx1201-20260821.md` §14 measured the shape of the fix on
**Gemma 4 26B A4B** — a different size in the same family, which is the gap this candidate has to
close physically:

| Output cap | Reasoning budget | Result |
|--:|--:|---|
| 8,192 | unrestricted | no answer, 17,147 reasoning chars |
| 16,384 | unrestricted | **no answer again**, 33,795 reasoning chars |
| 16,384 | 8,192 | diagnostic coding 6/6 |
| 16,384 | 4,096 | 6/6 |
| 16,384 | 2,048 | 6/6 |
| 16,384 | 0 | 6/6 diagnostic, 10/10 full coding |

Raising the cap alone does not work. A bounded reasoning budget with a separately reserved answer
budget does. That report also states explicitly: do not copy Qwen Huge's `reasoning_budget: 24576`
into Gemma.

### The staged candidate

```jsonc
// config/models.yaml, gemma4-12b-qat-ud-q4xl — NOT APPLIED
"max_output_tokens": 16384,   // was 8192
"n_predict": 16384,           // new
"reasoning_budget": 8192,     // new
"reasoning_budget_message": "Thinking budget reached. Stop analysing and write the final answer now."
```

Why these numbers, from repository evidence only:

- **`reasoning_budget: 8192`** is the largest budget measured to fix the no-answer on Gemma 4 26B,
  and it sits above every reasoning length Gemma 4 12B produced on a *passing* run (17,202 chars ≈
  5.6k tokens at the ~3.07 chars/token this model averaged). Smaller budgets also worked in the
  26B A/B; the largest one that works is chosen so the correction costs the least reasoning.
- **`n_predict: 16384`** is `reasoning_budget` plus the 8,192-token answer reserve the repository's
  own invariant requires — `min(8192, 16384) = 8192`, and `16384 − 8192 = 8192`. It is also the cap
  the 26B A/B used.
- **`reasoning_budget_message`** matches the phrasing already proven on `qwen38-27b-huge-256k`.
- **`reasoning_budget: 0`** scored best of all on 26B and is *not* proposed: it disables thinking
  entirely for a model that is the interactive default, which is a behaviour change well beyond
  fixing a budget. It belongs in the A/B in §8 step 4, not in the manifest.

### What has been verified statically, tonight

Checked without loading a model — evidence, not qualification:

| Check | Result |
|---|---|
| `Assert-V2ManifestSemantics` accepts the candidate | **accepted** |
| `Resolve-V2QualificationRequestBudget` | ceiling 16,384, source `profile`, reserve 8,192, ok |
| `unsloth-runtime-candidate` accepts `--n-predict` | **supported** |
| `unsloth-runtime-candidate` accepts `--reasoning-budget` | **supported** |
| `unsloth-runtime-candidate` accepts `--reasoning-budget-message` | **supported** |

The last three were previously unverified and are hard preconditions: Gemma runs on the unsloth
build, not on the `b10549` runtime the Qwen profiles use, and the flag support measured in the 26B
report was measured on the latter.

### What is still unproven

Everything that matters. The A/B was run on a different model size, and nothing has measured
whether a bounded reasoning budget preserves Gemma 4 12B's `tools`, `json` and `retention` results
— including the unexplained 0.00 retention score at 25% occupancy. **Gemma is not promoted, its
manifest is unchanged, and the candidate is qualified only by step 5b in §8** — a full campaign run
with `n_predict: 16384` and `reasoning_budget: 8192` on every suite. The step 4 A/B decides whether
that run is worth its hours; it does not itself qualify anything, and neither does a full campaign
run under the profile as served today.

## 7. Qwen3.6-35B-A3B — evaluated separately, deliberately outside this canary set

Not a missing candidate, not untested, and **not scheduled for requalification**.

It had its own physical campaign on 2026-08-22:
`benchmarks/REPORT-qwen36-35b-a3b-gfx1201-20260822.md` and
`benchmarks/campaign-qwen36-35b-a3b/`. All of it stays where it is.

The exact historical verdict, preserved because it is more specific than "rejected":

- **§12 Recommended profiles: "Not yet proposed."** No Qwen3.6 profile was ever recommended, and
  the blocker recorded is filled-context decode — `llama-bench` has no `--ctx-size`, so no
  throughput number in that report is measured against an occupied window. It is not a quality
  rejection.
- **Quality was measured per quantization, and the spread is narrow.** §11.1: UD-IQ4_XS **10/10**
  coding (the best coding result of any model in that campaign), UD-Q3_K_S 9/10, UD-Q2_K_XL 9/10,
  UD-Q3_K_XL 8/10. The report notes the spread rests on one hard task plus one reasoning overrun,
  "not a general gradient of competence".
- **Only one artifact was dropped from disk**, in commit `cda2639`: Qwen3.6-35B-A3B **UD-Q3_K_XL**,
  "weakest of its siblings on coding quality and reasoning behavior". That statement is about
  Q3_K_XL specifically. It is not a verdict on IQ4_XS, Q3_K_S or Q2_K_XL, and it must not be
  generalised into one.
- **`tool_pick_search_many_nested` failed on all four Qwen3.6 quantizations too**, which §11.1 read
  as "a property of the model rather than of the bits". Errata E3 gives a better explanation: the
  fixture demanded a literal it never supplied. Eight model configurations across two campaigns
  failed that task.

The 2026-08-23 campaign covers **the four current canary candidates**, not an inventory of every
model ever evaluated on this machine. Qwen3.6-35B-A3B was evaluated on its own terms, did not
advance into this canary set, and stays out of the re-run below unless someone explicitly asks for
it.

## 8. Next-night physical validation — the exact commands

**None of this was run.** In this order; steps 1–4 are cheap and should gate step 5.

Every command is repository-native. `-DryRun` (new) resolves the budget and prints both command
lines without loading anything, so each step can be reviewed before it costs hours.

### Step 0 — fast gate, no GPU (repeat before starting)

```powershell
.\scripts\v2\Test-V2Manifest.ps1; .\scripts\v2\Test-V2ConfigGeneration.ps1; .\scripts\v2\Test-V2HarnessConfig.ps1
```

```powershell
python .\scripts\v2\eval\test_qualify_budget.py; python .\scripts\v2\eval\test_tool_grading.py; python .\scripts\v2\eval\test_verifiers.py $env:TEMP\verifiers
```

### Step 1 — budget-contract regression on the profile that had it wrong

Confirms Huge now requests its declared 32,768-token contract. Coding only, one task, minutes not
hours.

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -DryRun -Label huge-256k-budget -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-recheck -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q2_K_XL.gguf -RuntimeRoot C:\IA\runtimes\llama.cpp\b10549-rocm-7.14 -ContextTokens 262144 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 288 -TensorOverride 'blk\.(6[0-3])\.ffn_.*=CPU' -NPredict 32768 -ReasoningBudget 24576 -ReasoningBudgetMessage 'Thinking budget reached. Stop analysing and write the final answer now.' -Port 19404 -Suites 'coding' -OnlyCodingTasks 'unity_impl'
```

Re-run the identical command without `-DryRun` to execute it. Expect `request_budget.source` =
`profile`, `request_max_tokens` = `effective_generation_ceiling` = 32768, and `budget_profile` =
`deployment` in the report. Any other `budget_profile` means the cell is not a baseline and step 5
must not be started on the strength of it.

### Step 2 — `unity_impl` on the three profiles whose result was never a Unity measurement

Huge hit the ceiling corrected in §1; Agent timed out. Deep is excluded — its CS0136 is a settled
model failure and is already pinned by a deterministic regression.

Gemma is the exception and is included for a different reason. Its ceiling was **not** corrected:
it declares no `n_predict`, so 8,192 was both the fixture default and a fair reading of its
contract, and this cell requests 8,192 again. A repeat `NO_ANSWER` here is therefore the *expected*
result and confirms §6's diagnosis rather than refuting it — the cell that answers Gemma's
exhaustion is the candidate in step 4, not this one.

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -Label huge-256k-unity -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-recheck -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q2_K_XL.gguf -RuntimeRoot C:\IA\runtimes\llama.cpp\b10549-rocm-7.14 -ContextTokens 262144 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 288 -TensorOverride 'blk\.(6[0-3])\.ffn_.*=CPU' -NPredict 32768 -ReasoningBudget 24576 -ReasoningBudgetMessage 'Thinking budget reached. Stop analysing and write the final answer now.' -Port 19404 -Suites 'coding' -OnlyCodingTasks 'unity_impl'
```

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -Label agent-128k-unity -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-recheck -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q3_K_XL.gguf -RuntimeRoot C:\IA\runtimes\llama.cpp\b10549-rocm-7.14 -ContextTokens 131072 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 288 -TensorOverride 'blk\.(6[0-3])\.ffn_.*=CPU' -NPredict 8192 -Port 19403 -Suites 'coding' -OnlyCodingTasks 'unity_impl'
```

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -Label gemma4-12b-unity -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-recheck -ModelPath C:\IA\models\gemma-4-12B-it-qat-GGUF\gemma-4-12B-it-qat-UD-Q4_K_XL.gguf -RuntimeRoot C:\Users\Sitr3n\.unsloth\llama.cpp\build\bin\Release -ContextTokens 131072 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 512 -Port 19401 -Suites 'coding' -OnlyCodingTasks 'unity_impl'
```

### Step 3 — the corrected nested and literal tool fixtures

The fastest way to separate the fixture defect from a real literal-preservation weakness: tool
replies are short, so these cells are minutes rather than hours.

Not 4,096-token requests, despite 4,096 being the `tools` and `literal_tools` fixture default. A
profile that declares `n_predict` makes the runner derive the request ceiling from it and apply it
uniformly, so `-NPredict 8192` sends `max_tokens: 8192` and Huge's `-NPredict 32768` sends
`max_tokens: 32768`. Only Gemma, which declares no `n_predict`, actually requests the 4,096-token
fixture default. The ceiling is a cap, not a target — a tool call that needs 200 tokens costs 200
either way — but a report that says "4,096-token requests" is describing a run that did not happen.

Run on all four; Deep shown, the other three differ only in `-ModelPath` / `-ContextTokens` /
`-CacheType*` / `-UBatchSize` / `-Port` / `-NPredict` as above.

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -Label deep-32k-tools -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-recheck -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-IQ4_XS.gguf -RuntimeRoot C:\IA\runtimes\llama.cpp\b10549-rocm-7.14 -ContextTokens 32768 -CacheTypeK q8_0 -CacheTypeV q8_0 -UBatchSize 288 -TensorOverride 'blk\.(6[0-3])\.ffn_.*=CPU' -NPredict 8192 -Port 19402 -Suites 'tools,literal_tools'
```

Optional, and only if step 3 still shows literal-argument failures — the labelled policy A/B from
§2. Three cells, compared against each other and never against a baseline:

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -Label deep-32k-tools-strict -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-recheck -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-IQ4_XS.gguf -RuntimeRoot C:\IA\runtimes\llama.cpp\b10549-rocm-7.14 -ContextTokens 32768 -CacheTypeK q8_0 -CacheTypeV q8_0 -UBatchSize 288 -TensorOverride 'blk\.(6[0-3])\.ffn_.*=CPU' -NPredict 8192 -Port 19402 -Suites 'tools,literal_tools' -StrictToolPolicy
```

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -Label deep-32k-tools-schema -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-recheck -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-IQ4_XS.gguf -RuntimeRoot C:\IA\runtimes\llama.cpp\b10549-rocm-7.14 -ContextTokens 32768 -CacheTypeK q8_0 -CacheTypeV q8_0 -UBatchSize 288 -TensorOverride 'blk\.(6[0-3])\.ffn_.*=CPU' -NPredict 8192 -Port 19402 -Suites 'tools,literal_tools' -ToolSchemaPolicy
```

### Step 4 — the tasks that produced NO_ANSWER, at the corrected ceiling

Huge's seven, plus Gemma's under its staged candidate. This is also the Gemma A/B: run it once as
served today and once with the §6 candidate passed on the command line, and compare. The manifest
stays unchanged either way — the flags are supplied to the runner, not written to
`config/models.yaml`.

Huge, corrected ceiling:

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -Label huge-256k-noanswer -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-recheck -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q2_K_XL.gguf -RuntimeRoot C:\IA\runtimes\llama.cpp\b10549-rocm-7.14 -ContextTokens 262144 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 288 -TensorOverride 'blk\.(6[0-3])\.ffn_.*=CPU' -NPredict 32768 -ReasoningBudget 24576 -ReasoningBudgetMessage 'Thinking budget reached. Stop analysing and write the final answer now.' -Port 19404 -Suites 'coding,hard' -OnlyCodingTasks 'go_impl,ts_refactor,unity_impl'
```

Gemma as served today (control):

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -Label gemma4-12b-noanswer-control -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-recheck -ModelPath C:\IA\models\gemma-4-12B-it-qat-GGUF\gemma-4-12B-it-qat-UD-Q4_K_XL.gguf -RuntimeRoot C:\Users\Sitr3n\.unsloth\llama.cpp\build\bin\Release -ContextTokens 131072 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 512 -Port 19401 -Suites 'coding,hard'
```

Gemma under the staged candidate (§6):

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -Label gemma4-12b-noanswer-candidate -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-recheck -ModelPath C:\IA\models\gemma-4-12B-it-qat-GGUF\gemma-4-12B-it-qat-UD-Q4_K_XL.gguf -RuntimeRoot C:\Users\Sitr3n\.unsloth\llama.cpp\build\bin\Release -ContextTokens 131072 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 512 -NPredict 16384 -ReasoningBudget 8192 -ReasoningBudgetMessage 'Thinking budget reached. Stop analysing and write the final answer now.' -Port 19401 -Suites 'coding,hard'
```

The `reasoning_budget: 0` variant the 26B report scored best, as a third diagnostic point only —
replace `-ReasoningBudget 8192` with `-ReasoningBudget 0` and drop `-ReasoningBudgetMessage` (a
message requires a positive budget).

**What "the candidate passes" means, decided before the run:** the candidate cell answers the
`coding` and `hard` tasks that produced `NO_ANSWER` on 2026-08-23 — no row with an empty `content`
field and `finish_reason: length` — and does not lose a task the control passes. Nothing weaker
counts, and no grader moves either way.

**What passing gates:** the candidate's own full qualification in step 5, with the same
`16384 / 8192` flags in every suite. It does **not** gate a `config/models.yaml` edit — that is
downstream of step 5, not of step 4.

### Step 5 — the full campaign

Only after steps 1–4 look right. Sequential (`provider.max_loaded_models: 1`), same suites,
retention depths and ports as 2026-08-23 so the two campaigns stay comparable. Expect roughly nine
hours for the four deployed profiles, plus about two more if the Gemma candidate cell runs.

#### The Gemma cell is two cells, and only one of them can qualify the candidate

The mistake this replaces: step 4 A/B-tested the candidate at `-NPredict 16384 -ReasoningBudget
8192`, and step 5 then ran Gemma with **no budget flags at all** — the profile as served today.
Passing step 4 and then qualifying the old configuration qualifies nothing. A run can only qualify
the contract it was run under.

So:

**5a — `gemma4-12b-control-full`, the profile as served today.** Runs whether or not the candidate
passes step 4. It is the comparison point against 2026-08-23 and the record of what
`config/models.yaml` currently serves. It is **not** a qualification of the candidate and must
never be cited as one.

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -Label gemma4-12b-control-full -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-full -ModelPath C:\IA\models\gemma-4-12B-it-qat-GGUF\gemma-4-12B-it-qat-UD-Q4_K_XL.gguf -RuntimeRoot C:\Users\Sitr3n\.unsloth\llama.cpp\build\bin\Release -ContextTokens 131072 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 512 -Port 19401 -Suites 'chat,coding,hard,tools,literal_tools,json,retention' -RetentionTokens 16384,32768,65536,98304,120000
```

**5b — `gemma4-12b-candidate-full`, the candidate under its own contract.** Run this **only if the
candidate passed the step 4 A/B**, and run it with the §6 flags on **every** suite — `chat`,
`coding`, `hard`, `tools`, `literal_tools`, `json` and `retention` alike. A bounded reasoning budget
is not a coding-suite setting: the open question is precisely whether it costs Gemma anything on
`tools`, `json` and retention, including the unexplained 0.00 retention score at 25% occupancy, and
a suite run without the flags cannot answer that.

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -Label gemma4-12b-candidate-full -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-full -ModelPath C:\IA\models\gemma-4-12B-it-qat-GGUF\gemma-4-12B-it-qat-UD-Q4_K_XL.gguf -RuntimeRoot C:\Users\Sitr3n\.unsloth\llama.cpp\build\bin\Release -ContextTokens 131072 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 512 -NPredict 16384 -ReasoningBudget 8192 -ReasoningBudgetMessage 'Thinking budget reached. Stop analysing and write the final answer now.' -Port 19401 -Suites 'chat,coding,hard,tools,literal_tools,json,retention' -RetentionTokens 16384,32768,65536,98304,120000
```

Reading the two reports:

| | 5a control | 5b candidate |
|---|---|---|
| `request_budget.n_predict` | `null` | 16384 |
| `request_budget.effective_generation_ceiling` | `null` (per-suite fixture defaults) | 16384 |
| `request_budget.budget_profile` | `deployment` | `deployment` |
| `policy_profile` | `baseline` | `baseline` |
| Qualifies the candidate | **no** | yes |
| Describes what `config/models.yaml` serves | yes | **no** |

Both read `deployment`, and correctly: each cell requests exactly what the server it is talking to
was told to serve. `budget_profile` compares the request against the running server's contract, not
against the manifest — so it cannot, on its own, tell you that 5b is running a profile the manifest
does not contain. That is what the `-candidate-` label and this table are for. Only a
`config/models.yaml` edit makes 5b's contract the deployed one, and that edit is what 5b exists to
justify.

A `config/models.yaml` change may cite 5b and only 5b. If 5b was not run, the candidate is not
qualified and the manifest does not move — the same rule as §6, unchanged.

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -Label qwen38-deep-32k-full -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-full -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-IQ4_XS.gguf -RuntimeRoot C:\IA\runtimes\llama.cpp\b10549-rocm-7.14 -ContextTokens 32768 -CacheTypeK q8_0 -CacheTypeV q8_0 -UBatchSize 288 -TensorOverride 'blk\.(6[0-3])\.ffn_.*=CPU' -NPredict 8192 -Port 19402 -Suites 'chat,coding,hard,tools,literal_tools,json,retention' -RetentionTokens 4096,8192,16384,24576,28000
```

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -Label qwen38-agent-128k-full -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-full -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q3_K_XL.gguf -RuntimeRoot C:\IA\runtimes\llama.cpp\b10549-rocm-7.14 -ContextTokens 131072 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 288 -TensorOverride 'blk\.(6[0-3])\.ffn_.*=CPU' -NPredict 8192 -Port 19403 -Suites 'chat,coding,hard,tools,literal_tools,json,retention' -RetentionTokens 16384,32768,65536,98304,120000
```

```powershell
.\scripts\v2\Invoke-V2ProfileQualification.ps1 -Label qwen38-huge-256k-full -OutputRoot C:\IA\IA_local_server\benchmarks\campaign-20260824-full -ModelPath C:\IA\models\Qwen3.8-27B-GGUF\Qwen3.8-27B-UD-Q2_K_XL.gguf -RuntimeRoot C:\IA\runtimes\llama.cpp\b10549-rocm-7.14 -ContextTokens 262144 -CacheTypeK q4_0 -CacheTypeV q4_0 -UBatchSize 288 -TensorOverride 'blk\.(6[0-3])\.ffn_.*=CPU' -NPredict 32768 -ReasoningBudget 24576 -ReasoningBudgetMessage 'Thinking budget reached. Stop analysing and write the final answer now.' -Port 19404 -Suites 'chat,coding,hard,tools,literal_tools,json,retention' -RetentionTokens 32768,65536,131072,196608,240000
```

Add `-CaptureDiagnostics` to any cell whose raw wire traffic is worth keeping.

### Step 6 — compare against the immutable 2026-08-23 campaign

```powershell
python .\scripts\v2\eval\summarize_campaign.py C:\IA\IA_local_server\benchmarks\campaign-20260824-full --out C:\IA\IA_local_server\benchmarks\campaign-20260824-full\summary.json
```

Read `request_budget` in each new report first: a comparison is only meaningful between cells whose
effective generation ceiling, `budget_profile` and `policy_profile` are stated and understood. Two
cells with different `budget_profile` values are not comparable as baselines, and
`gemma4-12b-control-full` and `gemma4-12b-candidate-full` are two different contracts even though
both read `deployment`. `benchmarks/campaign-20260823-full/` is evidence and is never overwritten —
the new run writes to `campaign-20260824-*`.

The `hard` suite is not summarised by `summarize_campaign.py` (the transcription errors in errata
E5 are downstream of that); read `qualify-*.json` → `suites.hard.passed` / `.total` directly.

### Step 7 — the 72-hour soak

Separate, and only after the qualification above is satisfactory. Described in
`docs/BENCHMARKS.md`. Not scheduled by this document.

### Explicitly not in this re-run

Qwen3.6-35B-A3B, for the reasons in §7.

## 9. Status

| | |
|---|---|
| Harness defects fixed and covered by fast tests | yes |
| Effective generation ceiling enforced as `min(max_tokens, n_predict)` | yes |
| Every benchmark cell classified `deployment` / `constrained` / `expanded` | yes |
| Retention refuses to send a request larger than the window | yes |
| Historical evidence modified | **no** |
| Historical scores or grader thresholds modified | **no** |
| `config/models.yaml` modified | **no** |
| Models requalified | **no — nothing below §8 has been run** |

Nothing in this document licenses calling any model requalified. That happens when step 5 completes
and its results are read.
