#!/usr/bin/env python3
"""Self-test for the GGUF tensor classifier.

The census answers "where are the bytes, and which of them can leave the GPU
cheaply", and every MoE placement decision in the campaign reports is read off
it. A misclassification is therefore not a cosmetic problem: it moves bytes
between the category that must stay resident and the category that may be
offloaded.

Two properties are checked.

1. A hybrid's recurrent layers are separated from its full-attention layers.
   Both name their projections ".attn_*", so the split has to come from the
   layer's structure rather than from the tensor's name.
2. Adding that split changed nothing for a non-hybrid MoE. Gemma 4 26B A4B is
   the control, replayed from the census committed by its own campaign.

Run: python test_gguf_probe.py [repo-root]
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from gguf_probe import tensor_category, tensor_census  # noqa: E402


def _fake(names):
    """Build the minimum shape tensor_census needs from a list of names."""
    return {"tensors": [{"name": n, "dims": [1], "type": "F32", "offset": i * 32}
                        for i, n in enumerate(names)]}


# One recurrent block and one full-attention block, named the way llama.cpp
# names qwen35moe: the recurrent block carries ssm_* tensors and reuses
# attn_qkv/attn_gate for its input projections.
HYBRID = [
    "token_embd.weight",
    "blk.0.attn_norm.weight",
    "blk.0.attn_qkv.weight",
    "blk.0.attn_gate.weight",
    "blk.0.ssm_conv1d.weight",
    "blk.0.ssm_a",
    "blk.0.ssm_norm.weight",
    "blk.0.ssm_out.weight",
    "blk.0.ffn_gate_inp.weight",
    "blk.0.ffn_up_exps.weight",
    "blk.0.ffn_up_shexp.weight",
    "blk.3.attn_norm.weight",
    "blk.3.attn_q.weight",
    "blk.3.attn_k.weight",
    "blk.3.attn_v.weight",
    "blk.3.attn_output.weight",
    "blk.3.ffn_gate_inp.weight",
    "blk.3.ffn_up_exps.weight",
    "output.weight",
]

HYBRID_EXPECTED = {
    "token_embd.weight": "embedding",
    "blk.0.attn_norm.weight": "norm",
    "blk.0.attn_qkv.weight": "recurrent",
    "blk.0.attn_gate.weight": "recurrent",
    "blk.0.ssm_conv1d.weight": "recurrent",
    "blk.0.ssm_a": "recurrent",
    "blk.0.ssm_norm.weight": "norm",
    "blk.0.ssm_out.weight": "recurrent",
    "blk.0.ffn_gate_inp.weight": "router",
    "blk.0.ffn_up_exps.weight": "expert_ffn",
    "blk.0.ffn_up_shexp.weight": "shared_ffn",
    "blk.3.attn_norm.weight": "norm",
    "blk.3.attn_q.weight": "attention",
    "blk.3.attn_k.weight": "attention",
    "blk.3.attn_v.weight": "attention",
    "blk.3.attn_output.weight": "attention",
    "blk.3.ffn_gate_inp.weight": "router",
    "blk.3.ffn_up_exps.weight": "expert_ffn",
    "output.weight": "output",
}

# A projector names its tensors "v.blk.N.attn_*"; the substring ".attn_" would
# claim them for the text stack if multimodal were not tested first.
VISION_EXPECTED = {
    "v.blk.0.attn_q.weight": "multimodal",
    "v.blk.0.ffn_up.weight": "multimodal",
    "mm.0.weight": "multimodal",
    "blk.0.nextn.embed_tokens.weight": "mtp",
}

GEMMA_CENSUS = [
    # Committed by the Gemma campaign, so this control survives a clean checkout.
    "benchmarks/campaign-gemma4-26b/tensor-census-unsloth-gemma4-26b-ud-q3kxl.jsonl",
    # Local-probe censuses from the same campaign, checked when present.
    "benchmarks/campaign-gemma4-26b/tensor-census-local-gemma4-26b-qat-ud-q4kxl.jsonl",
    "benchmarks/campaign-gemma4-26b/tensor-census-local-gemma4-26b-ud-q3kxl.jsonl",
    "benchmarks/campaign-gemma4-26b/tensor-census-local-gemma4-26b-ud-q2kxl.jsonl",
]


def check_hybrid(failures):
    rows = {r["name"]: r["category"] for r in tensor_census(_fake(HYBRID))}
    for name, want in HYBRID_EXPECTED.items():
        got = rows.get(name)
        status = "OK  " if got == want else "WRONG"
        print("  %-34s %-11s %s" % (name, got, status))
        if got != want:
            failures.append("hybrid %s: expected %s, got %s" % (name, want, got))

    recurrent = sum(1 for c in rows.values() if c == "recurrent")
    attention = sum(1 for c in rows.values() if c == "attention")
    if (recurrent, attention) != (5, 4):
        failures.append("hybrid split: expected 5 recurrent / 4 attention, "
                        "got %d / %d" % (recurrent, attention))


def check_vision(failures):
    for name, want in VISION_EXPECTED.items():
        got = tensor_category(name)
        status = "OK  " if got == want else "WRONG"
        print("  %-34s %-11s %s" % (name, got, status))
        if got != want:
            failures.append("vision/mtp %s: expected %s, got %s" % (name, want, got))


def check_gemma_unchanged(root, failures):
    for rel in GEMMA_CENSUS:
        path = os.path.join(root, rel)
        if not os.path.exists(path):
            print("  %-52s SKIPPED (absent)" % os.path.basename(rel))
            continue
        with open(path, encoding="utf-8") as handle:
            recorded = [json.loads(line) for line in handle if line.strip()]
        # Replay through the current classifier: same names, same order.
        info = {"tensors": [{"name": r["name"], "dims": [1], "type": "F32",
                             "offset": i * 32} for i, r in enumerate(recorded)]}
        fresh = tensor_census(info)
        diffs = [(a["name"], a["category"], b["category"])
                 for a, b in zip(recorded, fresh) if a["category"] != b["category"]]
        status = "OK  " if not diffs else "CHANGED"
        print("  %-52s %5d tensors  %s" % (os.path.basename(rel), len(recorded), status))
        for name, was, now in diffs[:5]:
            failures.append("gemma %s: was %s, now %s" % (name, was, now))


def main():
    root = sys.argv[1] if len(sys.argv) > 1 else os.path.abspath(
        os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", ".."))
    failures = []

    print("hybrid recurrent/attention split")
    check_hybrid(failures)
    print("")
    print("multimodal and MTP names")
    check_vision(failures)
    print("")
    print("Gemma 4 26B A4B control (classification must not move)")
    check_gemma_unchanged(root, failures)

    print("")
    if failures:
        print("GGUF PROBE SELF-TEST FAILED (%d)" % len(failures))
        for f in failures:
            print("  - %s" % f)
        return 1
    print("GGUF PROBE SELF-TEST PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
