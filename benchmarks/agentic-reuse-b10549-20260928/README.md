# Agentic context reuse on b10549 — Gates B, C and D, 2026-09-28

**Question.** `docs/TUNING.md` §1.4 and ADRs 0009/0010 recorded that context
checkpoints do not restore on hybrid (Gated DeltaNet) models upstream, so every
agentic turn would re-prefill the whole conversation, and they adopted the buun
fork for that reason. llama.cpp #22384 was closed as completed on 2026-04-26
(#24055 is still open). Does the pinned `amd-rocm-qwen38` runtime (b10549, commit
`b2e5e9b28`, SHA-256 `C8B1E5A6…2359`) reuse a live session's context?

**Answer: yes, on both hybrid profiles, as shipped, from ~55k to ~257k tokens.**
Six cells, six `incremental_reuse_pass`, zero full re-prefills.

## Method

`Measure-V2AgenticReuse.ps1` unchanged: a synthetic seeded base context, then
+2k-token increments alternating with tool turns, six turns, `max_tokens` 128,
temperature 0, fixture seed 20260817; a turn fails when it processes more than half
the conversation. `Test-V2AgenticHarness.ps1` passed first (7 tests: the gate
passes a reusing server and fails a re-prefilling one).

`Run-AgenticReuse.ps1` starts the manifest entry through
`New-V2LlamaServerArguments` — the production builder, minus the router key and
`--log-disable` — so every cell measures what the canary serves, **including the
defaults the manifest leaves unpinned**: `--cache-ram 8192`, `--cache-idle-slots`,
`--ctx-checkpoints 32`, `--checkpoint-min-step 8192`. Exact command lines:
`command-*.txt`. The canary router was running with no model loaded; nothing it
owned was touched.

Base sizes are requested in approximate tokens and measured, not assumed:
60,000 requested gave a 55.2k-token first prompt, 130,000 gave 119.1k, 200,000
gave 183.1k and 272,000 gave 248.9k. Gate C therefore reached 126.7k on Agent (its
window is 131,072) and 190.8k on Huge; Gate D peaked at 256,593 of Huge's 262,144.

The Gate D cell ran after the four defaults were declared in `config/models.yaml`
(same values), so its command line carries `--cache-ram 8192 --ctx-checkpoints 32
--checkpoint-min-step 8192 --cache-idle-slots` explicitly
(`command-huge256k-gated-256k.txt`); the earlier cells inherited them.

## Gate B (~55-63k)

`qwen38-27b-agent-128k` (Qwen3.8-27B UD-Q3_K_XL, dense hybrid) — pass

| Turn | Kind | Context | Processed | From cache | Turn latency |
|---:|---|---:|---:|---:|---:|
| 1 | cold prefill | 55,189 | 55,189 | 0 | 302.7 s |
| 2 | tool result | 55,196 | 11 | 55,185 | 4.0 s |
| 3 | user increment | 58,915 | 3,723 | 55,192 | 42.5 s |
| 4 | tool result | 58,910 | 287 | 58,623 | 17.7 s |
| 5 | user increment | 62,722 | 3,816 | 58,906 | 38.9 s |
| 6 | tool result | 62,748 | 30 | 62,718 | 16.1 s |

`qwen36-35b-a3b-huge-256k` (Qwen3.6-35B-A3B UD-Q2_K_XL, MoE hybrid, `n_cpu_moe` 8) — pass

| Turn | Kind | Context | Processed | From cache | Turn latency |
|---:|---|---:|---:|---:|---:|
| 1 | cold prefill | 55,151 | 55,151 | 0 | 170.0 s |
| 2 | tool result | 55,146 | 287 | 54,859 | 5.1 s |
| 3 | user increment | 58,959 | 3,817 | 55,142 | 17.4 s |
| 4 | tool result | 58,954 | 287 | 58,667 | 4.6 s |
| 5 | user increment | 62,814 | 3,864 | 58,950 | 17.8 s |
| 6 | tool result | 62,809 | 287 | 62,522 | 4.5 s |

## Gate C (~119-191k)

`qwen38-27b-agent-128k` at ~120k — pass

| Turn | Kind | Context | Processed | From cache | Turn latency |
|---:|---|---:|---:|---:|---:|
| 1 | cold prefill | 119,173 | 119,173 | 0 | 943.1 s |
| 2 | tool result | 119,188 | 19 | 119,169 | 9.6 s |
| 3 | user increment | 122,907 | 3,723 | 119,184 | 67.3 s |
| 4 | tool result | 122,922 | 19 | 122,903 | 6.7 s |
| 5 | user increment | 126,687 | 3,769 | 122,918 | 63.4 s |
| 6 | tool result | 126,722 | 39 | 126,683 | 13.0 s |

`qwen36-35b-a3b-huge-256k` at ~120k — pass

| Turn | Kind | Context | Processed | From cache | Turn latency |
|---:|---|---:|---:|---:|---:|
| 1 | cold prefill | 119,135 | 119,135 | 0 | 437.5 s |
| 2 | tool result | 119,130 | 287 | 118,843 | 6.0 s |
| 3 | user increment | 122,859 | 3,733 | 119,126 | 22.8 s |
| 4 | tool result | 122,854 | 287 | 122,567 | 5.6 s |
| 5 | user increment | 126,714 | 3,864 | 122,850 | 23.7 s |
| 6 | tool result | 126,709 | 287 | 126,422 | 5.6 s |

`qwen36-35b-a3b-huge-256k` at ~183k — pass

| Turn | Kind | Context | Processed | From cache | Turn latency |
|---:|---|---:|---:|---:|---:|
| 1 | cold prefill | 183,119 | 183,119 | 0 | 799.8 s |
| 2 | tool result | 183,114 | 287 | 182,827 | 7.3 s |
| 3 | user increment | 186,927 | 3,817 | 183,110 | 29.4 s |
| 4 | tool result | 186,922 | 287 | 186,635 | 6.8 s |
| 5 | user increment | 190,780 | 3,862 | 186,918 | 30.0 s |
| 6 | tool result | 190,775 | 287 | 190,488 | 6.8 s |

## Gate D (~249-257k)

`qwen36-35b-a3b-huge-256k` at ~249k, window 262,144 — pass

| Turn | Kind | Context | Processed | From cache | Turn latency |
|---:|---|---:|---:|---:|---:|
| 1 | cold prefill | 248,931 | 248,931 | 0 | 1,358.7 s |
| 2 | tool result | 248,926 | 287 | 248,639 | 7.9 s |
| 3 | user increment | 252,739 | 3,817 | 248,922 | 37.6 s |
| 4 | tool result | 252,734 | 287 | 252,447 | 8.1 s |
| 5 | user increment | 256,593 | 3,863 | 252,730 | 38.7 s |
| 6 | tool result | 256,588 | 287 | 256,301 | 8.2 s |

Server logs: zero `forcing full prompt re-processing` in every cell; the slot was
selected by LCP similarity on every turn after the first (`checkpoint-lines-*.txt`).

Gate C's other criteria: memory stable (below), no runtime fallback (load times
6.5-12.6 s, decode steady within each cell). MTP health does not apply — neither
profile declares `spec_decoding`.

## Reading

- **This is checkpoint restoration, not only prefix extension.** On tool turns the
  conversation usually *shrinks* (the harness drops reasoning from history), so the
  new prompt diverges inside the cached sequence. A recurrent state cannot be rewound
  without a checkpoint, yet `cache_n` lands *before* the previous prompt's end. On
  Huge it lands exactly 287-292 tokens before it — one `ubatch` of 288 — at 55k, 119k,
  183k and 249k alike: the server keeps a checkpoint ahead of the prompt's last
  micro-batch, so a divergence costs about one `ubatch` regardless of depth. When
  the model did emit a real tool call (Agent at Gate C, 3 of 3 tool turns), the
  history grew instead and the turn processed 19-39 tokens.
- **What reuse is worth here** is the cold column against the rest: a turn that
  would cost the full prefill (170 s to 1,359 s) cost 4.5-67 s instead.
- **Memory is flat with depth and turns.** Process private memory moved +0.13 to
  +0.30 GiB across six turns in every cell; Huge peaks at 10.47 GiB private at
  127k, 191k and 257k alike. The unpinned 8 GiB host prompt cache did not fill: a continuing
  session reuses the live slot. Its ceiling is reachable only when conversations
  alternate (idle-slot saves), which this scenario does not exercise.
- b10549 does not log checkpoint creation or restoration at default verbosity; the
  evidence is the server's own `cache_n`/`prompt_n` counters, which is what the gate
  was designed to read anyway.
- `mean_turn_efficiency` (0.39) is a scoring artifact, not a finding: on turns where
  the context shrank "new tokens" computes to 0, so efficiency reads 0 while only
  11-287 tokens were processed. The verdict does not use it.

## Not established

- History rewrites deeper than one `ubatch` (harness compaction, an edited earlier
  message): those depend on `--ctx-checkpoints`/`--checkpoint-min-step` spacing.
- Reuse across alternating conversations (the host prompt cache), and its memory cost.
- `ornith15-35b-a3b-fast-128k` (same architecture as Huge, not run) and
  `qwen38-27b-deep-32k` (its 32k window cannot hold a 60k base).
- Tool-call realism: with `max_tokens` 128, Agent produced a real tool call on 1 of
  3 tool turns at Gate B and 3 of 3 at Gate C; Huge on none (it spends the cap
  reasoning), so its tool turns exercised the reasoning-drop divergence rather than
  tool-call history. Both shapes of history change were covered, but not by both
  models.
- Any runtime other than b10549. Re-run this before adopting a new build.

## Side observations

- The Agent profile runs close to the VRAM cliff with this desktop (3.7-3.9 GiB
  dedicated before load): 94-96% dedicated occupancy and 3.6-3.7 GiB shared in every
  cell. Prefill 188 t/s at 55k and 128 t/s at 119k; decode 8.5-9.5 t/s at 55-63k and
  5.3-5.6 t/s at 119-127k. Huge stays at ~92% and 1.3-1.6 GiB shared: prefill 330 t/s
  at 55k, 275 t/s at 119k, 230 t/s at 183k, 184 t/s at 249k; decode 26-44 t/s up to
  191k and 25.6-27.4 t/s at 249-257k.
- Free physical RAM bottomed at 2.2 GiB during the Gate B Agent cell, 3.7 GiB during
  Gate C Agent, 4.7 GiB during Gate B Huge and 6.5-6.7 GiB during the Gate C and D
  Huge cells.

## Files

`command-*.txt` exact server command · `reuse-*.json` gate report ·
`summary-*.json` condensed turns + log counts · `server-*.log.err` server log ·
`driver-*.log` driver log · `Run-AgenticReuse.ps1` the driver (`-SummarizeOnly`
rebuilds a summary from these files). The first Agent cell lost its driver-log tail
to a file lock from a log tailer; its summary was rebuilt with `-SummarizeOnly`.
