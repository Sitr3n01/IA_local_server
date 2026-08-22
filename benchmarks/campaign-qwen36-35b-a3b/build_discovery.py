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

GGUF_REPO = "unsloth/Qwen3.6-35B-A3B-GGUF"
GGUF_REV = "a483e9e6cbd595906af30beda3187c2663a1118c"
UPSTREAM_REPO = "Qwen/Qwen3.6-35B-A3B"
UPSTREAM_REV = "995ad96eacd98c81ed38be0c5b274b04031597b0"

# Size and SHA-256 as Hugging Face's LFS metadata publishes them at GGUF_REV.
# A locally downloaded file is re-hashed by Get-Qwen36Artifacts.ps1 and only
# renamed into place when it matches, so a present local file means a verified
# one and these are not the last word.
CATALOG = {
    "UD-Q2_K_XL": (12290628576, "96b9c0af5c77a4ecaabe3983175112b5ece763261c1ece12b2494b692a70dad7"),
    "UD-Q3_K_S":  (15359196128, "212ccdf37d416167ce8dcd7e3a59bcd45b30ac7531822a1e7bb79bfbacb2d1aa"),
    "UD-Q3_K_M":  (16600710112, "1b715841683f960bd9a49f008181bd910ee169b78d4cf465b6fde7f4d929ff99"),
    "UD-Q3_K_XL": (16845511648, "a832b9689925f1bd335bbe985cdfb06c36bf2cf268f4f8f6eceafa3ceb515617"),
    "UD-IQ4_XS":  (17730509792, "649d7508507b84638732c4f52c24c8b15843c6dca2f3ff793ae07c14a67ebbb3"),
    "UD-IQ4_NL":  (18040888288, "0d17e255dc257a11f398ed4bc8d62412d8ce9ca24b3fce2947d962e4bfed5758"),
    "MXFP4_MOE":  (21706144736, "2fdd20997c4d88ee25f70f500c61f8b999378d92ab055f9d450fc70d617158d3"),
}

# Read from config.json at UPSTREAM_REV. Labelled UPSTREAM CLAIM in the report:
# it describes the checkpoint the GGUF was converted from, not this GGUF.
UPSTREAM = {
    "repository": UPSTREAM_REPO,
    "revision": UPSTREAM_REV,
    "license": "apache-2.0",
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
    "vision_config_present": True,
    "vocab_size": 248320,
    "rope_theta": 10000000.0,
    "partial_rotary_factor": 0.25,
}


def slug(label):
    return label.replace("-", "").replace("_", "").lower()


def load_probes(base):
    """Prefer a local probe over the remote one for the same artifact."""
    probes = {}
    for path in sorted(glob.glob(os.path.join(base, "probe-*-qwen36-35b-*.json"))):
        name = os.path.basename(path)
        is_local = name.startswith("probe-local-")
        key = name.split("qwen36-35b-", 1)[1][: -len(".json")]
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
        filename = "Qwen3.6-35B-A3B-%s.gguf" % label
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
            total = sum(spans.values())
            row.update({
                "probe_file": probe_file,
                "probe_source": "local file" if is_local else "HTTP range read",
                "gguf_architecture": probe["arch"],
                "block_count": probe["block_count"],
                "n_ctx_train": probe["n_ctx_train"],
                "tensor_count": probe["n_tensors"],
                "mtp_tensor_count": probe["mtp_tensor_count"],
                "nextn_tensor_count": probe["nextn_tensor_count"],
                "mtp_verdict": probe["mtp_verdict"],
                "quant_mix": probe["quant_mix"],
                "category_tensors": probe["category_tensors"],
                "category_span_bytes": spans,
                # The whole placement question in one pair of numbers: what
                # --n-cpu-moe can move, and what it cannot.
                "expert_ffn_gib": round(expert / 2 ** 30, 2),
                "non_expert_gib": round((total - expert) / 2 ** 30, 2),
                "expert_gib_per_layer": round(expert / probe["block_count"] / 2 ** 30, 4),
            })
        files.append(row)
    files.sort(key=lambda r: r["bytes"])

    doc = {
        "campaign": "qwen36-35b-a3b",
        "generated_utc": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "note": ("Header and tensor-info discovery only. No inference produced any figure "
                 "here, and no number in this file is a resource measurement."),
        "upstream_model": UPSTREAM,
        "gguf_repository": {
            "repository": GGUF_REPO,
            "revision": GGUF_REV,
            "license": "apache-2.0",
            "sha256_source": ("Hugging Face LFS object id at the pinned revision; "
                              "re-verified locally before an artifact is used"),
            "mmproj_files_present": ["mmproj-BF16.gguf", "mmproj-F16.gguf", "mmproj-F32.gguf"],
            "mmproj_note": ("Vision is out of scope for this qualification. No mmproj is "
                            "downloaded and none is passed to llama-server, and the text "
                            "GGUFs carry no vision tensors."),
        },
        "gguf_common_shape": {
            "general.architecture": "qwen35moe",
            "block_count": 40,
            "context_length": 262144,
            "tensor_count": 733,
            "expert_count": 256,
            "expert_used_count": 8,
            "attention.head_count": 16,
            "attention.head_count_kv": 2,
            "attention.key_length": 256,
            "attention.value_length": 256,
            "expert_feed_forward_length": 512,
            "expert_shared_feed_forward_length": 512,
            "full_attention_interval": 4,
            "ssm.conv_kernel": 4,
            "ssm.state_size": 128,
            "ssm.group_count": 16,
            "ssm.time_step_rank": 32,
            "ssm.inner_size": 4096,
            "rope.freq_base": 10000000.0,
            "rope.dimension_count": 64,
            "tokenizer.ggml.model": "gpt2",
            "tokenizer.ggml.pre": "qwen35",
            "tokenizer.ggml.bos_token_id": 248044,
            "tokenizer.ggml.eos_token_id": 248046,
            "mtp_tensor_count": 0,
            "vision_tensor_count": 0,
        },
        "files": files,
    }

    out = os.path.join(base, "artifact-discovery.json")
    with open(out, "w", encoding="utf-8") as handle:
        json.dump(doc, handle, indent=2)
    print("wrote %s" % out)

    header = "%-34s %8s %9s %11s %12s  %s"
    print(header % ("FILE", "GiB", "expertGiB", "nonExprGiB", "exp/layerGiB", "probe"))
    for row in files:
        if "expert_ffn_gib" in row:
            print(header % (row["filename"].replace("Qwen3.6-35B-A3B-", ""),
                            "%.2f" % row["gib"], "%.2f" % row["expert_ffn_gib"],
                            "%.2f" % row["non_expert_gib"], "%.4f" % row["expert_gib_per_layer"],
                            row["probe_source"]))
        else:
            print(header % (row["filename"].replace("Qwen3.6-35B-A3B-", ""),
                            "%.2f" % row["gib"], "-", "-", "-", "not probed"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
