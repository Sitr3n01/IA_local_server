#!/usr/bin/env python3
"""Read a GGUF header over HTTP range requests and report tensor quantization.

A GGUF's tensor-info block sits immediately after the metadata, both at the head
of the file, so the quantization of every tensor is knowable without downloading
the tensor data. On a 13 GB artifact that is the difference between a ~16 MB
probe and an hour of bandwidth.
"""
import argparse
import json
import re
import struct
import sys
import urllib.request

GGML_TYPES = {
    0: "F32", 1: "F16", 2: "Q4_0", 3: "Q4_1", 6: "Q5_0", 7: "Q5_1",
    8: "Q8_0", 9: "Q8_1", 10: "Q2_K", 11: "Q3_K", 12: "Q4_K", 13: "Q5_K",
    14: "Q6_K", 15: "Q8_K", 16: "IQ2_XXS", 17: "IQ2_XS", 18: "IQ3_XXS",
    19: "IQ1_S", 20: "IQ4_NL", 21: "IQ3_S", 22: "IQ2_S", 23: "IQ4_XS",
    24: "I8", 25: "I16", 26: "I32", 27: "I64", 28: "F64", 29: "IQ1_M",
    30: "BF16", 34: "TQ1_0", 35: "TQ2_0", 39: "MXFP4",
}

# Ranked worst -> best, for the ">= Q5_K" MTP gate in model-test-matrix.json.
QUALITY_RANK = {
    "IQ1_S": 1, "IQ1_M": 2, "TQ1_0": 2, "TQ2_0": 3, "IQ2_XXS": 4, "IQ2_XS": 5,
    "IQ2_S": 6, "Q2_K": 7, "IQ3_XXS": 8, "IQ3_S": 9, "Q3_K": 10, "Q4_0": 11,
    "Q4_1": 12, "IQ4_XS": 13, "IQ4_NL": 13, "Q4_K": 14, "MXFP4": 14,
    "Q5_0": 15, "Q5_1": 16, "Q5_K": 17, "Q6_K": 18, "Q8_0": 19, "Q8_1": 19,
    "Q8_K": 20, "BF16": 21, "F16": 21, "F32": 22, "F64": 22,
}
GATE_FLOOR = QUALITY_RANK["Q5_K"]

SCALAR = {0: ("<B", 1), 1: ("<b", 1), 2: ("<H", 2), 3: ("<h", 2), 4: ("<I", 4),
          5: ("<i", 4), 6: ("<f", 4), 7: ("<?", 1), 10: ("<Q", 8),
          11: ("<q", 8), 12: ("<d", 8)}


class RangeReader:
    """Byte cursor over an HTTP resource, pulling fixed windows on demand."""

    def __init__(self, url, chunk=4 << 20, local=None):
        self.url = url
        self.chunk = chunk
        self.buf = b""
        self.base = 0
        self.pos = 0
        self.fetched = 0
        self.requests = 0
        self.local = local

    def _fetch(self, start, length):
        if self.local is not None:
            with open(self.local, "rb") as handle:
                handle.seek(start)
                data = handle.read(length)
        else:
            req = urllib.request.Request(
                self.url, headers={"Range": "bytes=%d-%d" % (start, start + length - 1)}
            )
            with urllib.request.urlopen(req, timeout=180) as resp:
                data = resp.read()
        self.requests += 1
        self.fetched += len(data)
        return data

    def need(self, count):
        end = self.pos + count
        if self.base <= self.pos and end <= self.base + len(self.buf):
            return
        start = self.pos
        length = max(count, self.chunk)
        self.buf = self._fetch(start, length)
        self.base = start
        if len(self.buf) < count:
            raise EOFError("short read at %d: wanted %d, got %d"
                           % (start, count, len(self.buf)))

    def read(self, count):
        self.need(count)
        off = self.pos - self.base
        out = self.buf[off:off + count]
        self.pos += count
        return out

    def u32(self):
        return struct.unpack("<I", self.read(4))[0]

    def u64(self):
        return struct.unpack("<Q", self.read(8))[0]

    def string(self):
        n = self.u64()
        if n > (64 << 20):
            raise ValueError("absurd string length %d at %d" % (n, self.pos))
        return self.read(n).decode("utf-8", "replace")


def read_value(r, vtype):
    if vtype in SCALAR:
        fmt, size = SCALAR[vtype]
        return struct.unpack(fmt, r.read(size))[0]
    if vtype == 8:
        return r.string()
    if vtype == 9:
        etype = r.u32()
        count = r.u64()
        if etype == 8:
            # Tokenizer vocabularies run to hundreds of thousands of strings and
            # are never what this probe is after; skip without materialising.
            for _ in range(count):
                r.read(r.u64())
            return "<%d strings>" % count
        if etype == 9:
            return [read_value(r, 9) for _ in range(count)]
        fmt, size = SCALAR[etype]
        raw = r.read(size * count)
        vals = list(struct.unpack("<" + fmt[1] * count, raw))
        return vals if count <= 16 else "<%d values>" % count
    raise ValueError("unknown gguf value type %d" % vtype)


def probe(url, local=None):
    r = RangeReader(url, local=local)
    magic = r.read(4)
    if magic != b"GGUF":
        raise ValueError("not a GGUF: magic=%r" % magic)
    version = r.u32()
    n_tensors = r.u64()
    n_kv = r.u64()

    meta = {}
    for _ in range(n_kv):
        key = r.string()
        vtype = r.u32()
        meta[key] = read_value(r, vtype)

    tensors = []
    for _ in range(n_tensors):
        name = r.string()
        ndim = r.u32()
        dims = [r.u64() for _ in range(ndim)]
        ttype = r.u32()
        offset = r.u64()  # relative to the tensor data section
        tensors.append({"name": name, "dims": dims,
                        "type": GGML_TYPES.get(ttype, "UNK%d" % ttype),
                        "offset": offset})

    return {
        "version": version,
        "n_tensors": n_tensors,
        "n_kv": n_kv,
        "metadata": meta,
        "tensors": tensors,
        "bytes_fetched": r.fetched,
        "range_requests": r.requests,
    }


def tensor_category(name):
    if name.startswith("token_embd"):
        return "embedding"
    if name.startswith("output"):
        return "output"
    if "norm" in name:
        return "norm"
    if ".attn_" in name:
        return "attention"
    if "ffn_gate_inp" in name or "router" in name:
        return "router"
    if "_exps" in name or ".ffn_" in name and "_exp" in name:
        return "expert_ffn"
    if ".ffn_" in name:
        return "shared_ffn"
    if "mmproj" in name or "vision" in name:
        return "multimodal"
    return "other"


def tensor_census(info):
    tensors = sorted(info["tensors"], key=lambda t: t["offset"])
    next_offsets = [t["offset"] for t in tensors[1:]] + [None]
    rows = []
    for tensor, next_offset in zip(tensors, next_offsets):
        shape = list(reversed(tensor["dims"]))
        elements = 1
        for dim in tensor["dims"]:
            elements *= dim
        layer_match = re.search(r"(?:^|\.)blk\.(\d+)\.", tensor["name"])
        rows.append({
            "name": tensor["name"],
            "shape": shape,
            "quant": tensor["type"],
            "offset": tensor["offset"],
            "span_bytes": None if next_offset is None else next_offset - tensor["offset"],
            "elements": elements,
            "layer": None if not layer_match else int(layer_match.group(1)),
            "category": tensor_category(tensor["name"]),
        })
    return rows


def summarize(info, mtp_prefix="blk.64."):
    tensors = info["tensors"]
    census = tensor_census(info)
    mtp = [t for t in tensors if t["name"].startswith(mtp_prefix)]
    nextn = [t for t in tensors if "nextn" in t["name"]]

    quant_mix = {}
    for t in tensors:
        quant_mix[t["type"]] = quant_mix.get(t["type"], 0) + 1
    category_bytes = {}
    category_tensors = {}
    for row in census:
        category = row["category"]
        category_tensors[category] = category_tensors.get(category, 0) + 1
        if row["span_bytes"] is not None:
            category_bytes[category] = category_bytes.get(category, 0) + row["span_bytes"]

    # The gate applies to the head's quantized weight tensors; norms and biases
    # are F32 in every build and would mask a Q4 projection if averaged in.
    graded = [t for t in mtp if t["type"] not in ("F32", "F16", "BF16")]
    worst = None
    if graded:
        worst = min(graded, key=lambda t: QUALITY_RANK.get(t["type"], 0))

    verdict = "NO_MTP_HEAD"
    if mtp:
        if not graded:
            verdict = "PASS_ALL_FLOAT"
        elif QUALITY_RANK.get(worst["type"], 0) >= GATE_FLOOR:
            verdict = "PASS"
        else:
            verdict = "MTP_UNQUALIFIED"

    def meta_suffix(suffix):
        return next((v for k, v in info["metadata"].items()
                     if k.endswith(suffix)), None)

    return {
        "arch": info["metadata"].get("general.architecture"),
        "block_count": meta_suffix(".block_count"),
        "n_ctx_train": meta_suffix(".context_length"),
        "n_tensors": info["n_tensors"],
        "mtp_tensor_count": len(mtp),
        "nextn_tensor_count": len(nextn),
        "mtp_tensors": [{"name": t["name"], "type": t["type"], "dims": t["dims"]}
                        for t in mtp],
        "mtp_worst_quant": worst["type"] if worst else None,
        "mtp_verdict": verdict,
        "quant_mix": dict(sorted(quant_mix.items(), key=lambda kv: -kv[1])),
        "category_tensors": dict(sorted(category_tensors.items())),
        "category_span_bytes": dict(sorted(category_bytes.items())),
        "bytes_fetched": info["bytes_fetched"],
        "range_requests": info["range_requests"],
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("target", help="URL, or 'repo::filename' shorthand")
    ap.add_argument("--local", help="read a local file instead of HTTP")
    ap.add_argument("--revision", default="main")
    ap.add_argument("--json", action="store_true")
    ap.add_argument("--out", help="write the JSON summary here")
    ap.add_argument("--census-out", help="write the full tensor census as JSON Lines")
    ap.add_argument("--mtp-prefix", default="blk.64.")
    args = ap.parse_args()

    if args.target.startswith("http"):
        url = args.target
    else:
        repo, _, fname = args.target.partition("::")
        url = "https://huggingface.co/%s/resolve/%s/%s" % (repo, args.revision, fname)

    info = probe(url, local=args.local)
    summary = summarize(info, args.mtp_prefix)
    summary["source"] = args.local or url

    if args.census_out:
        with open(args.census_out, "w", encoding="utf-8") as handle:
            for row in tensor_census(info):
                handle.write(json.dumps(row, default=str) + "\n")

    if args.out:
        with open(args.out, "w", encoding="utf-8") as handle:
            json.dump(summary, handle, indent=2, default=str)

    if args.json:
        print(json.dumps(summary, indent=2, default=str))
    else:
        print("source            : %s" % summary["source"])
        print("arch/blocks/ctx   : %s / %s / %s"
              % (summary["arch"], summary["block_count"], summary["n_ctx_train"]))
        print("tensors           : %s" % summary["n_tensors"])
        print("MTP tensors       : %d (nextn: %d)"
              % (summary["mtp_tensor_count"], summary["nextn_tensor_count"]))
        for t in summary["mtp_tensors"]:
            print("    %-44s %-8s %s" % (t["name"], t["type"], t["dims"]))
        print("MTP worst quant   : %s" % summary["mtp_worst_quant"])
        print("VERDICT           : %s" % summary["mtp_verdict"])
        print("quant mix         : %s" % summary["quant_mix"])
        print("category tensors  : %s" % summary["category_tensors"])
        print("category spans    : %s" % summary["category_span_bytes"])
        print("probe cost        : %.1f MB in %d range requests"
              % (summary["bytes_fetched"] / 1e6, summary["range_requests"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
