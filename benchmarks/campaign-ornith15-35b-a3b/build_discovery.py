#!/usr/bin/env python3
"""Rebuild artifact-discovery.json from the probe files in this directory.

Separated from the probing itself so the record can be regenerated after a local
probe replaces a remote one, without re-reading 12 MB of headers per artifact
over the network. Everything it writes comes from a probe file or from the
pinned tables below; nothing is estimated.

Run: python build_discovery.py [campaign-dir]
"""
import datetime
import glob
import json
import os
import sys

GGUF_REPO = "bartowski/Ornith-1.5-35B-A3B-GGUF"
GGUF_REV = "64b0493d34a5ca4c1b4ad67bb99b41d74b4f07d6"
UPSTREAM_REPO = "ornith-ai/Ornith-1.5-35B-A3B"
UPSTREAM_REV = "10fbf86fed7ecee4a061f8b499a618f46001cac1"

# Size and SHA-256 as Hugging Face's LFS metadata publishes them at GGUF_REV.
# A locally downloaded file is re-hashed by Get-Ornith15Artifacts.ps1 and only
# renamed into place when it matches, so a present local file means a verified
# one and these are not the last word.
CATALOG = {
    "IQ2_M":   (12543403680, "be92ed1eb2da35876e91a5551c527ddcce450d49ec53aba92f03e26adba7b996"),
    "Q2_K":    (13085779616, "6e5742a1a8c263a9c77cd7ffd5338a290a29c1d5d3dd4d6deee76351b0718fe1"),
    "IQ3_XXS": (15340447392, "8918ccb9ee29abe3875efec0c3e86f0e35ef0b8a03e3f0e1c422869859a518d0"),
    "Q3_K_S":  (15983920800, "f2e3c4b472a353c3d22d2eece67cec20ce1f9b6c646efd433c4892c1e4d63ffb"),
    "IQ3_XS":  (16689088160, "202b8b39bcc35a0039d8e65af0697bf1ce969a8a99653356cc7911d2439f8828"),
    "Q3_K_XL": (17801889440, "782359e862536c3d743899e83906312ae66b9512ba8a27ad6013bb1d3032cccb"),
    "IQ4_XS":  (19278554784, "d6aef57fa948e9bba3ca4959b3c237ed898c605471f48c73a32cedbd24aabe70"),
}

# The three rungs this campaign downloads, and the Qwen3.6 artifact each one is
# sized against. The pairing is by bytes, not by quant name: bartowski's ladder
# is not unsloth's UD recipe, so a name-for-name comparison would be a
# comparison of quantizers. What the workstation actually constrains is bytes.
MATCHED_BUDGET_PAIRS = [
    {"ornith": "Ornith-1.5-35B-A3B-IQ2_M.gguf",   "ornith_bytes": 12543403680,
     "qwen36": "Qwen3.6-35B-A3B-UD-Q2_K_XL.gguf", "qwen36_bytes": 12290628576},
    {"ornith": "Ornith-1.5-35B-A3B-IQ3_XXS.gguf", "ornith_bytes": 15340447392,
     "qwen36": "Qwen3.6-35B-A3B-UD-Q3_K_S.gguf",  "qwen36_bytes": 15359196128},
    {"ornith": "Ornith-1.5-35B-A3B-Q3_K_XL.gguf", "ornith_bytes": 17801889440,
     "qwen36": "Qwen3.6-35B-A3B-UD-IQ4_XS.gguf",  "qwen36_bytes": 17730509792},
]

# Read from config.json at UPSTREAM_REV. Labelled UPSTREAM CLAIM in the report:
# it describes the checkpoint the GGUF was converted from, not this GGUF.
UPSTREAM = {
    "repository": UPSTREAM_REPO,
    "revision": UPSTREAM_REV,
    "license": "mit",
    "evidence": "config.json read at the pinned revision",
    "transformers_architecture": "Qwen3_5MoeForConditionalGeneration",
    "model_type": "qwen3_5_moe",
    "num_hidden_layers": 40,
    "num_experts": 256,
    "num_experts_per_tok": 8,
    "shared_expert_intermediate_size": 512,
    "moe_intermediate_size": 512,
    "hidden_size": 2048,
    "num_attention_heads": 16,
    "num_key_value_heads": 2,
    "head_dim": 256,
    "attn_output_gate": True,
    "full_attention_interval": 4,
    "full_attention_layers": 10,
    "linear_attention_layers": 30,
    "linear_conv_kernel_dim": 4,
    "linear_num_key_heads": 16,
    "linear_num_value_heads": 32,
    "linear_key_head_dim": 128,
    "linear_value_head_dim": 128,
    "max_position_embeddings": 262144,
    "mtp_num_hidden_layers": 1,
    "mtp_use_dedicated_embeddings": False,
    "vision_config_present": True,
    "vocab_size": 248320,
    "rope_theta": 10000000.0,
    "partial_rotary_factor": 0.25,
    "mrope_interleaved": True,
    "mrope_section": [11, 11, 10],
}

# What the upstream config declares as `num_hidden_layers: 40` plus
# `mtp_num_hidden_layers: 1` arrives in these GGUFs as 41 blocks. Qwen3.6's
# unsloth GGUFs declare the same config and ship 40, because the MTP head was
# dropped at conversion. That single difference is why this campaign has a
# Phase 6 and the Qwen3.6 campaign did not.
MTP_NOTE = (
    "blk.40 is a NextN/MTP block: 4 nextn projections plus a full attention and "
    "a 256-expert FFN of its own, 0.477 GB in every rung because bartowski "
    "leaves it Q4_0 regardless of the ladder. Q4_0 is below the >= Q5_K floor "
    "scripts/v2/eval/gguf_probe.py grades MTP heads against, so every rung "
    "reports MTP_UNQUALIFIED. That is a statement about the draft head only; it "
    "does not disqualify the artifact, and whether the head is usable at all is "
    "measured in Phase 6 rather than assumed here."
)


def slug(label):
    return label.replace("-", "").replace("_", "").lower()


def load_probes(base):
    """Prefer a local probe over the remote one for the same artifact."""
    probes = {}
    for path in sorted(glob.glob(os.path.join(base, "probe-*-ornith15-35b-*.json"))):
        name = os.path.basename(path)
        is_local = name.startswith("probe-local-")
        key = name.split("ornith15-35b-", 1)[1][: -len(".json")]
        if key in probes and probes[key][1] and not is_local:
            continue
        with open(path, encoding="utf-8") as handle:
            probes[key] = (json.load(handle), is_local, name)
    return probes


def main():
    base = sys.argv[1] if len(sys.argv) > 1 else os.path.dirname(os.path.abspath(__file__))
    probes = load_probes(base)

    files = []
    for label, (size, sha) in CATALOG.items():
        filename = "Ornith-1.5-35B-A3B-%s.gguf" % label
        row = {
            "filename": filename,
            "bytes": size,
            "gib": round(size / 2 ** 30, 2),
            "sha256_lfs": sha,
        }
        entry = probes.get(slug(label))
        if entry:
            probe, is_local, probe_file = entry
            spans = probe["category_span_bytes"]
            expert = spans.get("expert_ffn", 0)
            mtp_span = spans.get("mtp", 0)
            total = sum(spans.values())
            row.update({
                "probe_file": probe_file,
                "probe_source": "local file" if is_local else "HTTP range read",
                "gguf_architecture": probe["arch"],
                "block_count": probe["block_count"],
                "n_ctx_train": probe["n_ctx_train"],
                "tensor_count": probe["n_tensors"],
                "mtp_prefix": probe.get("mtp_prefix"),
                "mtp_tensor_count": probe["mtp_tensor_count"],
                "nextn_tensor_count": probe["nextn_tensor_count"],
                "mtp_worst_quant": probe["mtp_worst_quant"],
                "mtp_verdict": probe["mtp_verdict"],
                "quant_mix": probe["quant_mix"],
                "category_tensors": probe["category_tensors"],
                "category_span_bytes": spans,
                # The whole placement question in one pair of numbers: what
                # --n-cpu-moe can move, and what it cannot.
                "expert_ffn_gib": round(expert / 2 ** 30, 2),
                "non_expert_gib": round((total - expert) / 2 ** 30, 2),
                "expert_ffn_share": round(expert / total, 4) if total else None,
                "expert_gib_per_layer": round(expert / probe["block_count"] / 2 ** 30, 4),
                # Reported separately from the expert share because this is the
                # part of the file that only pays off if Phase 6 says it does.
                "mtp_nextn_span_bytes": mtp_span,
            })
        files.append(row)
    files.sort(key=lambda r: r["bytes"])

    for pair in MATCHED_BUDGET_PAIRS:
        pair["delta_bytes"] = pair["ornith_bytes"] - pair["qwen36_bytes"]
        pair["delta_percent"] = round(
            100.0 * pair["delta_bytes"] / pair["qwen36_bytes"], 2)

    doc = {
        "campaign": "ornith15-35b-a3b",
        "generated_utc": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "note": ("Header and tensor-info discovery only. No inference produced any figure "
                 "here, and no number in this file is a resource measurement."),
        "question": ("Whether Ornith-1.5-35B-A3B should replace Qwen3.6-35B-A3B at the VRAM "
                     "budget this workstation has. The comparison is by file size, not by "
                     "quant name - see matched_budget_pairs."),
        "upstream_model": UPSTREAM,
        "gguf_repository": {
            "repository": GGUF_REPO,
            "revision": GGUF_REV,
            "license": "mit",
            "sha256_source": ("Hugging Face LFS object id at the pinned revision; "
                              "re-verified locally before an artifact is used"),
            "provenance_note": (
                "unsloth publishes no GGUF repository for this model, so these are not the "
                "UD quants the rest of the roster uses. bartowski's ladder is "
                "imatrix-calibrated (Ornith-1.5-35B-A3B-imatrix.gguf is published alongside "
                "it at the same revision). The first-party repository ornith-ai/"
                "Ornith-1.5-35B-A3B-GGUF stops at Q4_K_M / 21.71 GB, which does not fit this "
                "workstation's budget, which is why it was not used."),
            "mmproj_files_present": ["mmproj-Ornith-1.5-35B-A3B-bf16.gguf",
                                     "mmproj-Ornith-1.5-35B-A3B-f16.gguf"],
            "mmproj_note": ("Vision is out of scope for this qualification. No mmproj is "
                            "downloaded and none is passed to llama-server, and the text "
                            "GGUFs carry no vision tensors."),
        },
        "gguf_common_shape": {
            "general.architecture": "qwen35moe",
            "block_count": 41,
            "context_length": 262144,
            "tensor_count": 753,
            "expert_count": 256,
            "expert_used_count": 8,
            "attention.head_count": 16,
            "attention.head_count_kv": 2,
            "attention.key_length": 256,
            "attention.value_length": 256,
            "expert_feed_forward_length": 512,
            "expert_shared_feed_forward_length": 512,
        },
        "mtp_head": MTP_NOTE,
        "matched_budget_pairs": MATCHED_BUDGET_PAIRS,
        "downloaded": ["Ornith-1.5-35B-A3B-IQ2_M.gguf",
                       "Ornith-1.5-35B-A3B-IQ3_XXS.gguf",
                       "Ornith-1.5-35B-A3B-Q3_K_XL.gguf"],
        "files": files,
    }

    out = os.path.join(base, "artifact-discovery.json")
    with open(out, "w", encoding="utf-8") as handle:
        json.dump(doc, handle, indent=1)
        handle.write("\n")
    print("wrote %s (%d artifacts, %d probed)" % (
        out, len(files), sum(1 for r in files if "probe_file" in r)))


if __name__ == "__main__":
    main()
