# ADR 0012: Three Qwen3.8 profile classes, and an output contract that belongs to the model

## Status

Proposed. All three profiles are `candidate` in the `canary` deployment.
`provider.public_model` still resolves to `local-coding` and is not changed here.

## Context

`config/models.yaml` carried five `qwen38-27b-ws-*` entries that differed from
one another almost entirely by context window: 8k, 32k, 32k with a cheaper cache,
64k, 128k. That is the wrong axis to build a profile on. It answers "how much can
this hold" and never answers "what is this for", so an operator choosing between
them was choosing a number rather than a behaviour, and five of them existed
because each measurement produced one.

Three measurement campaigns then supplied what the profiles were missing.
`REPORT-qwen38-27b-gfx1201-20260821` established the weight split and that full
GPU residency is the worst configuration on this adapter.
`REPORT-workstation-memory-20260821` established that the budget available to a
model is the card minus whatever the desktop holds, and shipped the live adapter
probe. `REPORT-qwen38-27b-q3-q2-kvq4-20260821` added the axis both had left open
— weight quantization — and produced three results that decide this ADR:

- **Q3_K_XL matched the IQ4_XS control on coding quality** (9/10 each on a suite
  graded by real compilers and test runners, 4/4 tool calling each), while
  decoding 44% faster and prefilling 2.9x faster below 32k at the same cache
  precision.
- **Prefill collapses above roughly 96% adapter occupancy**, and the three
  quantizations sit on different sides of that threshold rather than on one
  curve. IQ4_XS sits *on* it here, which makes its prefill throughput depend on
  what else is on screen: the same GGUF with the same flags measured 956 t/s and
  281 t/s at pp512 in two runs that differed only in desktop VRAM.
- **Q2_K_XL produced no incorrect code at all**, preserved tool calling and
  constraint adherence, and still failed three of ten tasks — every one by
  spending its whole 8192-token output allowance inside `reasoning_content` and
  never beginning the answer.

The third result is the one that could not be expressed at all. The manifest had
`max_output_tokens` as advertising for the catalogs and nothing that bounded the
server, no way to separate thinking from answering, and no per-model compaction
point. The Codex profile TOMLs meanwhile pinned `model_context_window = 131072`
and `model_auto_compact_token_limit = 110000` *globally*, so a 32k profile
inherited a 128k window and a 110k compaction threshold, and a 256k profile was
silently capped at 128k.

## Decision

**1. Three named classes, chosen by purpose rather than by size.**

| id | Weights | KV | Context | Output | For |
|---|---|---|---:|---:|---|
| `qwen38-27b-deep-32k` | UD-IQ4_XS | `q8_0`/`q8_0` | 32768 | 8192 | Hardest localized work |
| `qwen38-27b-agent-128k` | UD-Q3_K_XL | `q4_0`/`q4_0` | 131072 | 8192 | **Daily default** |
| `qwen38-27b-huge-256k` | UD-Q2_K_XL | `q4_0`/`q4_0` | 262144 | 32768 | Huge active context |

Selection is explicit, by model id. No semantic router is introduced, because
none exists and inventing one would put a guess in the request path.

**Agent is the daily default, not Deep.** A coding harness spends tens of
thousands of tokens on system prompt, tool definitions, file contents, logs and
history before the operator's problem arrives; 32k is opt-in for localized work.

**Huge is selected on working set, not on difficulty.** A hard, localized problem
is a Deep problem. Only a problem that is enormous *in context* is a Huge one.

**2. The output contract is the model's, and the server enforces it.**
Four optional manifest fields, all additive, all omitted by every pre-existing
model so their generated command lines stay byte-identical — the ADR 0009 §3
invariant that `Test-V2ConfigGeneration.ps1` asserts in CI, still 6 byte-stable
models after this change:

| Field | Flag | Meaning |
|---|---|---|
| `n_predict` | `--n-predict` | Hard ceiling a client cannot exceed by asking |
| `reasoning_budget` | `--reasoning-budget` | Tokens the model may spend thinking |
| `reasoning_budget_message` | `--reasoning-budget-message` | Injected to force the answer to begin |
| `compact_threshold_tokens` | *(none)* | Harness contract; consumed by the catalog generator |

`--n-predict`, `--reasoning-budget` and `--reasoning-budget-message` were
verified present in `llama-server --help` on b10549 before the fields were
added. Nothing here invents a parameter.

**Rejected alternative:** emitting `--n-predict` from the existing
`max_output_tokens` for every model. It is one fewer field, and it would have
changed the command line of all six existing models, invalidating their
qualification for a change none of them asked for.

**3. Only the Huge profile gets a thinking budget.** Huge declares
`n_predict: 32768` with `reasoning_budget: 24576`, leaving roughly 8k for the
answer. Deep and Agent keep an 8192 ceiling with reasoning unrestricted, which is
the configuration each was measured under; raising every profile to 32k because
one needs it would change three things to fix one, and `docs/MODEL_PROMOTION.md`
treats a serving-flag change as invalidating qualification.

Long reasoning on Huge is explicitly **not** a failure. Reaching 32768 without a
useful answer is recorded as an operational result — never silently raised to
64k, never retried indefinitely.

**4. Compaction derives from the model, not from a global constant.**
`compact_threshold_tokens = context_tokens − max_output_tokens − safety margin`:

| Profile | Context | Output | Threshold | Codex percent |
|---|---:|---:|---:|---:|
| Deep | 32768 | 8192 | 23552 | 71 |
| Agent | 131072 | 8192 | 114688 | 87 |
| Huge | 262144 | 32768 | 221184 | 84 |

`New-V2ClientCatalogs.ps1` converts the token figure into the percentage Codex
expects, flooring rather than rounding so the threshold never lands above the
declared value. Models that declare no threshold keep the previous flat 85%, so
this change is confined to profiles that opted into an explicit reserve.

The two global keys are deleted from both Codex profile TOMLs. **Codex's actual
precedence between a global override and a catalog entry could not be
demonstrated on this machine** — the Codex CLI is not installed here, only the
desktop application — so the keys are removed rather than reordered or relied
upon. Removing them cannot produce a disagreement; leaving them could.

**5. The five `ws-*` profiles are retired, not deleted.** `state: retired` with
no deployments: they leave the operational surface and the generated catalogs
while remaining in the manifest, because the benchmark reports reference them by
id and deleting them would orphan that evidence.

**6. The 4-block CPU split is unchanged.** §8.1 of the KV campaign identifies a
5- or 6-block split as the most promising untested lever for keeping Q3_K_XL
below the occupancy threshold at 32k. It is `NOT MEASURED`. Consolidating onto an
unmeasured hypothesis is the exact failure mode the occupancy finding is about,
so the shipped split is the one that was actually compared, and the sweep is
recorded as `qwen38-27b-q3-placement-sweep` in `model-test-matrix.json`.

## Consequences

`DefaultHeaderTimeout` moves from 1800s to 3600s. It bounds the wait for
*response headers*, which streaming requests receive immediately, so it binds
only non-streaming ones — where llama-server buffers the whole completion and the
wait is the entire generation. 32768 tokens at the 16.04 t/s measured for a
saturated adapter is 2043s, and a long prefill is charged on top; the old value
would have turned a working Huge generation into a timeout that reads as a hang.
**This is a real trade:** the edge has no per-model timeout, so a genuinely hung
upstream now takes an hour to surface on a non-streaming request. Per-profile
timeouts are the correct fix and are a follow-up, not something improvised here.

`/api/v1/status` gains a `profile` block per model — weights, cache types, output
ceiling, reasoning budget, compaction threshold. Context alone cannot tell the
three classes apart: Deep and Agent differ by weights and cache precision, Agent
and Huge by output and reasoning budget. It is observability only and is excluded
from `/v1/models`, so the public model list is unchanged.

Qualification is deliberately uneven and is recorded that way. **Deep** inherits
the IQ4_XS/`q8_0` qualification whose configuration it reproduces exactly.
**Agent** is the recommended daily profile with long-context retention still
pending. **Huge** is an experimental huge-context profile whose 256k memory
envelope is measured but whose retention and filled-context decode are not.
Presenting the last two as fully qualified would be false, and the campaign that
produced them was paused before the evidence existed.

Nothing here weakens loopback-only binding, the absence of cloud fallback,
credential isolation, the route and model allowlists, `max_loaded_models: 1`, or
admission control. The server remains stateless; compaction stays the harness's
responsibility, and this ADR only makes the contract it compacts against correct.
