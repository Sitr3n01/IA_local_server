#!/usr/bin/env python3
"""Collapse the head-to-head's raw JSON into the tables the report needs.

Reads whatever exists and says so, rather than assuming a complete run: an
interrupted campaign should still produce a readable partial answer, and a cell
that failed should be visible as a failed cell rather than as a missing row.

  python summarize.py               everything
  python summarize.py contract      one section
"""
import glob
import json
import os
import sys
from collections import defaultdict

HERE = os.path.dirname(os.path.abspath(__file__))


def load(path):
    try:
        with open(path, encoding="utf-8-sig") as handle:
            return json.load(handle)
    except Exception as error:
        print(f"  !! unreadable {os.path.basename(path)}: {error}")
        return None


def suite_results(payload, suite):
    body = (payload.get("suites") or {}).get(suite)
    if not isinstance(body, dict):
        return []
    # The json suite records one flat result rather than a `results` list.
    # Without this it reads as an empty suite and silently leaves a row out of
    # the comparison table.
    if "results" not in body and "id" in body:
        return [body]
    return body.get("results") or []


def request_errors(payload):
    """Cases the client could not complete -- a dead server, not a bad answer."""
    bad = 0
    for suite in (payload.get("suites") or {}):
        for case in suite_results(payload, suite):
            blob = json.dumps(case)
            if "REQUEST_ERROR" in blob or case.get("request_error"):
                bad += 1
    return bad


# --------------------------------------------------------------------------
def section_contract():
    print("=" * 74)
    print("A. CONTRACT GATE")
    print("=" * 74)
    for path in sorted(glob.glob(os.path.join(HERE, "contract", "contract-*.json"))):
        payload = load(path)
        if payload is None:
            continue
        probes = payload.get("probes") or {}
        graded = [(k, v) for k, v in probes.items() if not v.get("informational")]
        passed = sum(1 for _, v in graded if v.get("passed"))
        name = os.path.basename(path).replace("contract-", "").replace(".json", "")
        print(f"\n{name}")
        print(f"  verdict {payload.get('verdict')}   graded {passed}/{len(graded)}"
              f"   (+{len(probes) - len(graded)} informational)")
        for key, value in probes.items():
            if value.get("passed") and not value.get("error"):
                continue
            mark = "INFO" if value.get("informational") else "FAIL"
            detail = value.get("error") or f"status={value.get('status')}"
            print(f"    {mark}  {key:26s} {str(detail)[:90]}")
        dev = probes.get("developer_role") or {}
        print(f"    developer_role: status={dev.get('status')} "
              f"honoured={dev.get('honoured')}")


# --------------------------------------------------------------------------
def section_quality():
    print()
    print("=" * 74)
    print("B. QUALITY  (three seeds per model, 128k)")
    print("=" * 74)
    per_model = defaultdict(list)
    for path in sorted(glob.glob(os.path.join(HERE, "quality", "qualify-*.json"))):
        payload = load(path)
        if payload is None:
            continue
        label = payload.get("label") or os.path.basename(path)
        model = "ornith15-iq2m" if "ornith" in label else "qwen36-q2kxl"
        per_model[model].append((label, payload))

    # per-case tallies, so a case that only one model misses is visible
    case_tally = defaultdict(lambda: defaultdict(lambda: [0, 0]))
    for model, runs in sorted(per_model.items()):
        print(f"\n--- {model}  ({len(runs)} run(s))")
        for label, payload in runs:
            errors = request_errors(payload)
            flag = f"  [{errors} REQUEST_ERROR -- cell is not usable]" if errors else ""
            line = []
            for suite in ("chat", "coding", "hard", "tools", "literal_tools", "json"):
                results = suite_results(payload, suite)
                if not results:
                    continue
                good = sum(1 for c in results if c.get("passed"))
                line.append(f"{suite} {good}/{len(results)}")
                for case in results:
                    key = f"{suite}.{case.get('id')}"
                    case_tally[model][key][1] += 1
                    if case.get("passed"):
                        case_tally[model][key][0] += 1
            seed = label.rsplit("seed", 1)[-1]
            print(f"    seed {seed}: " + "  ".join(line) + flag)

    both = sorted(set(case_tally.get("qwen36-q2kxl", {})) |
                  set(case_tally.get("ornith15-iq2m", {})))
    # Compare pass RATES, not (pass, total) tuples: the two models can carry a
    # different number of usable seeds, and 0/2 against 0/3 is a tie, not a win.
    disagree, tied_fail = [], []
    for key in both:
        q = case_tally["qwen36-q2kxl"].get(key, [0, 0])
        o = case_tally["ornith15-iq2m"].get(key, [0, 0])
        if q[1] == 0 or o[1] == 0:
            continue
        q_rate, o_rate = q[0] / q[1], o[0] / o[1]
        if q_rate == o_rate:
            if q_rate < 1.0:
                tied_fail.append((key, q, o))
            continue
        disagree.append((key, q, o, q_rate, o_rate))

    print("\n--- cases the two models score differently")
    if not disagree:
        print("    none")
    score = {"qwen36": 0, "ornith": 0}
    for key, q, o, q_rate, o_rate in sorted(disagree, key=lambda r: -abs(r[3] - r[4])):
        winner = "qwen36" if q_rate > o_rate else "ornith"
        score[winner] += 1
        print(f"    {key:44s} qwen36 {q[0]}/{q[1]}   ornith {o[0]}/{o[1]}   -> {winner}")
    print(f"\n    discriminating cases: qwen36 {score['qwen36']}   ornith {score['ornith']}")

    print("\n--- cases BOTH models fail (not discriminating; a suite gap)")
    if not tied_fail:
        print("    none")
    for key, q, o in tied_fail:
        print(f"    {key:44s} qwen36 {q[0]}/{q[1]}   ornith {o[0]}/{o[1]}")


# --------------------------------------------------------------------------
def section_perf():
    print()
    print("=" * 74)
    print("D. PREFILL AND DECODE AT OCCUPANCY  (3 repetitions)")
    print("=" * 74)
    rows = []
    for path in sorted(glob.glob(os.path.join(HERE, "perf", "throughput-*.json"))):
        payload = load(path)
        if payload is None:
            continue
        label = payload.get("label") or os.path.basename(path)
        for row in (payload.get("results") or payload.get("rows") or []):
            rows.append((label, row))
    if not rows:
        print("  (no throughput reports yet)")
        return
    def fmt(value, spec):
        return format(value, spec) if isinstance(value, (int, float)) else "-"

    print(f"\n{'cell':38s} {'test':14s} {'t/s':>9s} {'rsd%':>5s} "
          f"{'VRAMded':>8s} {'VRAMshr':>8s} {'procWS':>7s} {'sysRAM':>7s}")
    by_test = {}
    for label, row in rows:
        kind, n, depth = row.get("kind"), row.get("n"), row.get("depth") or 0
        test = f"{kind}:{n}" + (f"@{depth}" if depth else "")
        peak = row.get("peak") or {}
        print(f"{label:38s} {test:14s} "
              f"{fmt(row.get('tokens_per_second'), '9.2f')} "
              f"{fmt(row.get('relative_stddev_percent'), '5.2f')} "
              f"{fmt(peak.get('vram_dedicated_mib'), '8.1f')} "
              f"{fmt(peak.get('vram_shared_mib'), '8.1f')} "
              f"{fmt(peak.get('process_ws_gib'), '7.2f')} "
              f"{fmt(peak.get('physical_used_gib'), '7.2f')}")
        if row.get("failure") or row.get("timed_out"):
            print(f"    !! failure={row.get('failure')} timed_out={row.get('timed_out')}")
        model = "ornith" if "ornith" in label else "qwen36"
        # Keyed by cell context as well as test: both cells run pp:8192 at
        # depth 0, so keying on the test alone silently drops one comparison.
        context = label.split("-ctx", 1)[-1].split("-", 1)[0]
        by_test.setdefault(f"{test} @ctx{context}", {})[model] = row.get("tokens_per_second")

    print("\n--- head to head (positive = Ornith faster)")
    for test, pair in by_test.items():
        q, o = pair.get("qwen36"), pair.get("ornith")
        if not (isinstance(q, (int, float)) and isinstance(o, (int, float)) and q):
            continue
        print(f"    {test:16s} qwen36 {q:9.2f}   ornith {o:9.2f}   {(o / q - 1) * 100:+6.1f}%")


# --------------------------------------------------------------------------
def main():
    wanted = sys.argv[1:] or ["contract", "quality", "perf"]
    if "contract" in wanted:
        section_contract()
    if "quality" in wanted:
        section_quality()
    if "perf" in wanted:
        section_perf()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
