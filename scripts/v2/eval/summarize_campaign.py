#!/usr/bin/env python3
"""Reduce the campaign's per-cell JSON into the report's tables.

Kept separate from the runners so the tables can be regenerated from artifacts
without re-running anything, and so a partial campaign still produces a readable
summary. Every number here is read from a file some runner wrote; nothing is
recomputed from a rate that was already reported.

Usage: python summarize_campaign.py <campaign-dir> [--markdown]
"""
import argparse
import glob
import io
import json
import os

# PowerShell 5.1's Set-Content -Encoding UTF8 writes a BOM.
def load(path):
    with io.open(path, encoding="utf-8-sig") as handle:
        return json.load(handle)


def fmt(value, spec="%.2f", dash="-"):
    if value is None:
        return dash
    try:
        return spec % value
    except (TypeError, ValueError):
        return str(value)


def footprint_table(root):
    rows = []
    for path in sorted(glob.glob(os.path.join(root, "footprint-*.json"))):
        d = load(path)
        c = d["configuration"]
        p = d.get("peak") or {}
        rows.append({
            "model": os.path.basename(path).split("-")[1],
            "ctx": c["context_tokens"],
            "k": c["cache_type_k"], "v": c["cache_type_v"],
            "cpu_moe": c.get("cpu_moe"),
            "n_cpu_moe": c.get("n_cpu_moe"),
            "dedicated": p.get("vram_dedicated_mib"),
            "shared": p.get("vram_shared_mib"),
            "ws": p.get("process_ws_gib"),
            "private": p.get("process_private_gib"),
            "marginal": d.get("marginal_vram_mib"),
            "state": (d.get("gpu_pressure") or {}).get("state"),
            "load_s": d.get("load_seconds"),
            "failure": d.get("failure"),
        })
    rows.sort(key=lambda r: (r["model"], r["k"], r["v"], r["ctx"]))
    return rows


def quality_table(root):
    out = []
    for path in sorted(glob.glob(os.path.join(root, "profile-*.json"))):
        d = load(path)
        ev = d.get("eval") or {}
        suites = ev.get("suites") or {}
        coding = suites.get("coding") or {}
        tools = suites.get("tools") or {}
        js = suites.get("json") or {}
        peak = d.get("session_peak") or {}
        entry = {
            "label": d.get("label"),
            "model_bytes": (d.get("configuration") or {}).get("model_bytes"),
            "ctx": (d.get("configuration") or {}).get("context_tokens"),
            "k": (d.get("configuration") or {}).get("cache_type_k"),
            "v": (d.get("configuration") or {}).get("cache_type_v"),
            "cpu_moe": (d.get("configuration") or {}).get("cpu_moe"),
            "n_cpu_moe": (d.get("configuration") or {}).get("n_cpu_moe"),
            "coding_passed": coding.get("passed"),
            "coding_total": coding.get("total"),
            "coding_no_answer": coding.get("no_answer"),
            "coding_truncated": coding.get("truncated"),
            "tools_passed": tools.get("passed"),
            "tools_total": tools.get("total"),
            "json_passed": js.get("passed"),
            "dedicated": peak.get("vram_dedicated_mib"),
            "shared": peak.get("vram_shared_mib"),
            "ws": peak.get("process_ws_gib"),
            "private": peak.get("process_private_gib"),
            "pressure": (d.get("gpu_pressure") or {}).get("state"),
            "load_s": d.get("load_seconds"),
            "failure": d.get("failure"),
            "per_task": {r["id"]: {
                "passed": r.get("passed"),
                "no_answer": r.get("no_answer"),
                "truncated": r.get("truncated"),
                "out_tokens": r.get("output_tokens"),
                "reasoning_chars": r.get("reasoning_chars"),
                "detail": (r.get("detail") or "")[:200],
            } for r in (coding.get("results") or [])},
            "per_tool": {r["id"]: {
                "passed": r.get("passed"),
                "names": r.get("names"),
                "detail": (r.get("detail") or "")[:200],
            } for r in (tools.get("results") or [])},
            "retention": suites.get("retention"),
        }
        out.append(entry)
    return out


def _raw_bench(root, tag):
    """Recover a result straight from llama-bench's own output file.

    The roll-up written by the sweep script is a convenience; llama-bench's JSON
    is the artifact. An early revision of the sweep recorded every completed run
    as a failure because the timeout overload of WaitForExit leaves ExitCode
    unpopulated, and the numbers were recovered from these files rather than by
    re-running six hours of benchmarks. Reading them here makes that recovery the
    normal path instead of a manual one.
    """
    path = os.path.join(root, "bench-%s.json" % tag)
    if not os.path.exists(path):
        return None, None
    try:
        rows = load(path)
        entry = rows[-1] if isinstance(rows, list) else rows
        return round(float(entry["avg_ts"]), 2), round(float(entry.get("stddev_ts", 0)), 2)
    except Exception:
        return None, None


def throughput_table(root):
    out = []
    for path in sorted(glob.glob(os.path.join(root, "throughput-*.json"))):
        d = load(path)
        for r in d.get("results") or []:
            peak = r.get("peak") or {}
            tps, sd = r.get("tokens_per_second"), r.get("stddev")
            if tps is None:
                tps, sd = _raw_bench(root, r.get("tag") or "")
            out.append({
                "label": d.get("label"),
                "cpu_moe": (d.get("configuration") or {}).get("cpu_moe"),
                "n_cpu_moe": (d.get("configuration") or {}).get("n_cpu_moe"),
                "tag": r.get("tag"), "kind": r.get("kind"),
                "n": r.get("n"), "depth": r.get("depth"),
                "tps": tps, "stddev": sd,
                "dedicated": peak.get("vram_dedicated_mib"),
                "shared": peak.get("vram_shared_mib"),
                "ws": peak.get("process_ws_gib"),
                "wall_s": r.get("wall_s"),
                "failure": r.get("failure"),
            })
    return out


def print_text(root):
    print("=" * 100)
    print("FOOTPRINT AT LOAD")
    print("%-7s %-6s %-6s %8s %8s %9s %8s %8s %8s %9s %-10s" %
          ("model", "K", "V", "ctx", "cpu_moe", "dedicated", "shared", "ws", "private", "marginal", "state"))
    for r in footprint_table(root):
        moe = "all" if r["cpu_moe"] else (r["n_cpu_moe"] if r["n_cpu_moe"] is not None else "-")
        print("%-7s %-6s %-6s %8d %8s %9s %8s %8s %8s %9s %-10s" % (
            r["model"], r["k"], r["v"], r["ctx"], moe,
            fmt(r["dedicated"], "%.0f"), fmt(r["shared"], "%.0f"),
            fmt(r["ws"], "%.2f"), fmt(r["private"], "%.2f"),
            fmt(r["marginal"], "%.0f"), r["state"] or "-"))

    print()
    print("=" * 100)
    print("QUALITY")
    for q in quality_table(root):
        print("-" * 100)
        moe = "all" if q["cpu_moe"] else (q["n_cpu_moe"] if q["n_cpu_moe"] is not None else "-")
        print("%s   ctx=%s kv=%s/%s cpu_moe=%s   model=%.2f GiB" % (
            q["label"], q["ctx"], q["k"], q["v"], moe,
            (q["model_bytes"] or 0) / (1024 ** 3)))
        if q["failure"]:
            print("   FAILURE: %s" % q["failure"])
        print("   coding  %s/%s pass   (no_answer %s, truncated %s)" % (
            q["coding_passed"], q["coding_total"], q["coding_no_answer"], q["coding_truncated"]))
        for tid, t in q["per_task"].items():
            mark = "PASS" if t["passed"] else ("NO-ANSWER" if t["no_answer"] else "FAIL")
            print("      %-24s %-10s out=%-5s think=%-6s %s" % (
                tid, mark, t["out_tokens"], t["reasoning_chars"],
                "" if t["passed"] else (t["detail"] or "").replace("\n", " ")[:110]))
        print("   tools   %s/%s pass" % (q["tools_passed"], q["tools_total"]))
        for tid, t in q["per_tool"].items():
            print("      %-24s %-6s %-28s %s" % (
                tid, "PASS" if t["passed"] else "FAIL", str(t["names"]),
                (t["detail"] or "").replace("\n", " ")[:80]))
        print("   json    %s" % ("PASS" if q["json_passed"] else "FAIL"))
        print("   peak    dedicated %s | shared %s | ws %s | private %s | %s" % (
            fmt(q["dedicated"], "%.0f"), fmt(q["shared"], "%.0f"),
            fmt(q["ws"], "%.2f"), fmt(q["private"], "%.2f"), q["pressure"]))
        if q["retention"]:
            print("   retention:")
            for r in q["retention"]:
                if r.get("failure"):
                    print("      target %-7s FAILED: %s" % (r.get("target_tokens"), r["failure"]))
                    continue
                fams = r.get("by_family") or {}
                print("      prompt %-7s score %-5s exact %-2s halluc %-2s stale %-2s | "
                      "prefill %s t/s | TTFT %ss | decode %s t/s | %s" % (
                          r.get("prompt_tokens"), fmt(r.get("score"), "%.2f"),
                          r.get("exact_count"), r.get("hallucinated_count"),
                          r.get("stale_count"),
                          fmt(r.get("prefill_tps"), "%.1f"),
                          fmt((r.get("ttft_prefill_ms") or 0) / 1000.0, "%.1f"),
                          fmt(r.get("decode_tps_at_occupancy"), "%.2f"),
                          " ".join("%s=%.2f" % (k, v["score"]) for k, v in sorted(fams.items()))))

    print()
    print("=" * 100)
    print("THROUGHPUT")
    print("%-22s %-8s %-6s %8s %8s %10s %9s %8s %8s" %
          ("label", "cpu_moe", "kind", "n", "depth", "t/s", "stddev", "shared", "wall_s"))
    for r in throughput_table(root):
        moe = "all" if r["cpu_moe"] else (r["n_cpu_moe"] if r["n_cpu_moe"] is not None else "-")
        print("%-22s %-8s %-6s %8s %8s %10s %9s %8s %8s" % (
            r["label"], moe, r["kind"], r["n"], r["depth"],
            fmt(r["tps"]), fmt(r["stddev"]), fmt(r["shared"], "%.0f"),
            fmt(r["wall_s"], "%.0f")))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("root")
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()
    if args.json:
        print(json.dumps({
            "footprint": footprint_table(args.root),
            "quality": quality_table(args.root),
            "throughput": throughput_table(args.root),
        }, indent=2, default=str))
    else:
        print_text(args.root)


if __name__ == "__main__":
    main()
