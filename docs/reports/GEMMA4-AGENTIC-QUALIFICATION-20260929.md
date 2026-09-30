# Gemma 4 12B Agentic (Q4_K_M) at 256k - 2026-09-29

`gemma4-12b-agentic-q4km-256k`: yuxinlu1's agentic fine-tune of google/gemma-4-12B-it,
`gemma4-v2-Q4_K_M.gguf`, revision `190a31365a6b`, SHA-256 `0B9506CA...6791` (equal to
Hugging Face's LFS hash). Same runtime (`unsloth-runtime-candidate`, b10225), KV
`q4_0`/`q4_0`, ubatch 512 and request ceiling 8192 as the QAT 256k campaign of
2026-08-25, so the two are comparable. Evidence: `benchmarks/campaign-gemma4-agentic-20260929/`.

## What the model card claims, and what it does not

The card recommends a 16,384-token window and states no training sequence length.
The GGUF header declares `n_ctx_train` 262144, inherited from the base. Its tau2-bench
evidence is telecom only, 20 of 114 tasks, at Q8_0 in the author's own harness; the
telecom domain is a customer-support dialogue with API calls, not repository work.

## Admission

262144 is admissible: peak dedicated VRAM 13,794 MiB with 2,510 MiB headroom, shared
delta 354 MiB, no pressure. The idle baseline was 4.2 GiB because the desktop held that
much; the model's own cost is `marginal_vram_mib` 9,846.

Manifest resources (same convention as the QAT entry): `peak_vram_gib` 9.62,
`peak_commit_gib` 13.12, `peak_ram_gib` 11.17 (QAT: 9.67 / 14.91 / 9.94).

## Retention (12 probes per level)

| Prompt tokens | Agentic | QAT |
|---:|---:|---:|
| 8k | 1.000 | - |
| 16k | 0.958 | - |
| 32k | 0.958 | 1.000 |
| 65k | 0.958 | 1.000 |
| 120k | 0.958 | 1.000 |
| 196k | 0.958 | 0.917 |
| 240k | 0.875 (1 hallucinated needle) | 1.000 |

The 16k recommendation is not a retention wall: needles, entities, negative memory and
mutable state hold to 196k. The one recurring miss is the `constraint` probe at 0.5 from
16k upward. Time to first token at 240k is 756 s, the same as the QAT (764 s).

## Quality (33 cases, 8192-token ceiling, no reasoning budget)

| Suite | Agentic | QAT (2026-08-25) |
|---|---:|---:|
| chat | 4/4 | 4/4 |
| coding | 9/10 | 6/10 |
| hard | 6/8 | 4/8 |
| tools | 6/7 | 7/7 |
| literal_tools | 2/4 | 4/4 |
| json | pass | pass |
| **total** | **27/33** | **25/33** |

The QAT column is the run with no reasoning budget. With the `reasoning_budget` the
manifest now declares, the QAT scored 31/34 (CHANGELOG, ADR 0016 addendum), so the
agentic model's lead is not established against the shipped QAT profile.

It thinks much less: 357 reasoning characters against 3,122 on the same bug-fix case, and
the whole suite took 3 minutes against 25. That is the property that makes it fast in a
tool loop.

## The defect that matters for agents

All three tool failures are the same class, and the class is the one that disqualified the
Ornith Fast profile: the model rewrites literal argument values instead of copying them.
`**/*.go` and `*.go` are swapped; `Assets/Scripts/**/*.cs` becomes `Assets/Scripts/*.cs`;
and `include`/`exclude` arrive as strings where a list was required. Each returns a
well-formed call with a subtly different search. `capabilities.function_calling` stays
`false`.

## Decision

Rejected. The retention numbers would justify a 256k profile, but not a role in a tool
loop, and its lead over the QAT does not survive the QAT's reasoning budget (31/34). The
manifest entry, the client-catalog entries and the 6.9 GiB GGUF were removed on
2026-09-29. Revisit only if argument fidelity is fixed, or a harness validates tool
arguments against the request. To reinstall: the repository, revision and SHA-256 above
are enough to reproduce the file and the campaign driver reruns the measurements.
