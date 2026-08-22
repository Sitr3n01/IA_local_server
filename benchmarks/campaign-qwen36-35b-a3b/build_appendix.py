#!/usr/bin/env python3
"""Regenerates the report's complete-results appendix from the campaign's JSON.

Every table below is produced from the measurement files rather than typed, so
the report cannot drift from the data it cites. Re-run after any new cell:

    python build_appendix.py > appendix-generated.md

Cells whose file name carries a `-repeat` / `-passN` suffix are second and
further readings of a configuration already measured. They are listed with the
suffix rather than folded in, because a disagreement between two runs of one
configuration is itself a finding (report section 9.3).
"""
import glob
import io
import json
import os
import re
import sys

ORDER = ["q2kxl", "q3ks", "q3kxl", "iq4xs"]
PRETTY = {"q2kxl": "UD-Q2_K_XL", "q3ks": "UD-Q3_K_S",
          "q3kxl": "UD-Q3_K_XL", "iq4xs": "UD-IQ4_XS"}
SHARED_MARGIN = 400.0
DEVICE = 16304
RESERVE = 1024
BUDGETS = {"dedicated": 2700, "workstation": 4400}

CELL_RE = re.compile(
    r"^footprint-(\w+?)-ctx(\d+)-kv([a-z0-9_]+?)(?:-([a-z0-9_]+))?"
    r"-ncpumoe(\d+)(-[a-z0-9]+)?\.json$")


def load(path):
    return json.load(io.open(path, encoding="utf-8-sig"))


def fmt(value, spec="%.1f", dash="-"):
    return dash if value is None else spec % value


def footprint_rows():
    rows = []
    for path in glob.glob("footprint-*.json"):
        match = CELL_RE.match(os.path.basename(path))
        if not match:
            sys.stderr.write("unparsed: %s\n" % path)
            continue
        model, ctx, ctk, ctv, n, suffix = match.groups()
        report = load(path)
        idle, peak = report.get("idle"), report.get("peak")
        spill = None
        if idle and peak:
            spill = round(peak["vram_shared_mib"] - idle["vram_shared_mib"], 1)
        rows.append({
            "model": model, "ctx": int(ctx), "ctk": ctk, "ctv": ctv or ctk,
            "n": int(n), "suffix": (suffix or "").lstrip("-"),
            "failure": report.get("failure"),
            "marginal": report.get("marginal_vram_mib"),
            "spill": spill,
            "idle_ded": idle["vram_dedicated_mib"] if idle else None,
            "peak_ded": peak["vram_dedicated_mib"] if peak else None,
            "peak_shr": peak["vram_shared_mib"] if peak else None,
            "commit": peak["commit_gib"] if peak else None,
            "ws": peak["process_ws_gib"] if peak else None,
            "load_s": report.get("load_seconds"),
            "pressure": (report.get("gpu_pressure") or {}).get("state"),
            "started": report.get("started_utc") or "",
        })
    return rows


def admissible(row, cap):
    if row["failure"] or row["marginal"] is None:
        return False
    if row["spill"] is not None and row["spill"] > SHARED_MARGIN:
        return False
    return row["marginal"] <= cap


def section_footprint(rows):
    caps = {name: DEVICE - allow - RESERVE for name, allow in BUDGETS.items()}
    out = ["## Appendix A - Complete footprint census (%d cells)" % len(rows), "",
           "Every `--n-cpu-moe` cell measured in this campaign, taken at load with one",
           "server running at a time. **Marginal** is peak dedicated minus the idle",
           "baseline sampled immediately before the cell; it is the figure selection",
           "uses, and section 7.5 measures it reproducing to 0.16%. **Spill** is peak",
           "shared minus idle shared - above %d MiB the cell is paging and its marginal"
           % SHARED_MARGIN,
           "figure is not meaningful (section 9.2), so such cells are marked and",
           "excluded rather than ranked.",
           "",
           "**D** marks the cell selected for the `dedicated` set (marginal <= %d MiB),"
           % caps["dedicated"],
           "**W** for `workstation` (<= %d MiB). A cell can carry both." % caps["workstation"],
           ""]
    groups = {}
    for row in rows:
        groups.setdefault((row["model"], row["ctx"], row["ctk"]), []).append(row)

    for model in ORDER:
        for ctx in (32768, 131072, 262144):
            for cache in ("q4_0", "q8_0"):
                key = (model, ctx, cache)
                if key not in groups:
                    continue
                group = sorted(groups[key], key=lambda r: (r["n"], r["started"]))
                # Five placements were measured twice without either run being
                # labelled a repeat, because Phase 1 and the budget search wrote
                # different file names for the same cell. Order by time and mark
                # only the first, so a second reading cannot silently become a
                # second selected cell.
                seen = set()
                for row in group:
                    row["ordinal"] = len([1 for k in seen if k == row["n"]])
                    seen = list(seen) + [row["n"]]
                chosen = {}
                for name, cap in caps.items():
                    for row in group:
                        if not row["suffix"] and row["ordinal"] == 0 and admissible(row, cap):
                            chosen[name] = row["n"]
                            break
                out.append("### %s | %s tokens | KV `%s`" % (
                    PRETTY[model], format(ctx, ","), cache))
                out.append("")
                out.append("| | `n_cpu_moe` | marginal | spill | idle ded | peak ded "
                           "| peak shr | commit | WS | load | pressure |")
                out.append("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|")
                for row in group:
                    marks = ""
                    if not row["suffix"] and row["ordinal"] == 0:
                        if chosen.get("dedicated") == row["n"]:
                            marks += "**D**"
                        if chosen.get("workstation") == row["n"]:
                            marks += "**W**"
                    label = str(row["n"])
                    if row["suffix"]:
                        label += " *(%s)*" % row["suffix"]
                    elif row["ordinal"]:
                        label += " *(reading %d)*" % (row["ordinal"] + 1)
                    if row["failure"]:
                        out.append("| %s | %s | - | - | - | - | - | - | - | - | "
                                   "**load failed** |" % (marks, label))
                        continue
                    flag = " !" if (row["spill"] or 0) > SHARED_MARGIN else ""
                    out.append("| %s | %s | %s | %s%s | %s | %s | %s | %s | %s | %s | %s |" % (
                        marks, label,
                        fmt(row["marginal"]), fmt(row["spill"], "%.0f"), flag,
                        fmt(row["idle_ded"]), fmt(row["peak_ded"]), fmt(row["peak_shr"]),
                        fmt(row["commit"], "%.2f"), fmt(row["ws"], "%.2f"),
                        fmt(row["load_s"], "%.1f s"), row["pressure"] or "-"))
                out.append("")
    return out


def throughput_table(pattern, title, note_lines):
    files = sorted(glob.glob(pattern))
    if not files:
        return []
    out = ["## %s" % title, ""] + note_lines + [""]
    out.append("| Run | Test | t/s | stddev | rel. sd | wall | peak ded | peak shr | WS |")
    out.append("|---|---|---:|---:|---:|---:|---:|---:|---:|")
    for path in files:
        report = load(path)
        label = report.get("label") or os.path.basename(path)
        for result in report.get("results", []):
            peak = result.get("peak") or {}
            tps = ("**timeout**" if result.get("timed_out")
                   else fmt(result.get("tokens_per_second"), "%.2f"))
            out.append("| `%s` | %s | %s | %s | %s | %s | %s | %s | %s |" % (
                label, result.get("tag", "").split("-")[-1], tps,
                fmt(result.get("stddev"), "%.2f"),
                fmt(result.get("relative_stddev_percent"), "%.1f%%"),
                fmt(result.get("wall_s"), "%.1f s"),
                fmt(peak.get("vram_dedicated_mib"), "%.0f"),
                fmt(peak.get("vram_shared_mib"), "%.0f"),
                fmt(peak.get("process_ws_gib"), "%.2f")))
    out.append("")
    return out


def section_loadmode():
    path = "loadmode/loadmode-ab.json"
    if not os.path.exists(path):
        return []
    report = load(path)
    config = report["configuration"]
    out = ["## Appendix D - `--load-mode` A/B", "",
           "%s, `n_cpu_moe %d`, KV `%s`, %d repetitions. Same binary, same cell, one flag."
           % (os.path.basename(config["model_path"]), config["n_cpu_moe"],
              config["cache_type_k"], config["repetitions"]), ""]
    bench, load_rows = {}, {}
    for result in report["results"]:
        if result["measurement"] == "server-load":
            load_rows[result["mode"]] = result
        else:
            bench.setdefault(result["measurement"], {})[result["mode"]] = result
    out.append("| Measurement | `auto` | `none` | change |")
    out.append("|---|---:|---:|---:|")
    for measurement in ("pp512", "pp8192", "tg128"):
        pair = bench.get(measurement, {})
        a, b = pair.get("auto"), pair.get("none")
        if not a or not b:
            continue
        delta = (b["tokens_per_second"] / a["tokens_per_second"] - 1.0) * 100.0
        out.append("| %s | %.2f +/- %.2f | %.2f +/- %.2f | **%+.0f%%** |" % (
            measurement, a["tokens_per_second"], a["stddev"],
            b["tokens_per_second"], b["stddev"], delta))
    out.append("")
    if load_rows.get("auto") and load_rows.get("none"):
        a, b = load_rows["auto"], load_rows["none"]
        out.append("| Cost | `auto` | `none` |")
        out.append("|---|---:|---:|")
        out.append("| load | %.1f s | %.1f s |" % (a["load_seconds"], b["load_seconds"]))
        out.append("| peak dedicated | %.0f MiB | %.0f MiB |" % (
            a["peak"]["vram_dedicated_mib"], b["peak"]["vram_dedicated_mib"]))
        out.append("| peak shared | %.0f MiB | %.0f MiB |" % (
            a["peak"]["vram_shared_mib"], b["peak"]["vram_shared_mib"]))
        out.append("| private commit | %.2f GiB | %.2f GiB |" % (
            a["peak"]["process_private_gib"], b["peak"]["process_private_gib"]))
        out.append("| working set | %.2f GiB | %.2f GiB |" % (
            a["peak"]["process_ws_gib"], b["peak"]["process_ws_gib"]))
        out.append("")
    return out


def section_quality():
    files = sorted(glob.glob("quality/qualify-*.json"))
    if not files:
        return []
    reports = {}
    for path in files:
        reports[os.path.basename(path).split("-")[1]] = load(path)
    present = [m for m in ORDER if m in reports]
    if not present:
        return []

    coding_ids = [c["id"] for c in reports[present[0]]["suites"]["coding"]["results"]]
    tool_ids = [c["id"] for c in reports[present[0]]["suites"]["tools"]["results"]]
    header = "| Task | " + " | ".join(PRETTY[m] for m in present) + " |"
    divider = "|---" * (1 + len(present)) + "|"

    out = ["## Appendix E - Phase 4, task by task", "",
           "Default system policy and token budget, so the grades are comparable to the",
           "Qwen3.8 grading. Reasoning characters are printed beside each coding verdict",
           "because section 10 attributes Q3_K_XL's two losses to reasoning overrun",
           "rather than to weight quality, and these counts are that argument's evidence.",
           "", "### Coding suite", "", header, divider]
    for task in coding_ids:
        cells = []
        for model in present:
            case = next(c for c in reports[model]["suites"]["coding"]["results"]
                        if c["id"] == task)
            mark = "PASS" if case["passed"] else "**FAIL**"
            flags = []
            if case.get("truncated"):
                flags.append("truncated")
            if case.get("no_answer"):
                flags.append("no answer")
            if flags:
                mark += " (%s)" % ", ".join(flags)
            cells.append("%s - %s ch" % (mark, format(case.get("reasoning_chars", 0), ",")))
        out.append("| `%s` | %s |" % (task, " | ".join(cells)))
    out.append("")

    out += ["### Tool-selection suite", "", header, divider]
    for task in tool_ids:
        cells = []
        for model in present:
            case = next(c for c in reports[model]["suites"]["tools"]["results"]
                        if c["id"] == task)
            cells.append("PASS" if case["passed"] else "**FAIL**")
        out.append("| `%s` | %s |" % (task, " | ".join(cells)))
    out.append("")

    out += ["### JSON adherence", "", header, divider]
    row = []
    for model in present:
        suite = reports[model]["suites"]["json"]
        row.append("%s - parsed %s, clean %s" % (
            "PASS" if suite["passed"] else "**FAIL**",
            suite["parsed"], suite["clean_json"]))
    out.append("| `structured_json` | %s |" % " | ".join(row))
    out.append("")
    return out


def section_contract():
    path = "contract/edge-contract-direct.json"
    if not os.path.exists(path):
        return []
    report = load(path)
    out = ["## Appendix F - Serving-contract probes, direct port", "",
           "`%s`, alias `%s`. %s" % (report["base_url"], report["alias"],
                                     report.get("label", "")), "",
           "| Probe | Result | seconds | detail |", "|---|---|---:|---|"]
    for name, probe in report["probes"].items():
        if probe.get("informational"):
            verdict = "INFO"
        else:
            verdict = "PASS" if probe.get("passed") else "**FAIL**"
        detail = probe.get("note") or probe.get("error") or ""
        out.append("| `%s` | %s | %s | %s |" % (
            name, verdict, fmt(probe.get("seconds"), "%.1f"), detail[:130]))
    out.append("")
    out.append("**%s - %d of %d graded probes passed.** The three informational probes"
               % (report["verdict"], report["passed"], report["graded"]))
    out.append("record behaviour that is not this model's to pass or fail.")
    out.append("")
    return out


def section_selection():
    path = "summary-budget-search.json"
    if not os.path.exists(path):
        return []
    report = load(path)
    out = ["## Appendix G - Two-budget placement search, with search traces", "",
           "%d rows; %d cells newly measured and %d reused. Each trace lists every"
           % (len(report["results"]), report["cells_measured"], report["cells_reused"]),
           "placement tried in ascending order - `nN:marginal` for a cell that was read,",
           "`nN:spillX` for one discarded before its marginal could be. The search stops",
           "at the first admissible placement, because more experts on the device is",
           "better for decode (section 8.3).", ""]
    for budget in sorted({r["budget"] for r in report["results"]}):
        allowance = report["budgets"][budget]
        out += ["### `%s` - desktop %d MiB + reserve %d MiB, marginal <= %d MiB" % (
            budget, allowance, report["vram_reserve_mib"],
            report["device_vram_mib"] - allowance - report["vram_reserve_mib"]), "",
            "| Model | Context | KV | chosen | marginal | spill | commit | trace |",
            "|---|---:|---|---:|---:|---:|---:|---|"]
        for row in report["results"]:
            if row["budget"] != budget:
                continue
            out.append("| %s | %s | `%s` | **%s** | %s | %s | %s | <sub>%s</sub> |" % (
                PRETTY.get(row["model"], row["model"]), format(row["context"], ","),
                row["ctk"],
                "n=%s" % row["ncpumoe"] if row["ncpumoe"] is not None else "NONE",
                fmt(row["marginal_mib"]), fmt(row["spill_mib"], "%.0f"),
                fmt(row["commit_gib"], "%.2f GiB"), row["trace"]))
        out.append("")
    return out


def section_bistability():
    path = "summary-spill-bistability.json"
    if not os.path.exists(path):
        return []
    report = load(path)
    out = ["## Appendix H - Repeat readings of the two decision cells", "",
           report["finding"], ""]
    for name, cell in report["decision_cells"].items():
        out += ["### `%s`" % name, "", "*%s*" % cell["question"], "",
                "| Placement | started | load | spill | marginal | idle shared |",
                "|---|---|---:|---:|---:|---:|"]
        for key in ("n18", "n17", "n14"):
            for reading in cell.get(key, []):
                out.append("| `%s` | %s | %s | %s | %s | %s |" % (
                    key, reading["started_utc"][11:19],
                    fmt(reading["load_seconds"], "%.1f s"),
                    fmt(reading["spill_mib"], "%.0f"),
                    fmt(reading["marginal_vram_mib"]),
                    fmt(reading["idle_shared_mib"], "%.0f")))
        out += ["", "**Verdict.** %s" % cell["verdict"]]
        if cell.get("n17_note"):
            out += ["", cell["n17_note"]]
        out.append("")
    return out


def section_repeatability(rows):
    """Placements measured twice, and what the two readings agree to.

    Section 7.5 quotes marginal VRAM reproducing to 0.16% from a single pair.
    Four pairs now support it. The fifth disagrees by 10%, and the reason is the
    one section 7.1 documents rather than a reproducibility failure: one of its
    two readings is spilling 1284 MiB, and a spilling cell's marginal *falls* as
    the configuration gets worse. It is listed here rather than dropped.
    """
    groups = {}
    for row in rows:
        if row["suffix"] or row["marginal"] is None:
            continue
        groups.setdefault((row["model"], row["ctx"], row["ctk"], row["n"]), []).append(row)
    pairs = {k: sorted(v, key=lambda r: r["started"])
             for k, v in groups.items() if len(v) > 1}
    if not pairs:
        return []
    out = ["## Appendix I - Placements measured more than once", "",
           "%d placements were measured twice, because Phase 1 and the budget search"
           % len(pairs),
           "wrote different file names for the same cell. That accident is useful: it",
           "gives independent readings of the same configuration hours apart.", "",
           "| Cell | readings | marginal | spread | spill |", "|---|---:|---|---:|---|"]
    for key in sorted(pairs):
        rows_ = pairs[key]
        values = [r["marginal"] for r in rows_]
        spread = (max(values) - min(values)) / min(values) * 100.0
        spills = " / ".join(fmt(r["spill"], "%.0f") for r in rows_)
        flag = " !" if any((r["spill"] or 0) > SHARED_MARGIN for r in rows_) else ""
        out.append("| %s ctx %s `%s` n%d | %d | %s | **%.2f%%**%s | %s |" % (
            PRETTY.get(key[0], key[0]), format(key[1], ","), key[2], key[3],
            len(rows_), " / ".join("%.1f" % v for v in values), spread, flag, spills))
    out += ["",
            "The four pairs in which neither reading spills agree to 0.13-0.16%, which",
            "is the figure section 7.5 relies on. The `UD-Q3_K_S ctx 32,768 q4_0 n6` pair",
            "disagrees by 9.98%, and its second reading is spilling 1284 MiB while the",
            "first spills 100 MiB. Its *lower* marginal is the inversion section 7.1",
            "describes - part of the allocation moved to shared, so less dedicated memory",
            "was charged for a worse configuration. It is evidence for the spill filter,",
            "not against the repeatability claim.", ""]
    return out


def main():
    blocks = []
    blocks += section_footprint(footprint_rows())
    blocks += throughput_table(
        "throughput/throughput-*.json", "Appendix B - Complete throughput results",
        ["Phase 2 plus the two contaminated-cell re-runs. `llama-bench` derives its",
         "context from `-p + -n + -d` and has no `--ctx-size`, so every figure here is",
         "short-context. None of it is decode against a filled window."])
    blocks += throughput_table(
        "threads/throughput-*.json", "Appendix C - `--threads` A/B",
        ["Same cell, same binary; 8 against 16 threads on an 8-core / 16-thread part."])
    blocks += section_loadmode()
    blocks += section_quality()
    blocks += section_contract()
    blocks += section_selection()
    blocks += section_bistability()
    blocks += section_repeatability(footprint_rows())
    sys.stdout.write("\n".join(blocks) + "\n")


if __name__ == "__main__":
    main()
