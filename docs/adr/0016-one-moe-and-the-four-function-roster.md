# ADR 0016: Four functions, four artifacts, and one MoE

## Status

Proposed. The MoE profile added here is `candidate` in the `canary` deployment.
`provider.public_model` still resolves to `gemma4-12b-qat-ud-q4xl` and is not
changed here.

**2026-09-28:** `provider.public_model` now resolves to
`gemma4-12b-qat-ud-q4xl-256k` — see the addendum at the end.

Supersedes the "huge context" third of [ADR 0012](0012-three-qwen38-profile-classes-and-the-output-contract.md).
That ADR's premise — that context is the wrong axis to build a profile on, and
that a profile should be built on a *job* — is kept, and is in fact the reason
for this one. What changes is which artifact does the huge-context job.

## Context

`C:\IA\models` held eleven GGUFs across four families and the volume was down to
34.21 GB free. Most of them were intermediate rungs of quantization ladders that
had already been walked: two Qwen3.6 rungs, two Ornith rungs, and two Qwen3.8
rungs that no active profile named.

Underneath the disk problem was a roster problem. `config/models.yaml` carried
ten model entries for four distinct jobs, and one of those jobs — huge active
context — was being served by `qwen38-27b-huge-256k`, a 27B dense model at
Q2_K_XL. That profile exists because ADR 0012 needed *something* to hold 256k,
not because a 2-bit dense 27B is the right shape for the job. A sparse MoE that
activates 3B of 35B parameters is: it holds the same window with better
throughput at depth, which is exactly what a huge-context profile is for.

Two MoE candidates had been measured against each other across a placement
census, a throughput sweep and a retention ramp on 2026-08-24, and the
comparison had stopped one step short of a decision. Two things were blocking
it:

**Ornith could not be given a `developer` message.** Its GGUF ships the upstream
Qwen3-Coder chat template, which accepts exactly one authority message and only
at index 0. `llama.cpp` rewrites `developer` to `system` before rendering, so
`system -> developer -> user` arrives as two system messages, calls
`raise_exception`, and returns HTTP 500. Qwen3.6 never had the problem because
unsloth had already patched its copy of the same template. The difference was
one template, not the weights.

**Qwen3.6 had never had a full quality suite,** and the one it was eventually
given was run without a reasoning budget while its sibling profile in the same
manifest uses one. Three of its five failures turned out to be `NO_ANSWER` —
8,192 output tokens consumed with 32,000 characters still in
`reasoning_content` — which is a configuration defect, not a quality gap.

## Decision

### 1. Four jobs, four artifacts, and nothing kept that does not name a job

| Job | Artifact | Why this one |
|---|---|---|
| general / public | Gemma 4 12B QAT UD-Q4_K_XL | small, fast, the public model |
| dense, hardest localized | Qwen3.8 27B UD-IQ4_XS | reasoning per token, 32k is enough |
| dense agentic default | Qwen3.8 27B UD-Q3_K_XL | 128k, daily coding-agent work |
| long-context MoE | **Qwen3.6 35B-A3B UD-Q2_K_XL** | 256k with 3B active |

Six GGUFs that named no job were deleted by exact path — 92.52 GB recovered.
An artifact stays only if it is the answer to one of these four questions.

### 2. The long-context job goes to Qwen3.6 35B-A3B, not Ornith 1.5

Measured head to head under identical conditions, with the template defect
fixed and both models given the same reasoning budget:

| | Qwen3.6 Q2_K_XL | Ornith IQ2_M |
|---|:---:|:---:|
| contract gate | 9/9 graded, PASS | 9/9 graded, PASS |
| retention, all ten levels | 120/120 | 120/120 |
| quality | **31/34** | 29/34 |
| decode at 240,000 | 32.92 t/s | **34.64 t/s** |
| peak dedicated VRAM | **11,922 MiB** | 12,283 MiB |
| same suite, wall clock | 757.0 s | **218.6 s** |

Quality was set as the deciding dimension, with throughput breaking a tie. There
is no tie: Ornith loses both `literal_tools` cases to a single defect — it omits
the `exclude` filter from a tool call while its own reasoning names the
exclusion. For an agentic model that is the worst shape of failure, because a
search that silently includes `vendor/**` returns wrong results that look right.

Ornith is nonetheless **3.5× faster end to end**, because it answers with a
third of the tokens. That is recorded rather than dismissed; it is the reason
this decision could be revisited if a speed-first long-context role is ever
worth its own slot. It is not worth a second 12 GB artifact today.

### 3. `chat_template_file` becomes a manifest field

A GGUF's embedded template is the right default and stays the default. But a
template can be wrong for *this* server's contract while being fine in general,
and when it is, the fix belongs next to the manifest rather than in a wrapper
script. `config/chat-templates/` holds the corrected template beside the
verbatim upstream copy it was derived from, so `diff` between them is the patch,
and `scripts/v2/eval/test_chat_template_contract.py` renders both on every CI
build.

The mechanism is kept even though its first user is the model that lost. The
finding it encodes — that two distributors of the same base model ship
templates with different contract behaviour, and that this is invisible until
something sends a `developer` message — outlives the artifact.

### 4. Gemma is offered a 256k candidate, and it carries a reasoning budget

`gemma4-12b-qat-ud-q4xl-256k` points at the **same** artifact path, bytes and
SHA-256 as the public profile. No second copy of the weights exists, and the
only field that differed at measurement time was `context_tokens`.

262144 holds: 54.8% VRAM occupancy, 6.4 GiB unused, retention 59/60 up to
240,000 tokens, and quality at the larger window measured *better* than at
131072, not worse.

The campaign then found something the context question was not asking about.
Seven of Gemma's eight quality failures were `NO_ANSWER` — the whole output
budget spent inside `reasoning_content` — and they are present in the
2026-08-23 baseline too. Given the same `reasoning_budget: 6144` that fixed the
identical failure on Qwen3.6, all seven become answers and the profile goes
**26/34 → 31/34**. So the candidate declares that budget as well as the window.

The public 128k profile was deliberately **not** changed. The same field would
very likely do the same thing there; that is a recommendation for a separate
decision, not a change to make on a public model's behalf while measuring
something else.

### 5. Retiring a profile is not deleting its history

`qwen38-27b-huge-256k` moved to `state: retired` with no deployments *before*
its artifact was deleted. It keeps its id, its measurements and its
configuration; what it loses is the claim to be servable. A retired profile
naming a deleted file is the documented outcome. An active one naming a deleted
file is a broken manifest, which is why the deletion script refused the file
until the profile was retired first.

## Consequences

- **The huge-context profile changes shape.** It goes from a 27B dense model at
  2 bits to a 35B MoE with 3B active. Anything that assumed the Qwen3.8 family
  across all three long-context tiers no longer holds; `README.md` and
  `docs/RUNBOOK.md` are updated accordingly.
- **The MoE carries a reasoning budget.** `reasoning_budget: 6144` under an
  `n_predict: 16384` ceiling is not decoration: without it this model spends an
  entire 8,192-token budget thinking and returns an empty answer on the harder
  coding tasks, three times out of thirty-four. The budget is part of the
  profile in the same way the context window is.
- **One more template to maintain.** `config/chat-templates/` is a small
  surface, but it is a surface: a template checked in beside a model is a copy
  that can drift from the GGUF it was extracted from. The upstream baseline is
  checked in for exactly this reason, and the contract test asserts the baseline
  still fails the cases the patch exists to fix — if it ever stops failing,
  someone replaced the baseline and the no-regression check has been comparing a
  file to itself.
- **`function_calling` stays `false` on the new profile.** It scored 6/7 on the
  tool suite against llama-server's own endpoint. Per this repository's own
  rule that flag means a forced tool call demonstrated end to end through
  `internal/edge/namespace.go`, which has not been done, so the flag does not
  move. A benchmark score is not the evidence the flag names.
- **A grader defect was found and left in place.** `verify_missing_info` matches
  the substring `read` and fails a correct answer that says "provide" instead.
  It cost Ornith a point in this comparison. It is filed separately rather than
  fixed here, because changing a grader mid-comparison changes what every
  earlier campaign's report would say.

## Addendum: the public model moves to the 256k profile (2026-09-28)

The separate decision §4 deferred was taken by the operator on 2026-09-28, in a
different form than §4 suggested. Instead of adding the reasoning budget to the
128k profile, `provider.public_model` now resolves to
`gemma4-12b-qat-ud-q4xl-256k`, the profile that already carries it (26/34 →
31/34 with the budget, measured in §4). It is the same artifact, so no weights
moved. The 128k profile stays in the manifest as a `candidate`, unchanged.

What the switch costs, all of it measured: +1.9 GiB of VRAM for the larger
window (a 9.67 GiB peak against 7.8 in the manifest's `resources`); admission
now requires 26.9 GiB of available commit for the public
model — its 14.91 GiB peak, the 4 GiB reserve and the 8 GiB host prompt cache it
now declares (`docs/TUNING.md` §1.4) — where the 128k profile needs 17.5; and a
prompt that actually fills the window pays the time to first token recorded in
`docs/reports/FINAL-ROSTER-20260825.md`, 12.7 minutes at 240,000 tokens.

The harness templates in `integrations/` pin the new id. The installed canary
and any installed harness profile follow only after they are regenerated and
reinstalled (`New-V2Config.ps1 -Environment Canary -Apply`).
