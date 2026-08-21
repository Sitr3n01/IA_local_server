#!/usr/bin/env python3
"""Qualification battery against a running llama-server.

Talks HTTP to a server someone else started, so the same battery can be pointed
at any (weights, KV, context, split) cell without this script knowing how the
cell was configured. It never starts or stops a server.

Four suites, in the order a failure is cheapest to discover:

  coding     compiled or executed by a real toolchain, never judged
  tools      exact function name and exact argument values
  json       strict structured output
  retention  one long prefill at a requested occupancy, answering every
             long-context probe in a single JSON reply

The retention suite is deliberately one prefill rather than one per probe. At
256k a prefill costs tens of minutes, and asking twelve questions of the same
filled window measures the same thing twelve times more expensively. It also
yields the numbers section 14 of the campaign asks for that nothing else does:
decode throughput with the context actually occupied, and cold TTFT at that
occupancy, both read from the server's own `timings` block rather than derived.
"""
import argparse
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import coding_tasks as CT           # noqa: E402
import fixture_corpus as FC         # noqa: E402


# --------------------------------------------------------------------------
# transport
# --------------------------------------------------------------------------

class Server:
    def __init__(self, base_url, alias, timeout=1800):
        self.base = base_url.rstrip("/")
        self.alias = alias
        self.timeout = timeout

    def _post(self, path, payload, timeout=None):
        data = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(
            self.base + path, data=data,
            headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=timeout or self.timeout) as resp:
            return json.loads(resp.read().decode("utf-8"))

    def wait_ready(self, seconds=900):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            try:
                with urllib.request.urlopen(self.base + "/health", timeout=5) as resp:
                    if json.loads(resp.read().decode("utf-8")).get("status") == "ok":
                        return True
            except Exception:
                time.sleep(2)
        return False

    def count_tokens(self, text):
        return len(self._post("/tokenize", {"content": text}, timeout=600)["tokens"])

    def props(self):
        try:
            with urllib.request.urlopen(self.base + "/props", timeout=30) as resp:
                return json.loads(resp.read().decode("utf-8"))
        except Exception:
            return {}

    def chat(self, messages, max_tokens=1024, temperature=0.0, tools=None,
             timeout=None, seed=20260821):
        payload = {
            "model": self.alias,
            "messages": messages,
            "max_tokens": max_tokens,
            "temperature": temperature,
            "top_p": 1.0,
            "seed": seed,
            "stream": False,
            "timings_per_token": False,
        }
        if tools:
            payload["tools"] = tools
            payload["tool_choice"] = "auto"
        started = time.monotonic()
        out = self._post("/v1/chat/completions", payload, timeout=timeout)
        wall = time.monotonic() - started
        choice = (out.get("choices") or [{}])[0]
        message = choice.get("message") or {}
        timings = out.get("timings") or {}
        # llama.cpp routes a thinking model's chain of thought into
        # reasoning_content and leaves content empty until the answer starts. A
        # generation that hits max_tokens while still reasoning therefore
        # returns an empty answer, which a grader reads as broken code unless
        # the two are told apart here.
        reasoning = message.get("reasoning_content") or message.get("reasoning") or ""
        return {
            "content": message.get("content") or "",
            "reasoning_chars": len(reasoning),
            "tool_calls": message.get("tool_calls") or [],
            "finish_reason": choice.get("finish_reason"),
            "usage": out.get("usage") or {},
            "wall_s": round(wall, 2),
            "timings": {
                "prompt_n": timings.get("prompt_n"),
                "prompt_ms": timings.get("prompt_ms"),
                "prompt_per_second": timings.get("prompt_per_second"),
                "predicted_n": timings.get("predicted_n"),
                "predicted_ms": timings.get("predicted_ms"),
                "predicted_per_second": timings.get("predicted_per_second"),
                "cache_n": timings.get("cache_n"),
            },
        }


# --------------------------------------------------------------------------
# scoring helpers
# --------------------------------------------------------------------------

JSON_OBJ_RE = re.compile(r"\{.*\}", re.S)


def parse_json_reply(text):
    """Recover a JSON object from a reply that may be fenced or prefaced.

    Grading a structured-output failure as a retention failure would confuse two
    different defects, so this is permissive about packaging and strict about
    content: the caller still sees whether the reply was clean JSON.
    """
    clean = CT.strip_reasoning(text)
    fenced = CT.FENCE_RE.findall(clean)
    candidates = [body for _, body in fenced] + [clean]
    for cand in candidates:
        cand = cand.strip()
        try:
            return json.loads(cand), True
        except Exception:
            pass
    for cand in candidates:
        match = JSON_OBJ_RE.search(cand)
        if match:
            try:
                return json.loads(match.group(0)), False
            except Exception:
                continue
    return None, False


def norm(value):
    if value is None:
        return ""
    return re.sub(r"\s+", " ", str(value)).strip().strip('.,;:"\'').lower()


def grade_probe(probe, value):
    """Return one of exact / semantic / partial / incorrect / hallucinated / missing.

    The campaign asks for nuance rather than PASS/FAIL, and the distinction that
    matters operationally is between a model that says UNKNOWN (recoverable: the
    agent can go look) and one that returns a confident wrong value
    (unrecoverable: the agent acts on it). Those are `missing` and `hallucinated`
    and they are never collapsed together.
    """
    if value is None:
        return "missing"
    raw = str(value).strip()
    if norm(value) in ("unknown", "", "n/a", "none", "null"):
        return "missing"

    kind = probe["kind"]
    if kind == "number":
        found = re.search(r"-?\d+", raw)
        if not found:
            return "incorrect"
        return "exact" if int(found.group(0)) == probe["expect"] else "hallucinated"

    if kind == "phrase":
        hits = sum(1 for term in probe["expect"] if term.lower() in raw.lower())
        if hits == len(probe["expect"]):
            return "exact"
        return "partial" if hits else "incorrect"

    want = probe["expect"]
    if raw == want:
        return "exact"
    if norm(raw) == norm(want):
        return "semantic"
    # A stale value is the mutable-state failure specifically, and is worth
    # separating from an arbitrary wrong answer.
    if probe.get("stale") and norm(probe["stale"]) in norm(raw):
        return "stale"
    if norm(want) in norm(raw):
        return "partial"
    return "hallucinated"


GRADE_CREDIT = {"exact": 1.0, "semantic": 1.0, "partial": 0.5,
                "stale": 0.0, "incorrect": 0.0, "hallucinated": 0.0,
                "missing": 0.0}


# --------------------------------------------------------------------------
# suites
# --------------------------------------------------------------------------

def run_coding(server, workdir, only=None):
    results = []
    for task in CT.TASKS:
        if only and task["id"] not in only:
            continue
        started = time.monotonic()
        try:
            reply = server.chat(
                [{"role": "system", "content": task["system"]},
                 {"role": "user", "content": task["prompt"]}],
                max_tokens=task["max_tokens"], timeout=900)
        except Exception as exc:
            results.append({"id": task["id"], "family": task["family"],
                            "lang": task["lang"], "passed": False,
                            "error": "request failed: %s" % exc})
            continue

        code = CT.extract_code(reply["content"], task["langs"], task["pick"])
        truncated = reply["finish_reason"] == "length"
        no_answer = truncated and not code.strip()
        constraint_ok = True
        violated = []
        for marker in task.get("forbidden_markers", []):
            if marker in code:
                constraint_ok = False
                violated.append(marker)

        if no_answer:
            # Nothing was emitted to grade. Running a compiler on the empty
            # string would record a compile failure that the model never had a
            # chance to cause.
            passed, detail = False, ("no answer: generation stopped at the output "
                                     "cap with %d chars still in reasoning_content"
                                     % reply["reasoning_chars"])
        else:
            try:
                passed, detail = task["verify"](code, workdir)
            except Exception as exc:
                passed, detail = False, "verifier crashed: %s" % exc

        results.append({
            "id": task["id"], "family": task["family"], "lang": task["lang"],
            "passed": bool(passed and constraint_ok),
            "compiled_or_ran": bool(passed),
            "constraint_respected": constraint_ok,
            "violated_markers": violated,
            "truncated": truncated,
            "no_answer": no_answer,
            "reasoning_chars": reply["reasoning_chars"],
            "finish_reason": reply["finish_reason"],
            "output_tokens": reply["usage"].get("completion_tokens"),
            "decode_tps": reply["timings"].get("predicted_per_second"),
            "wall_s": round(time.monotonic() - started, 1),
            "detail": detail[-700:] if isinstance(detail, str) else str(detail),
            "code": code,
        })
        verdict = "PASS" if results[-1]["passed"] else ("NO-ANSWER" if no_answer else "FAIL")
        print("  coding %-24s %-9s (%d out tok, %d reasoning chars)"
              % (task["id"], verdict, results[-1]["output_tokens"] or 0,
                 reply["reasoning_chars"]), flush=True)
    return results


def _arg_match(got, want):
    """Argument comparison that accepts string-encoded scalars.

    A model that emits {"verbose": "true"} has selected the right tool with the
    right intent and a loose serialization; treating that as the same failure as
    calling the wrong tool would make the tool-calling score unreadable.
    """
    mismatches = []
    for key, expected in want.items():
        if key not in got:
            mismatches.append("missing:%s" % key)
            continue
        actual = got[key]
        if isinstance(expected, bool):
            ok = (actual is True) if expected else (actual is False)
            if not ok and isinstance(actual, str):
                ok = actual.strip().lower() == str(expected).lower()
        elif isinstance(expected, int):
            try:
                ok = int(str(actual).strip()) == expected
            except Exception:
                ok = False
        else:
            ok = norm(actual) == norm(expected)
        if not ok:
            mismatches.append("%s=%r want %r" % (key, actual, expected))
    return (not mismatches), mismatches


def run_tools(server):
    results = []
    for task in CT.TOOL_TASKS:
        try:
            reply = server.chat(
                [{"role": "system", "content":
                  "You are a coding agent. When a tool can do what the user "
                  "asked, call it. Do not describe the call in prose."},
                 {"role": "user", "content": task["prompt"]}],
                max_tokens=4096, tools=CT.TOOLS, timeout=900)
        except Exception as exc:
            results.append({"id": task["id"], "passed": False,
                            "error": "request failed: %s" % exc})
            continue

        calls = reply["tool_calls"]
        row = {"id": task["id"], "call_count": len(calls),
               "name_ok": False, "args_ok": False, "json_ok": False,
               "forbidden_called": False, "passed": False,
               "detail": ""}
        if not calls:
            row["detail"] = "no tool call; content=%s" % (
                CT.strip_reasoning(reply["content"])[:200])
        else:
            names = [c.get("function", {}).get("name") for c in calls]
            row["names"] = names
            if task.get("forbid_name") and task["forbid_name"] in names:
                row["forbidden_called"] = True
            first = calls[0].get("function", {})
            row["name_ok"] = first.get("name") == task["want_name"]
            raw_args = first.get("arguments")
            try:
                args = json.loads(raw_args) if isinstance(raw_args, str) else (raw_args or {})
                row["json_ok"] = True
            except Exception:
                args = {}
                row["detail"] = "unparseable arguments: %r" % (raw_args,)[:200]
            if row["json_ok"]:
                row["args_ok"], mismatches = _arg_match(args, task["want_args"])
                if mismatches:
                    row["detail"] = "; ".join(mismatches)[:300]
            row["passed"] = bool(row["name_ok"] and row["args_ok"]
                                 and row["json_ok"] and not row["forbidden_called"])
        results.append(row)
        print("  tool   %-24s %s" % (task["id"], "PASS" if row["passed"] else "FAIL"),
              flush=True)
    return results


def run_json(server):
    task = CT.JSON_TASK
    try:
        reply = server.chat(
            [{"role": "system", "content": "Return only JSON. No prose, no fence."},
             {"role": "user", "content": task["prompt"]}],
            max_tokens=4096, timeout=900)
    except Exception as exc:
        return {"id": task["id"], "passed": False, "error": str(exc)}

    obj, clean = parse_json_reply(reply["content"])
    row = {"id": task["id"], "parsed": obj is not None, "clean_json": clean,
           "passed": False, "mismatches": []}
    if obj is not None:
        want = task["want"]
        bad = []
        for key, expected in want.items():
            actual = obj.get(key)
            if isinstance(expected, list):
                ok = [norm(x) for x in (actual or [])] == [norm(x) for x in expected]
            elif isinstance(expected, bool):
                ok = actual is expected or norm(actual) == norm(expected)
            elif isinstance(expected, int):
                try:
                    ok = int(str(actual).strip()) == expected
                except Exception:
                    ok = False
            else:
                ok = norm(actual) == norm(expected)
            if not ok:
                bad.append("%s=%r" % (key, actual))
        row["mismatches"] = bad
        row["passed"] = not bad
    print("  json   %-24s %s" % (task["id"], "PASS" if row["passed"] else "FAIL"), flush=True)
    return row


def build_corpus_for(server, target_tokens, question_tokens_guess=600, seed=20260821):
    """Size the briefing so prompt tokens land just under `target_tokens`.

    Two calibration passes, because the filler's tokens-per-record is stable but
    not known in advance and differs slightly between tokenizers.
    """
    units = max(8, target_tokens // 60)
    measured = None
    for _ in range(3):
        doc = FC.build_corpus(units, seed=seed)
        measured = server.count_tokens(doc)
        per_unit = measured / float(units)
        room = target_tokens - question_tokens_guess
        if abs(measured - room) <= max(512, room * 0.01):
            break
        units = max(8, int(room / per_unit))
    doc = FC.build_corpus(units, seed=seed)
    return doc, units, server.count_tokens(doc)


def run_retention(server, target_tokens, output_reserve=4096, seed=20260821):
    print("  retention: sizing corpus for ~%d prompt tokens" % target_tokens, flush=True)
    doc, units, doc_tokens = build_corpus_for(server, target_tokens, seed=seed)
    prompt = doc + FC.QUESTION_BLOCK
    total_tokens = server.count_tokens(prompt)
    print("  retention: %d filler units, %d prompt tokens; prefilling" %
          (units, total_tokens), flush=True)

    started = time.monotonic()
    try:
        reply = server.chat(
            [{"role": "system", "content":
              "You are a coding agent reading a project briefing. Answer only "
              "from the briefing. Never guess a value you did not read."},
             {"role": "user", "content": prompt}],
            max_tokens=output_reserve, timeout=7200)
    except Exception as exc:
        return {"target_tokens": target_tokens, "prompt_tokens": total_tokens,
                "failure": "request failed: %s" % exc}

    obj, clean = parse_json_reply(reply["content"])
    probes = []
    if obj is None:
        for probe in FC.PROBES:
            probes.append({"key": probe["key"], "family": probe["family"],
                           "depth": probe["depth"], "grade": "missing"})
    else:
        for probe in FC.PROBES:
            grade = grade_probe(probe, obj.get(probe["key"]))
            probes.append({"key": probe["key"], "family": probe["family"],
                           "depth": probe["depth"], "grade": grade,
                           "got": obj.get(probe["key"])})

    by_family = {}
    for row in probes:
        bucket = by_family.setdefault(row["family"], {"n": 0, "credit": 0.0})
        bucket["n"] += 1
        bucket["credit"] += GRADE_CREDIT[row["grade"]]
    for bucket in by_family.values():
        bucket["score"] = round(bucket["credit"] / bucket["n"], 3)

    timings = reply["timings"]
    return {
        "target_tokens": target_tokens,
        "filler_units": units,
        "corpus_tokens": doc_tokens,
        "prompt_tokens": total_tokens,
        "corpus_sha256": FC.corpus_fingerprint(prompt),
        "clean_json": clean,
        "parsed": obj is not None,
        "probes": probes,
        "by_family": by_family,
        "score": round(sum(GRADE_CREDIT[p["grade"]] for p in probes) / len(probes), 3),
        "exact_count": sum(1 for p in probes if p["grade"] == "exact"),
        "hallucinated_count": sum(1 for p in probes if p["grade"] == "hallucinated"),
        "stale_count": sum(1 for p in probes if p["grade"] == "stale"),
        # These three are the section 14 and 16 numbers, read from the server
        # rather than divided out of a rate it already reported.
        "ttft_prefill_ms": timings.get("prompt_ms"),
        "prefill_tps": timings.get("prompt_per_second"),
        "decode_tps_at_occupancy": timings.get("predicted_per_second"),
        "decoded_tokens": timings.get("predicted_n"),
        "processed_prompt_tokens": timings.get("prompt_n"),
        "cache_n": timings.get("cache_n"),
        "wall_s": round(time.monotonic() - started, 1),
        "finish_reason": reply["finish_reason"],
        "truncated": reply["finish_reason"] == "length",
        "reasoning_chars": reply["reasoning_chars"],
        "raw_reply": CT.strip_reasoning(reply["content"])[:4000],
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base-url", default="http://127.0.0.1:19399")
    ap.add_argument("--alias", default="local")
    ap.add_argument("--label", required=True)
    ap.add_argument("--workdir", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--suites", default="coding,tools,json",
                    help="comma-separated: coding,tools,json,retention")
    ap.add_argument("--retention-tokens", type=int, action="append", default=[])
    ap.add_argument("--only", default="", help="restrict the coding suite to these ids")
    args = ap.parse_args()

    os.makedirs(args.workdir, exist_ok=True)
    server = Server(args.base_url, args.alias)
    if not server.wait_ready(900):
        print("server never became ready", file=sys.stderr)
        return 2

    suites = [s.strip() for s in args.suites.split(",") if s.strip()]
    only = set(x.strip() for x in args.only.split(",") if x.strip())

    report = {
        "schema_version": 1,
        "scenario": "profile-qualification",
        "label": args.label,
        "started_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "server_props": {k: v for k, v in (server.props() or {}).items()
                         if k in ("n_ctx", "model_path", "default_generation_settings",
                                  "total_slots", "build_info", "chat_template")
                         and k != "chat_template"},
        "suites": {},
    }

    if "coding" in suites:
        print("[coding]", flush=True)
        rows = run_coding(server, args.workdir, only or None)
        report["suites"]["coding"] = {
            "results": rows,
            "passed": sum(1 for r in rows if r.get("passed")),
            "total": len(rows),
            "compiled": sum(1 for r in rows if r.get("compiled_or_ran")),
            "no_answer": sum(1 for r in rows if r.get("no_answer")),
            "truncated": sum(1 for r in rows if r.get("truncated")),
        }

    if "tools" in suites:
        print("[tools]", flush=True)
        rows = run_tools(server)
        report["suites"]["tools"] = {
            "results": rows,
            "passed": sum(1 for r in rows if r.get("passed")),
            "total": len(rows),
        }

    if "json" in suites:
        print("[json]", flush=True)
        report["suites"]["json"] = run_json(server)

    if "retention" in suites:
        print("[retention]", flush=True)
        levels = args.retention_tokens or [30000]
        report["suites"]["retention"] = [run_retention(server, t) for t in levels]

    with open(args.out, "w", encoding="utf-8") as handle:
        json.dump(report, handle, indent=2, default=str)
    print("wrote %s" % args.out)
    return 0


if __name__ == "__main__":
    sys.exit(main())
