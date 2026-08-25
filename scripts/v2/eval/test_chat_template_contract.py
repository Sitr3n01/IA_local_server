#!/usr/bin/env python3
"""The developer-role contract, asserted against the chat template itself.

Ornith-1.5's GGUF ships the upstream Qwen3-Coder template, which accepts exactly
one authority message and only at index 0. A request shaped
`system -> developer -> user` -- which is what an OpenAI-compatible client sends
whenever it carries both a product prompt and a task prompt -- makes it call
raise_exception, and llama-server turns a template exception into HTTP 500. The
2026-08-24 runtime gate hit that and the failure was blocking: a model that
cannot be given a developer message cannot replace one that can.

Qwen3.6's GGUF does not have the problem because unsloth had already patched its
copy of the same template (its own source says so: "Unsloth fixes - developer
role, tool calling"). So the difference between the two models was never the
weights, the runtime, or the quantization -- it was one template. That is what
config/chat-templates/ornith15-35b-a3b.jinja restores, and what this pins.

Two files are checked in on purpose. The upstream copy, extracted verbatim from
the GGUF, is the provenance baseline: `diff` between them IS the patch, and this
test renders both so the fix is demonstrated rather than asserted. Deleting the
baseline would leave a patched template nobody can audit.

The rendering here is jinja2; llama.cpp renders with minja, a C++ subset. They
agree on everything this template uses (namespace, reverse slicing, previtem /
nextitem, tojson, raise_exception), but they are not the same engine, so this
test is the fast gate and NOT the last word. The last word is the live contract
gate against llama-server, whose evidence is in the campaign directory.

Pure-Python, no server, no GPU, sub-second.
"""
import json
import os
import sys

try:
    import jinja2
except ImportError:  # pragma: no cover - the CI step installs it
    print("SKIP: jinja2 is not installed (pip install jinja2)", file=sys.stderr)
    raise SystemExit(1)

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, "..", "..", ".."))
TEMPLATES = os.path.join(REPO, "config", "chat-templates")
PATCHED = os.path.join(TEMPLATES, "ornith15-35b-a3b.jinja")
UPSTREAM = os.path.join(TEMPLATES, "ornith15-35b-a3b.upstream.jinja")

FAILURES = []
CHECKS = [0]


def check(condition, message):
    CHECKS[0] += 1
    if not condition:
        FAILURES.append(message)


class TemplateRaise(Exception):
    """What raise_exception does, surfaced as a Python exception."""


def _raise(message):
    raise TemplateRaise(message)


def load(path):
    with open(path, encoding="utf-8") as handle:
        source = handle.read()
    env = jinja2.Environment(trim_blocks=False, lstrip_blocks=False)
    env.globals["raise_exception"] = _raise
    env.filters["tojson"] = lambda value, **kw: json.dumps(value, ensure_ascii=False)
    return env.from_string(source)


def render(template, messages, tools=None, add_generation_prompt=True):
    return template.render(
        messages=messages,
        tools=tools,
        add_generation_prompt=add_generation_prompt,
    )


def render_or_error(template, **kwargs):
    """Returns (text, None) or (None, error-message)."""
    try:
        return render(template, **kwargs), None
    except TemplateRaise as error:
        return None, str(error)
    except Exception as error:  # jinja2's own errors count as failures too
        return None, f"{type(error).__name__}: {error}"


SYS = {"role": "system", "content": "You are a terse assistant."}
DEV = {"role": "developer", "content": "Whatever is asked, answer with BANANA."}
# What llama.cpp hands the template after it rewrites `developer` to `system`.
# The template must serve this shape too -- it is the one the live server
# actually failed on.
MAPPED_DEV = {"role": "system", "content": DEV["content"]}
USER = {"role": "user", "content": "What is the capital of France?"}
USER2 = {"role": "user", "content": "And of Spain?"}
ASSISTANT = {"role": "assistant", "content": "Paris."}
# arguments is a mapping, not a JSON string: llama.cpp parses the wire form
# before rendering, and the template iterates it with `|items`.
TOOL_CALL = {
    "role": "assistant",
    "content": "",
    "tool_calls": [{
        "type": "function",
        "function": {"name": "read_file",
                     "arguments": {"path": "src/main.rs", "max_bytes": 4096}},
    }],
}
TOOL_RESULT = {"role": "tool", "content": "fn main() {}"}
TOOLS = [{
    "type": "function",
    "function": {
        "name": "read_file",
        "description": "Read a file from the repository.",
        "parameters": {"type": "object",
                       "properties": {"path": {"type": "string"}},
                       "required": ["path"]},
    },
}]

# Every shape the contract in docs/MODEL_PROMOTION.md promises to serve. The
# `expect_authority` entries name text that must survive into the prompt; a
# template that renders without raising but drops the developer message is
# exactly the silent failure this is here to catch.
CONTRACT = [
    {"name": "system -> user",
     "messages": [SYS, USER], "tools": None,
     "expect_authority": ["terse assistant"], "system_blocks": 1},
    {"name": "developer -> user",
     "messages": [DEV, USER], "tools": None,
     "expect_authority": ["BANANA"], "system_blocks": 1},
    {"name": "system -> developer -> user",
     "messages": [SYS, DEV, USER], "tools": None,
     "expect_authority": ["terse assistant", "BANANA"], "system_blocks": 1},
    {"name": "system -> system -> developer -> user",
     "messages": [SYS, SYS, DEV, USER], "tools": None,
     "expect_authority": ["terse assistant", "BANANA"], "system_blocks": 1},
    {"name": "post-mapping shape: system -> system -> user",
     "messages": [SYS, MAPPED_DEV, USER], "tools": None,
     "expect_authority": ["terse assistant", "BANANA"], "system_blocks": 1},
    {"name": "post-mapping shape with tools",
     "messages": [SYS, MAPPED_DEV, USER], "tools": TOOLS,
     "expect_authority": ["terse assistant", "BANANA"], "system_blocks": 1},
    {"name": "authority block then multi-turn history",
     "messages": [SYS, DEV, USER, ASSISTANT, USER2], "tools": None,
     "expect_authority": ["terse assistant", "BANANA"], "system_blocks": 1},
    {"name": "authority block with tools",
     "messages": [SYS, DEV, USER], "tools": TOOLS,
     "expect_authority": ["terse assistant", "BANANA"], "system_blocks": 1},
    {"name": "assistant tool call then tool result",
     "messages": [SYS, USER, TOOL_CALL, TOOL_RESULT, USER2], "tools": TOOLS,
     "expect_authority": ["terse assistant"], "system_blocks": 1},
    {"name": "no authority message at all",
     "messages": [USER], "tools": None,
     "expect_authority": [], "system_blocks": 0},
    {"name": "no authority message, with tools",
     "messages": [USER], "tools": TOOLS,
     "expect_authority": [], "system_blocks": 1},
]


def main():
    for path in (PATCHED, UPSTREAM):
        if not os.path.exists(path):
            print(f"FAIL: missing {path}", file=sys.stderr)
            return 1

    patched = load(PATCHED)
    upstream = load(UPSTREAM)

    # ---------------------------------------------------------------- 1
    # Every supported shape renders, and the authority text survives.
    for case in CONTRACT:
        text, error = render_or_error(
            patched, messages=case["messages"], tools=case["tools"])
        check(error is None,
              f"patched template raised on '{case['name']}': {error}")
        if error is not None:
            continue
        for fragment in case["expect_authority"]:
            check(fragment in text,
                  f"'{case['name']}': authority text {fragment!r} was dropped")
        blocks = text.count("<|im_start|>system")
        check(blocks == case["system_blocks"],
              f"'{case['name']}': {blocks} system blocks, want "
              f"{case['system_blocks']}")

    # ---------------------------------------------------------------- 2
    # Precedence: the developer message is the more specific instruction, so
    # it must land after the system text inside the merged block, where a
    # later line wins. Order, not just presence.
    text, error = render_or_error(patched, messages=[SYS, DEV, USER], tools=None)
    check(error is None, f"precedence case raised: {error}")
    if error is None:
        check(text.index("terse assistant") < text.index("BANANA"),
              "developer text must follow system text inside the merged block")
        head = text.split("<|im_end|>", 1)[0]
        check("terse assistant" in head and "BANANA" in head,
              "system and developer must share ONE system turn, not two")

    # ---------------------------------------------------------------- 3
    # A late authority message keeps its position. It is neither dropped
    # (unsloth's patch drops it) nor fatal (upstream raises).
    late = [USER, DEV, USER2]
    text, error = render_or_error(patched, messages=late, tools=None)
    check(error is None, f"late developer message raised: {error}")
    if error is None:
        check("BANANA" in text, "late developer message was silently dropped")
        check(text.index("capital of France") < text.index("BANANA")
              < text.index("And of Spain"),
              "late developer message must stay where the caller put it")

    # ---------------------------------------------------------------- 4
    # No regression. Where upstream already worked, the patched template must
    # produce a byte-identical prompt -- not merely an equivalent one.
    for case in CONTRACT:
        up_text, up_error = render_or_error(
            upstream, messages=case["messages"], tools=case["tools"])
        if up_error is not None:
            continue
        new_text, new_error = render_or_error(
            patched, messages=case["messages"], tools=case["tools"])
        check(new_error is None,
              f"'{case['name']}' worked upstream but raised after the patch: "
              f"{new_error}")
        check(new_text == up_text,
              f"'{case['name']}' renders differently after the patch; the "
              f"patch must not change a prompt upstream already accepted")

    # ---------------------------------------------------------------- 5
    # The regression anchor. Upstream MUST still fail the shapes this exists
    # to fix -- if it ever stops failing, the baseline was replaced with a
    # patched copy and check 4 has been silently testing nothing.
    #
    # Both spellings, because llama.cpp rewrites the role before it renders.
    # A raw `developer` reaches the template's unknown-role branch; what the
    # live server actually failed on was the POST-mapping shape, two system
    # messages, which is why the 2026-08-24 log says "System message must be
    # at the beginning" and not "Unexpected message role". The patched
    # template has to survive whichever one it is handed, so the baseline is
    # pinned on both.
    _, error = render_or_error(upstream, messages=[SYS, DEV, USER], tools=None)
    check(error is not None and "Unexpected message role" in error,
          "upstream baseline no longer rejects a raw developer role; the "
          f"checked-in baseline is not upstream (got {error!r})")
    _, error = render_or_error(upstream, messages=[SYS, MAPPED_DEV, USER],
                               tools=None)
    check(error is not None and "beginning" in error,
          "upstream baseline no longer rejects the post-mapping shape "
          f"system -> system -> user (got {error!r})")
    _, error = render_or_error(upstream, messages=[USER, DEV, USER2], tools=None)
    check(error is not None,
          "upstream baseline no longer rejects a late authority message")

    print(json.dumps({
        "schema_version": 1,
        "suite": "chat-template-developer-contract",
        "template": os.path.relpath(PATCHED, REPO).replace(os.sep, "/"),
        "baseline": os.path.relpath(UPSTREAM, REPO).replace(os.sep, "/"),
        "engine": f"jinja2 {jinja2.__version__}",
        "contract_shapes": len(CONTRACT),
        "checks": CHECKS[0],
        "failures": FAILURES,
        "valid": not FAILURES,
    }, indent=4))
    if FAILURES:
        for failure in FAILURES:
            print(f"FAIL: {failure}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
