# Chat template overrides

A GGUF carries its own chat template in `tokenizer.chat_template`, and
`llama-server --jinja` uses it. That is the right default and stays the default:
nothing here applies to a model unless its manifest entry sets
`chat_template_file`.

This directory exists for the case where an artifact's embedded template is
wrong for the contract *this* server serves, rather than wrong in general. It
is empty today: every model in the roster uses the template inside its GGUF.

## Why an override at all

The OpenAI-shaped surface this project exposes has three authority roles:
`system`, `developer`, and their combination. A coding harness produces the
combination routinely - a product prompt as `system`, a task prompt as
`developer` - so a model that cannot be given both cannot be an agent model
here.

`internal/edge/chat.go` already coalesces the leading authority block before
anything reaches a runtime, so the edge never had this problem. But the
qualification harness measures against a direct `llama-server`, on purpose:
that is the layer where a model's real template behaviour is visible, and the
edge would hide exactly the defect worth finding.

## Adding one

1. Extract the embedded template and check it in as `<model>.upstream.jinja`.
2. Patch a copy as `<model>.jinja`; keep the diff small enough to read, so the
   patch is reviewable against the verbatim baseline.
3. Add a contract test under `scripts/v2/eval/` that renders both and pins the
   message shapes the patch exists to fix, plus byte-identical output for every
   shape the upstream template already accepted. Run it in CI.
4. Set `chat_template_file` on the manifest entry. It requires `jinja: true`;
   `New-V2LlamaServerArguments` throws otherwise, because `llama-server` would
   accept the flag and ignore the file.
