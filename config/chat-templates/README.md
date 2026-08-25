# Chat template overrides

A GGUF carries its own chat template in `tokenizer.chat_template`, and
`llama-server --jinja` uses it. That is the right default and stays the default:
nothing here applies to a model unless its manifest entry sets
`chat_template_file`.

This directory exists for the case where an artifact's embedded template is
wrong for the contract *this* server serves, rather than wrong in general.

## Why an override at all

The OpenAI-shaped surface this project exposes has three authority roles:
`system`, `developer`, and their combination. A coding harness produces the
combination routinely — a product prompt as `system`, a task prompt as
`developer` — so a model that cannot be given both cannot be an agent model
here.

`internal/edge/chat.go` already coalesces the leading authority block before
anything reaches a runtime, so the edge never had this problem. But the
qualification harness measures against a direct `llama-server`, on purpose:
that is the layer where a model's real template behaviour is visible, and the
edge would hide exactly the defect worth finding.

## Ornith-1.5-35B-A3B

Two files, both checked in:

| file | what it is |
| --- | --- |
| `ornith15-35b-a3b.upstream.jinja` | extracted verbatim from the GGUF; the provenance baseline |
| `ornith15-35b-a3b.jinja` | the one the server is given |

`diff` between them is the entire patch. Keeping the baseline is what makes the
patch reviewable and what lets
`scripts/v2/eval/test_chat_template_contract.py` prove the fix by rendering
both rather than asserting it in prose.

**The defect.** Upstream accepts exactly one authority message, and only at
index 0. Any other arrangement calls `raise_exception`, and `llama-server`
turns a template exception into HTTP 500. Measured on 2026-08-24:

```
POST /v1/chat/completions   [system, developer, user]
500  Jinja Exception: System message must be at the beginning.
```

llama.cpp rewrites `developer` to `system` before rendering, which is why the
message names a system message the caller never sent. Send `developer` to the
unpatched template directly and it fails differently — `Unexpected message
role.` — so both spellings are pinned in the test.

**The fix.** Walk the contiguous authority block at the head of the
conversation and merge it into the single system turn the model was trained on,
joined by a blank line — the same rule, and the same separator, as
`normalizeChatAuthorityMessages` in `internal/edge/chat.go`, so the edge and a
direct port compose the same prompt from the same request. An authority message
that arrives *after* conversation content is emitted in place as its own
`<|im_start|>system` turn: well-formed ChatML, position preserved.

**This is not novel.** Qwen3.6-35B-A3B never had the problem because unsloth had
already patched its copy of the same upstream template — its own source says so:
`{#- Unsloth fixes - developer role, tool calling #}`. bartowski's Ornith build
ships the template unmodified. So the entire difference between the two models
on this axis was one template, not the weights, the runtime, or the
quantization. The patch here is that same repair, applied the same way, plus two
corrections:

- unsloth handles at most **two** leading authority messages; this handles the
  whole block.
- unsloth filters every `system`/`developer` message out of the render loop, so
  a late one is **silently dropped**. Dropping an instruction is worse than
  either raising or honouring it, so this emits it in place instead.

## What is guaranteed

`scripts/v2/eval/test_chat_template_contract.py` runs on every CI build. It
renders eleven message shapes through both templates and asserts, among other
things, that:

- every shape in the contract renders without raising;
- the developer text survives into the prompt and lands *after* the system text,
  where a later instruction wins;
- system and developer share **one** system turn, not two;
- a late authority message is neither dropped nor fatal;
- for every shape upstream already accepted, the patched template produces a
  **byte-identical** prompt. This is the no-regression guarantee, and it is also
  why evidence gathered before the patch stays valid for prompts that never
  used a developer message.
- the checked-in baseline still fails the shapes the patch exists to fix — if it
  ever stops failing, someone replaced the baseline with a patched copy and the
  byte-identical check has been comparing a file to itself.

The renderer there is Python `jinja2`; `llama.cpp` renders with `minja`, a C++
subset. They agree on everything this template uses, but they are not the same
engine, so that test is the fast gate and not the last word. The last word is
the live contract gate in
`benchmarks/campaign-final-moe-20260825/contract/`, which ran the 12 probes of
`edge_contract.py` against a real `llama-server` and recorded, in the same
directory, a control run with the embedded template that still returns 500.

## Adding another one

1. Extract the embedded template and check it in as `<model>.upstream.jinja`.
2. Patch a copy as `<model>.jinja`; keep the diff small enough to read.
3. Add the model's shapes to `test_chat_template_contract.py`.
4. Set `chat_template_file` on the manifest entry. It requires `jinja: true`;
   `New-V2LlamaServerArguments` throws otherwise, because `llama-server` would
   accept the flag and ignore the file.
