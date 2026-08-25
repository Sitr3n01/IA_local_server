#!/usr/bin/env python3
"""Qualification battery against a running llama-server.

Talks HTTP to a server someone else started, so the same battery can be pointed
at any (weights, KV, context, split) cell without this script knowing how the
cell was configured. It never starts or stops a server.

Four suites, in the order a failure is cheapest to discover:

  chat       chat template, multi-turn, streaming and tool-result continuation
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
import copy
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
    def __init__(self, base_url, alias, timeout=1800, capture_dir=None):
        self.base = base_url.rstrip("/")
        self.alias = alias
        self.timeout = timeout
        self.capture_dir = capture_dir
        self.request_counter = 0

    def _capture(self, path, payload, raw_text, parsed, tag=None):
        if not self.capture_dir:
            return
        self.request_counter += 1
        safe_tag = re.sub(r"[^A-Za-z0-9_.-]+", "_", tag or path.strip("/") or "request")
        os.makedirs(self.capture_dir, exist_ok=True)
        out_path = os.path.join(
            self.capture_dir, "%04d-%s.json" % (self.request_counter, safe_tag[:80]))
        record = {
            "path": path,
            "request_payload": payload,
            "raw_wire_response_text": raw_text,
            "raw_wire_response_json": parsed,
        }
        with open(out_path, "w", encoding="utf-8") as handle:
            json.dump(record, handle, indent=2, ensure_ascii=False, default=str)

    def _post(self, path, payload, timeout=None, tag=None):
        data = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(
            self.base + path, data=data,
            headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=timeout or self.timeout) as resp:
            raw_text = resp.read().decode("utf-8", errors="replace")
            parsed = json.loads(raw_text)
            self._capture(path, payload, raw_text, parsed, tag=tag)
            return parsed

    def _post_stream(self, path, payload, timeout=None):
        data = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(
            self.base + path, data=data,
            headers={"Content-Type": "application/json"})
        chunks = 0
        done = False
        with urllib.request.urlopen(req, timeout=timeout or self.timeout) as resp:
            for raw in resp:
                line = raw.decode("utf-8", errors="replace").strip()
                if not line.startswith("data:"):
                    continue
                data = line[5:].strip()
                if data == "[DONE]":
                    done = True
                    break
                if data:
                    chunks += 1
        return {"chunks": chunks, "done": done}

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

    def context_window(self):
        """Tokens the loaded slot can hold, or None if the server will not say.

        llama.cpp reports it under default_generation_settings rather than at the
        top level on every build, so both are tried before giving up. None means
        "unknown", which callers must treat as "do not assume room", never as 0.
        """
        props = self.props() or {}
        for candidate in (props.get("n_ctx"),
                          (props.get("default_generation_settings") or {}).get("n_ctx")):
            try:
                value = int(candidate)
            except (TypeError, ValueError):
                continue
            if value > 0:
                return value
        return None

    def chat(self, messages, max_tokens=1024, temperature=0.0, tools=None,
             timeout=None, seed=20260821, tool_choice="auto", tag=None):
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
            payload["tool_choice"] = tool_choice
        started = time.monotonic()
        out = self._post("/v1/chat/completions", payload, timeout=timeout, tag=tag)
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
            "reasoning_content": reasoning,
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
            "parsed_message": message,
        }

    def stream_chat(self, messages, max_tokens=128, temperature=0.0, timeout=None):
        payload = {
            "model": self.alias,
            "messages": messages,
            "max_tokens": max_tokens,
            "temperature": temperature,
            "top_p": 1.0,
            "seed": 20260821,
            "stream": True,
        }
        return self._post_stream("/v1/chat/completions", payload, timeout=timeout)


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


STRICT_TOOL_POLICY = (
    "Tool arguments are API contracts. Preserve user-provided paths, globs, "
    "regular expressions, exclude patterns, identifiers and literal values "
    "exactly unless the user explicitly asks you to transform them."
)

AGENTIC_SYSTEM_POLICY = (
    "You are a coding agent working in an existing repository.\n"
    "- Obey explicit constraints exactly, including frozen files and literal values.\n"
    "- Inspect source before relying on APIs, file contents, or project behavior.\n"
    "- Never invent APIs, methods, files, flags, or test results.\n"
    "- Preserve tool arguments exactly when they are supplied as paths, globs, "
    "patterns, identifiers, or literal values.\n"
    "- If required information is missing, say what must be read instead of guessing.\n"
    "- Produce the requested implementation or tool call rather than endless analysis."
)


def compose_system(base, system_policy="current", strict_tool_policy=False):
    parts = [base]
    if system_policy == "agentic":
        parts.append(AGENTIC_SYSTEM_POLICY)
    if strict_tool_policy:
        parts.append(STRICT_TOOL_POLICY)
    return "\n\n".join(p for p in parts if p)


# Per-suite fixture defaults, named so a report can state what a run would have
# used had no profile ceiling been resolved. The coding default lives in the
# fixture table because it is a property of the fixtures.
CODING_FIXTURE_DEFAULT_MAX_TOKENS = CT.DEFAULT_MAX_TOKENS
TOOL_SUITE_DEFAULT_MAX_TOKENS = 4096
JSON_SUITE_DEFAULT_MAX_TOKENS = 4096
# Retention answers a fixed list of probes as one small JSON object, so its
# reserve is sized for the answer rather than for the profile. A profile ceiling
# raises it only as far as the unused part of the context window allows -- at
# 91% occupancy of a 262144-token window there is no room for a 32768-token
# answer, and asking for one would fail the prefill rather than measure it.
RETENTION_DEFAULT_OUTPUT_RESERVE = 4096
# Returned instead of a reserve when the prefill has taken so much of the window
# that not even the floor fits. The probe is then not measurable as configured,
# and the honest outcome is to say so rather than to send a request the server
# has to reject: n_ctx is a hard wall, and a rejected request measures nothing.
INSUFFICIENT_CONTEXT_RESERVE = "INSUFFICIENT_CONTEXT_RESERVE"
# Slack between the prefill and the answer cap. The tokenizer count and the
# server's own accounting differ slightly (template wrapping, BOS handling), and
# a probe that overflows the window measures nothing at all.
RETENTION_CONTEXT_MARGIN_TOKENS = 1024

# The floor mirrors $script:V2MinimumAnswerReserve in scripts/v2/Common.ps1.
# Common.ps1 derives the ceiling before a model is loaded; this module verifies
# the ceiling it was handed. One deriver, one verifier -- not two algorithms.
#
# It is the minimum answer reserve the output contract chose, and deliberately
# not "the smallest per-suite default": tools, literal_tools, json and retention
# all ask for 4096. The floor answers a different question -- how many tokens a
# profile with a bounded reasoning budget must keep back so the answer can still
# be written -- and 8192 is the number every fixture in this repository fits in.
MINIMUM_ANSWER_RESERVE = 8192

# The widest per-suite request default below, mirrored by
# $script:V2WidestFixtureCeiling in scripts/v2/Common.ps1. A profile that
# declares no n_predict is benchmarked through the per-suite defaults, so a
# uniform explicit ceiling is classified against the widest of them.
WIDEST_FIXTURE_CEILING = 8192

# What a run's request ceiling says about the profile it claims to measure.
# Only `deployment` is a baseline.
BUDGET_PROFILES = ("deployment", "constrained", "expanded")


def minimum_answer_reserve(request_ceiling):
    """Answer tokens a run with a positive reasoning budget must keep back.

    min() rather than a constant so a ceiling below the floor is judged against
    itself instead of an unreachable target.
    """
    return min(MINIMUM_ANSWER_RESERVE, request_ceiling)


def effective_generation_ceiling(request_ceiling, n_predict):
    """Tokens a run can actually generate: min(request_max_tokens, n_predict).

    llama-server stops at `n_predict` whatever `max_tokens` asks for, so a
    request ceiling above `n_predict` is arithmetic rather than headroom. Every
    budget rule is evaluated against this number, not against the request.

    0 on either side means "not stated": a request of 0 leaves each suite its own
    fixture default, and an `n_predict` of 0 means the profile declares none.

    Mirrors Get-V2EffectiveGenerationCeiling in scripts/v2/Common.ps1.
    """
    n_predict = n_predict or 0
    request_ceiling = request_ceiling or 0
    if n_predict <= 0:
        return max(request_ceiling, 0)
    if request_ceiling <= 0:
        return n_predict
    return min(request_ceiling, n_predict)


def budget_profile(source, request_ceiling, n_predict):
    """Name how a run's request ceiling relates to the served contract.

      deployment   the request is the served contract -- no override at all, a
                   ceiling derived from n_predict, or an explicit ceiling that
                   restates it exactly. The only one that is a baseline.
      constrained  an explicit ceiling below the contract; a NO_ANSWER may be the
                   benchmark cap rather than the model.
      expanded     an explicit ceiling above the contract; the server still stops
                   at n_predict, so a pass is not evidence about the profile.

    Classified on the ceiling that was *requested*, not on the effective one: an
    explicit 32768 against an n_predict of 8192 generates exactly what the
    deployment generates and is still not the deployment's own contract.

    Mirrors Get-V2BudgetProfile in scripts/v2/Common.ps1.
    """
    if source != "explicit":
        return "deployment"
    contract = n_predict if (n_predict or 0) > 0 else WIDEST_FIXTURE_CEILING
    request_ceiling = request_ceiling or 0
    if request_ceiling == contract:
        return "deployment"
    if request_ceiling < contract:
        return "constrained"
    return "expanded"


def answer_reserve_verdict(request_ceiling, reasoning_budget):
    """Describe what a (ceiling, reasoning budget) pair leaves for the answer."""
    applies = bool(reasoning_budget and reasoning_budget > 0)
    if not applies:
        return {"applies": False, "request_ceiling": request_ceiling,
                "reasoning_budget": None, "answer_reserve": None,
                "minimum_answer_reserve": None, "ok": True}
    reserve = request_ceiling - reasoning_budget
    minimum = minimum_answer_reserve(request_ceiling)
    return {"applies": True, "request_ceiling": request_ceiling,
            "reasoning_budget": reasoning_budget, "answer_reserve": reserve,
            "minimum_answer_reserve": minimum, "ok": reserve >= minimum}


def resolve_request_budget(request_max_tokens, source, n_predict,
                           reasoning_budget):
    """Everything a report has to say about one run's generation budget.

    The Python-side mirror of Resolve-V2QualificationRequestBudget in
    scripts/v2/Common.ps1: Common.ps1 derives this before a model is loaded, and
    this verifies the same arithmetic on the values it was handed. Both are
    pinned against the same table -- RESERVE_TABLE / CEILING_TABLE in
    test_qualify_budget.py and Assert-Budget in Test-V2ConfigGeneration.ps1.
    """
    requested = request_max_tokens if request_max_tokens and request_max_tokens > 0 else 0
    effective = effective_generation_ceiling(requested, n_predict)
    # With neither a request nor an n_predict the ceiling is per-suite and not
    # known here, so the invariant falls back to the minimum answer reserve the
    # output contract requires -- the floor every fixture must fit in, not the
    # smallest per-suite default.
    verdict = answer_reserve_verdict(effective or MINIMUM_ANSWER_RESERVE,
                                     reasoning_budget)
    return {
        "request_max_tokens": requested or None,
        "n_predict": (n_predict or 0) or None,
        "effective_generation_ceiling": effective or None,
        # Retained under its historical name so older readers keep working; it
        # has always meant the effective ceiling, and now really is one.
        "effective_max_tokens": effective or None,
        "source": source,
        "budget_profile": budget_profile(source, requested, n_predict),
        "reasoning_budget": (reasoning_budget or 0) or None,
        "answer_reserve": verdict["answer_reserve"],
        "minimum_answer_reserve": verdict["minimum_answer_reserve"],
        "reserve_ok": verdict["ok"],
    }


def resolve_policy_profile(system_policy, strict_tool_policy, tool_schema_policy,
                           run_budget_profile, constrained_diagnostic):
    """`baseline` only when the run measured the profile exactly as served.

    Served means both halves of the contract: the system and tool policies the
    deployment uses, and the generation budget it grants. An explicit ceiling
    that is not the profile's own contract is a diagnostic cell however ordinary
    the rest of the run looks.
    """
    baseline = (system_policy == "current"
                and not strict_tool_policy
                and not tool_schema_policy
                and run_budget_profile == "deployment"
                and not constrained_diagnostic)
    return "baseline" if baseline else "diagnostic"


def max_tokens(default, override):
    return override if override and override > 0 else default


def diagnostic_tools(with_schema_policy=False):
    tools = copy.deepcopy(CT.TOOLS)
    if not with_schema_policy:
        return tools
    literal_names = {
        "path", "paths", "query", "pattern", "glob", "include", "exclude",
        "identifier", "target", "package",
    }

    def annotate_schema(schema):
        if not isinstance(schema, dict):
            return
        props = schema.get("properties")
        if isinstance(props, dict):
            for name, prop in props.items():
                if isinstance(prop, dict):
                    if name in literal_names:
                        text = ("Use the literal value supplied by the user. "
                                "Do not normalize, broaden, simplify or substitute it.")
                        old = prop.get("description")
                        prop["description"] = (old + " " + text) if old else text
                    annotate_schema(prop)
        items = schema.get("items")
        if isinstance(items, dict):
            annotate_schema(items)

    for tool in tools:
        fn = tool.get("function") or {}
        params = fn.get("parameters") or {}
        annotate_schema(params)
    return tools


# The canonical failure vocabulary. Every failing row in every suite carries at
# least one of these, because the distinction they encode is the one a campaign
# report keeps getting wrong by hand: a model that wrote bad code, a model that
# never got to write anything, and a request that never completed are three
# different findings, and only the first is a quality result.
#
# Sub-reasons (CONSTRAINT_VIOLATION, INVENTED_API, SYNTAX_ERROR, TEST_FAILURE,
# VERIFIER_TIMEOUT, INVALID_TOOL, PARSER_FAILURE) may accompany a canonical tag
# and never replace it.
CANONICAL_FAILURES = (
    "MODEL_OUTPUT_FAILURE",     # the model answered and the answer was wrong
    "COMPILE_ERROR",            # the answer did not build
    "NO_ANSWER",                # generation ended with an empty content field
    "REASONING_EXHAUSTED",      # ... and it ended inside reasoning_content
    "OUTPUT_LENGTH",            # generation stopped at the request ceiling
    "REQUEST_TIMEOUT",          # the HTTP request never returned in time
    "REQUEST_ERROR",            # the HTTP request failed for any other reason
    "TOOL_ARGUMENT_ERROR",      # the right tool with mutated or missing arguments
    "STRUCTURED_OUTPUT_ERROR",  # the reply was not the object the schema asked for
    INSUFFICIENT_CONTEXT_RESERVE,  # the prefill left no room for even a floor answer
)


def classify_request_exception(exc):
    """Split a failed HTTP request into timeout and everything else.

    An untagged transport failure is how the 2026-08-23 report came to describe
    a 900-second timeout as "empty response, no output or reasoning tokens at
    all": the row carried an error string and no taxonomy at all, so a reader
    scored it as a model result.
    """
    text = str(exc).lower()
    if "timed out" in text or "timeout" in text:
        return ["REQUEST_TIMEOUT"]
    return ["REQUEST_ERROR"]


def classify_coding_failure(task, passed, constraint_ok, no_answer, truncated, detail, code):
    if passed and constraint_ok:
        return []
    categories = []
    text = (detail or "") + "\n" + (code or "")
    if no_answer:
        categories.append("NO_ANSWER")
    if truncated:
        categories.append("OUTPUT_LENGTH")
        if no_answer:
            categories.append("REASONING_EXHAUSTED")
    if not constraint_ok:
        categories.append("CONSTRAINT_VIOLATION")
    if "TIMEOUT" in text:
        # The toolchain timed out running the model's code, which is a property
        # of the answer. Deliberately not REQUEST_TIMEOUT, which is transport.
        categories.append("VERIFIER_TIMEOUT")
    lower = text.lower()
    if task["id"] == "no_invented_api" or "does not contain a definition" in lower or "undefined" in lower:
        categories.append("INVENTED_API")
    if "syntaxerror" in lower:
        categories.append("SYNTAX_ERROR")
    if ("build failed" in lower or "compilation failed" in lower or
            "error cs" in lower or "go test" in lower or "tsc" in lower or
            # dotnet speaks the host locale. Matching English only classified
            # Deep's real CS0136 on this pt-BR host as a bare TEST_FAILURE.
            "falha da compila" in lower or "erro(s)" in lower):
        categories.append("COMPILE_ERROR")
    if not categories:
        categories.append("TEST_FAILURE" if detail else "OTHER")
    # Output exhaustion is never promoted to a model-quality verdict: a model
    # that emitted no code cannot have emitted code that fails to compile.
    if not no_answer and not truncated:
        categories.insert(0, "MODEL_OUTPUT_FAILURE")
    return categories


# --------------------------------------------------------------------------
# suites
# --------------------------------------------------------------------------

def run_coding(server, workdir, only=None, max_tokens_override=0, temperature=0.0,
               seed=20260821, system_policy="current", strict_tool_policy=False,
               tasks=None, suite_name="coding"):
    results = []
    for task in (tasks or CT.TASKS):
        if only and task["id"] not in only:
            continue
        started = time.monotonic()
        try:
            reply = server.chat(
                [{"role": "system", "content": compose_system(task["system"], system_policy, strict_tool_policy)},
                 {"role": "user", "content": task["prompt"]}],
                max_tokens=max_tokens(task["max_tokens"], max_tokens_override),
                temperature=temperature, seed=seed, timeout=900,
                tag="%s-%s" % (suite_name, task["id"]))
        except Exception as exc:
            # Tagged, because an untagged transport failure in this list is
            # indistinguishable from a model that answered badly.
            taxonomy = classify_request_exception(exc)
            results.append({"id": task["id"], "family": task["family"],
                            "lang": task["lang"], "passed": False,
                            "compiled_or_ran": False, "no_answer": False,
                            "truncated": False, "request_failed": True,
                            "error": "request failed: %s" % exc,
                            "failure_taxonomy": taxonomy})
            print("  %-6s %-24s %-9s (%s)"
                  % (suite_name, task["id"], "REQ-FAIL", taxonomy[0]), flush=True)
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
            "reasoning_content": reply.get("reasoning_content", ""),
            "decode_tps": reply["timings"].get("predicted_per_second"),
            "wall_s": round(time.monotonic() - started, 1),
            "detail": detail[-700:] if isinstance(detail, str) else str(detail),
            "code": code,
        })
        results[-1]["failure_taxonomy"] = classify_coding_failure(
            task, bool(passed), constraint_ok, no_answer, truncated, detail, code)
        verdict = "PASS" if results[-1]["passed"] else ("NO-ANSWER" if no_answer else "FAIL")
        print("  %-6s %-24s %-9s (%d out tok, %d reasoning chars)"
              % (suite_name, task["id"], verdict, results[-1]["output_tokens"] or 0,
                 reply["reasoning_chars"]), flush=True)
    return results


def _value_match(actual, expected):
    if isinstance(expected, bool):
        if actual is expected:
            return True
        if isinstance(actual, str):
            return actual.strip().lower() == str(expected).lower()
        return False
    if isinstance(expected, int):
        try:
            return int(str(actual).strip()) == expected
        except Exception:
            return False
    if isinstance(expected, list):
        if not isinstance(actual, list) or len(actual) != len(expected):
            return False
        return all(_value_match(a, e) for a, e in zip(actual, expected))
    if isinstance(expected, dict):
        if not isinstance(actual, dict):
            return False
        return not _arg_mismatches(actual, expected)
    if isinstance(expected, str):
        # Byte-exact, deliberately. `norm` folds case, collapses whitespace and
        # strips trailing punctuation, which is right for a retention probe
        # ("Go" == "go") and wrong for a tool argument: it made `Vendor/**`
        # match `vendor/**`, `FOO[0-9]+` match `foo[0-9]+`, and a trailing space
        # on a path invisible. literal_identifier asks a model to preserve case
        # exactly and the grader could not see case at all. Re-graded against
        # the 2026-08-23 raw arguments this changes no historical verdict -- the
        # hole was latent, not load-bearing.
        return isinstance(actual, str) and actual == expected
    return norm(actual) == norm(expected)


def _arg_mismatches(got, want, prefix=""):
    mismatches = []
    for key, expected in want.items():
        label = ("%s.%s" % (prefix, key)) if prefix else key
        if key not in got:
            mismatches.append("missing:%s" % label)
            continue
        actual = got[key]
        if not _value_match(actual, expected):
            mismatches.append("%s=%r want %r" % (label, actual, expected))
    return mismatches


def _arg_match(got, want):
    """Argument comparison that accepts string-encoded scalars.

    A model that emits {"verbose": "true"} has selected the right tool with the
    right intent and a loose serialization; treating that as the same failure as
    calling the wrong tool would make the tool-calling score unreadable.
    """
    mismatches = _arg_mismatches(got, want)
    return (not mismatches), mismatches


def run_chat_smoke(server, temperature=0.0, seed=20260821, system_policy="current",
                   strict_tool_policy=False):
    results = []

    def add(row):
        results.append(row)
        print("  chat   %-24s %s" % (row["id"], "PASS" if row["passed"] else "FAIL"),
              flush=True)

    try:
        reply = server.chat(
            [{"role": "system", "content": compose_system("Follow the user's instruction exactly.", system_policy, strict_tool_policy)},
             {"role": "user", "content": "Reply with exactly: GEMMA-READY"}],
            max_tokens=256, temperature=temperature, seed=seed, timeout=300,
            tag="chat-system_user_stop")
        content = CT.strip_reasoning(reply["content"]).strip()
        add({"id": "system_user_stop", "passed": content == "GEMMA-READY",
             "finish_reason": reply["finish_reason"],
             "reasoning_chars": reply["reasoning_chars"]})
    except Exception as exc:
        add({"id": "system_user_stop", "passed": False, "error": str(exc),
             "failure_taxonomy": classify_request_exception(exc)})

    try:
        reply = server.chat(
            [{"role": "system", "content": compose_system("Remember facts from prior assistant turns.", system_policy, strict_tool_policy)},
             {"role": "user", "content": "Remember this project code: VX-417. Reply OK."},
             {"role": "assistant", "content": "OK"},
             {"role": "user", "content": "What project code did I give you? Reply with only the code."}],
            max_tokens=256, temperature=temperature, seed=seed, timeout=300,
            tag="chat-multi_turn")
        content = CT.strip_reasoning(reply["content"]).strip()
        add({"id": "multi_turn", "passed": content == "VX-417",
             "finish_reason": reply["finish_reason"],
             "reasoning_chars": reply["reasoning_chars"]})
    except Exception as exc:
        add({"id": "multi_turn", "passed": False, "error": str(exc),
             "failure_taxonomy": classify_request_exception(exc)})

    try:
        streamed = server.stream_chat(
            [{"role": "user", "content": "Count from one to five, one number per line."}],
            max_tokens=128, timeout=300)
        add({"id": "streaming", "passed": streamed["done"] and streamed["chunks"] > 1,
             "chunks": streamed["chunks"], "done": streamed["done"]})
    except Exception as exc:
        add({"id": "streaming", "passed": False, "error": str(exc),
             "failure_taxonomy": classify_request_exception(exc)})

    tool = {"type": "function", "function": {
        "name": "read_file",
        "description": "Read a file from the repository.",
        "parameters": {"type": "object", "properties": {
            "path": {"type": "string"}},
            "required": ["path"]}}}
    try:
        first = server.chat(
            [{"role": "system", "content": compose_system("Use tools when needed, then answer from tool results.", system_policy, strict_tool_policy)},
             {"role": "user", "content": "Read status.json, then tell me the status and ticket."}],
            max_tokens=512, temperature=temperature, seed=seed, tools=[tool],
            timeout=300, tool_choice="required", tag="chat-tool_result_first")
        calls = first["tool_calls"]
        call_ok = bool(calls and calls[0].get("function", {}).get("name") == "read_file")
        args_ok = False
        if calls:
            raw_args = calls[0].get("function", {}).get("arguments")
            try:
                args_ok = json.loads(raw_args).get("path") == "status.json"
            except Exception:
                args_ok = False
        if calls:
            call = calls[0]
            second_messages = [
                {"role": "system", "content": compose_system("Use tools when needed, then answer from tool results.", system_policy, strict_tool_policy)},
                {"role": "user", "content": "Read status.json, then tell me the status and ticket."},
                {"role": "assistant", "content": "", "tool_calls": [call]},
                {"role": "tool", "tool_call_id": call.get("id", "call_1"),
                 "content": "{\"status\":\"GREEN\",\"ticket\":\"G-42\"}"},
            ]
            second = server.chat(second_messages, max_tokens=512, temperature=temperature,
                                 seed=seed, timeout=300, tag="chat-tool_result_second")
            content = CT.strip_reasoning(second["content"])
            continuation_ok = "GREEN" in content and "G-42" in content
            finish_reason = second["finish_reason"]
            reasoning_chars = second["reasoning_chars"]
        else:
            continuation_ok = False
            finish_reason = first["finish_reason"]
            reasoning_chars = first["reasoning_chars"]
        add({"id": "tool_result_continuation",
             "passed": call_ok and args_ok and continuation_ok,
             "call_ok": call_ok, "args_ok": args_ok,
             "continuation_ok": continuation_ok,
             "finish_reason": finish_reason,
             "reasoning_chars": reasoning_chars})
    except Exception as exc:
        add({"id": "tool_result_continuation", "passed": False, "error": str(exc),
             "failure_taxonomy": classify_request_exception(exc)})

    return {"results": results,
            "passed": sum(1 for r in results if r.get("passed")),
            "total": len(results)}


def run_tools(server, only=None, max_tokens_override=0, temperature=0.0, seed=20260821,
              system_policy="current", strict_tool_policy=False, tool_schema_policy=False,
              tasks=None, suite_name="tools"):
    results = []
    tools = diagnostic_tools(tool_schema_policy)
    for task in (tasks or CT.TOOL_TASKS):
        if only and task["id"] not in only:
            continue
        try:
            reply = server.chat(
                [{"role": "system", "content": compose_system(
                  "You are a coding agent. When a tool can do what the user "
                  "asked, call it. Do not describe the call in prose.",
                  system_policy, strict_tool_policy)},
                 {"role": "user", "content": task["prompt"]}],
                max_tokens=max_tokens(TOOL_SUITE_DEFAULT_MAX_TOKENS, max_tokens_override),
                temperature=temperature, seed=seed, tools=tools, timeout=900,
                tag="%s-%s" % (suite_name, task["id"]))
        except Exception as exc:
            taxonomy = classify_request_exception(exc)
            results.append({"id": task["id"], "passed": False,
                            "request_failed": True,
                            "error": "request failed: %s" % exc,
                            "failure_taxonomy": taxonomy})
            print("  %-6s %-24s %-9s (%s)"
                  % (suite_name, task["id"], "REQ-FAIL", taxonomy[0]), flush=True)
            continue

        calls = reply["tool_calls"]
        row = {"id": task["id"], "call_count": len(calls),
               "name_ok": False, "args_ok": False, "json_ok": False,
               "forbidden_called": False, "passed": False,
               "detail": "", "finish_reason": reply["finish_reason"],
               "reasoning_chars": reply["reasoning_chars"],
               "reasoning_content": reply.get("reasoning_content", "")}
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
            row["raw_arguments_before_parser"] = raw_args
            try:
                args = json.loads(raw_args) if isinstance(raw_args, str) else (raw_args or {})
                row["json_ok"] = True
            except Exception:
                args = {}
                row["detail"] = "unparseable arguments: %r" % (raw_args,)[:200]
            row["arguments_after_parser"] = args
            row["arguments_observed_by_verifier"] = args
            if row["json_ok"]:
                row["args_ok"], mismatches = _arg_match(args, task["want_args"])
                if mismatches:
                    row["detail"] = "; ".join(mismatches)[:300]
            row["passed"] = bool(row["name_ok"] and row["args_ok"]
                                 and row["json_ok"] and not row["forbidden_called"])
        row["truncated"] = reply["finish_reason"] == "length"
        row["no_answer"] = bool(row["truncated"] and not calls
                                and not CT.strip_reasoning(reply["content"]).strip())
        if not row["passed"]:
            failure = []
            # Budget exhaustion first: a model still reasoning when the ceiling
            # arrived never chose a tool, and grading that as tool selection is
            # the same mistake NO_ANSWER exists to prevent on the coding suite.
            if row["no_answer"]:
                failure.extend(["NO_ANSWER", "OUTPUT_LENGTH", "REASONING_EXHAUSTED"])
            elif row["truncated"]:
                failure.append("OUTPUT_LENGTH")
            if not calls and not row["no_answer"]:
                failure.append("INVALID_TOOL")
            if calls and not row["name_ok"]:
                failure.append("INVALID_TOOL")
            if calls and not row["json_ok"]:
                failure.extend(["TOOL_ARGUMENT_ERROR", "PARSER_FAILURE"])
            if calls and row["json_ok"] and not row["args_ok"]:
                failure.append("TOOL_ARGUMENT_ERROR")
            if row["forbidden_called"]:
                failure.append("CONSTRAINT_VIOLATION")
            if calls and "MODEL_OUTPUT_FAILURE" not in failure and not row["no_answer"]:
                failure.insert(0, "MODEL_OUTPUT_FAILURE")
            row["failure_taxonomy"] = failure or ["OTHER"]
        results.append(row)
        print("  %-6s %-24s %s" % (suite_name, task["id"], "PASS" if row["passed"] else "FAIL"),
              flush=True)
    return results


def run_json(server, max_tokens_override=0, temperature=0.0, seed=20260821,
             system_policy="current", strict_tool_policy=False):
    task = CT.JSON_TASK
    try:
        reply = server.chat(
            [{"role": "system", "content": compose_system("Return only JSON. No prose, no fence.", system_policy, strict_tool_policy)},
             {"role": "user", "content": task["prompt"]}],
            max_tokens=max_tokens(JSON_SUITE_DEFAULT_MAX_TOKENS, max_tokens_override),
            temperature=temperature, seed=seed, timeout=900,
            tag="json-%s" % task["id"])
    except Exception as exc:
        return {"id": task["id"], "passed": False, "request_failed": True,
                "error": str(exc), "failure_taxonomy": classify_request_exception(exc)}

    obj, clean = parse_json_reply(reply["content"])
    row = {"id": task["id"], "parsed": obj is not None, "clean_json": clean,
           "passed": False, "mismatches": [], "finish_reason": reply["finish_reason"],
           "reasoning_chars": reply["reasoning_chars"],
           "reasoning_content": reply.get("reasoning_content", "")}
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
    row["truncated"] = reply["finish_reason"] == "length"
    row["no_answer"] = bool(row["truncated"] and not CT.strip_reasoning(reply["content"]).strip())
    if not row["passed"]:
        failure = []
        if row["no_answer"]:
            failure.extend(["NO_ANSWER", "OUTPUT_LENGTH", "REASONING_EXHAUSTED"])
        elif row["truncated"]:
            failure.append("OUTPUT_LENGTH")
        if not row["no_answer"]:
            # Either it did not parse, or it parsed and disagreed with the
            # schema. Both are structured-output failures; neither is a
            # transport failure and neither is exhaustion.
            failure.extend(["MODEL_OUTPUT_FAILURE", "STRUCTURED_OUTPUT_ERROR"])
        row["failure_taxonomy"] = failure or ["OTHER"]
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


def resolve_retention_reserve(prompt_tokens, n_ctx, requested_ceiling,
                              floor=RETENTION_DEFAULT_OUTPUT_RESERVE):
    """Answer cap for one retention probe, and why it is that number.

    A profile ceiling is the contract to measure, but the prefill has already
    taken most of the window by the time this is asked. Requesting more than the
    window has left turns a retention measurement into a context-overflow error,
    so the ceiling is clamped to the room that remains and never falls below the
    floor the probes actually need.

    The floor is a floor on what to ask for, never a promise that it fits. When
    the room left is smaller than the floor, this returns
    (0, INSUFFICIENT_CONTEXT_RESERVE) and the caller must shrink the corpus or
    skip the probe -- sending a floor-sized request into a window that cannot
    hold it produces a rejected request, not a measurement.
    """
    room = None
    if n_ctx and prompt_tokens:
        room = n_ctx - prompt_tokens - RETENTION_CONTEXT_MARGIN_TOKENS
    # Checked before the ceiling, because the floor path has to obey the window
    # too: a 4096-token request is still a request, and a 261000-token prefill in
    # a 262144-token window has nowhere to put it.
    if room is not None and room < floor:
        return 0, INSUFFICIENT_CONTEXT_RESERVE
    if not requested_ceiling or requested_ceiling <= floor:
        return floor, "fixture"
    if room is None:
        return floor, "fixture"
    allowed = min(requested_ceiling, room)
    if allowed <= floor:
        return floor, "fixture"
    return allowed, ("profile" if allowed == requested_ceiling else "context-clamped")


def run_retention(server, target_tokens, output_reserve=RETENTION_DEFAULT_OUTPUT_RESERVE,
                  seed=20260821, request_ceiling=0):
    print("  retention: sizing corpus for ~%d prompt tokens" % target_tokens, flush=True)
    doc, units, doc_tokens = build_corpus_for(server, target_tokens, seed=seed)
    prompt = doc + FC.QUESTION_BLOCK
    total_tokens = server.count_tokens(prompt)
    n_ctx = server.context_window()
    reserve, reserve_source = resolve_retention_reserve(
        total_tokens, n_ctx, request_ceiling, floor=output_reserve)
    if reserve_source == INSUFFICIENT_CONTEXT_RESERVE:
        # No request is sent. The prompt plus even a floor-sized answer exceeds
        # n_ctx, so the only thing a request could measure is the server
        # rejecting it. The probe depth has to come down, or the window go up.
        room = n_ctx - total_tokens - RETENTION_CONTEXT_MARGIN_TOKENS
        print("  retention: %d filler units, %d prompt tokens, %d of %d window left "
              "after the %d-token margin -- below the %d-token floor; not sent" %
              (units, total_tokens, room, n_ctx, RETENTION_CONTEXT_MARGIN_TOKENS,
               output_reserve), flush=True)
        return {
            "target_tokens": target_tokens,
            "prompt_tokens": total_tokens,
            "corpus_tokens": doc_tokens,
            "filler_units": units,
            "n_ctx": n_ctx,
            "context_room": room,
            "output_reserve": 0,
            "output_reserve_source": reserve_source,
            "requested": False,
            "failure": ("not measurable: a %d-token prompt leaves %d tokens of a "
                        "%d-token window after the %d-token margin, and the probes "
                        "need %d. Lower the retention depth or raise n_ctx."
                        % (total_tokens, room, n_ctx,
                           RETENTION_CONTEXT_MARGIN_TOKENS, output_reserve)),
            "failure_taxonomy": [INSUFFICIENT_CONTEXT_RESERVE],
        }
    print("  retention: %d filler units, %d prompt tokens, %d answer tokens (%s); prefilling" %
          (units, total_tokens, reserve, reserve_source), flush=True)

    started = time.monotonic()
    try:
        reply = server.chat(
            [{"role": "system", "content":
              "You are a coding agent reading a project briefing. Answer only "
              "from the briefing. Never guess a value you did not read."},
             {"role": "user", "content": prompt}],
            max_tokens=reserve, timeout=7200)
    except Exception as exc:
        return {"target_tokens": target_tokens, "prompt_tokens": total_tokens,
                "output_reserve": reserve, "output_reserve_source": reserve_source,
                "failure": "request failed: %s" % exc,
                "failure_taxonomy": classify_request_exception(exc)}

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
        "output_reserve": reserve,
        "output_reserve_source": reserve_source,
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
                    help="comma-separated: chat,coding,hard,tools,literal_tools,json,retention")
    ap.add_argument("--retention-tokens", type=int, action="append", default=[])
    ap.add_argument("--only", default="", help="restrict the coding suite to these ids")
    ap.add_argument("--only-tools", default="", help="restrict the tool suite to these ids")
    ap.add_argument("--max-tokens", type=int, default=0,
                    help="effective completion cap for every suite; 0 keeps each "
                         "fixture's own default")
    ap.add_argument("--max-tokens-source", choices=("explicit", "profile", "fixture"),
                    default="fixture",
                    help="where --max-tokens came from, recorded so a report can "
                         "distinguish a deployment contract from a benchmark cap")
    ap.add_argument("--n-predict", type=int, default=0,
                    help="the profile's --n-predict. The server stops there "
                         "whatever --max-tokens asks for, so the budget rules are "
                         "checked against min(max_tokens, n_predict); 0 means the "
                         "profile declares none")
    ap.add_argument("--reasoning-budget", type=int, default=0,
                    help="the server's --reasoning-budget, recorded and checked "
                         "against the effective generation ceiling; 0 means "
                         "unbounded/unset")
    ap.add_argument("--allow-constrained-request-budget", action="store_true",
                    help="permit a request ceiling that cannot hold the answer "
                         "reserve; names a diagnostic cell, never a baseline")
    ap.add_argument("--temperature", type=float, default=0.0)
    ap.add_argument("--seed", type=int, default=20260821)
    ap.add_argument("--capture-dir", default="",
                    help="write raw request/response artifacts for parser diagnosis")
    ap.add_argument("--system-policy", choices=("current", "agentic"), default="current")
    ap.add_argument("--strict-tool-policy", action="store_true")
    ap.add_argument("--tool-schema-policy", action="store_true")
    args = ap.parse_args()

    # What the HTTP request asks for. 0 means "each suite keeps its own fixture
    # default".
    requested_ceiling = args.max_tokens if args.max_tokens > 0 else 0
    # What the server will actually emit. A request above the profile's n_predict
    # is arithmetic, not headroom, and checking the budget against the request
    # rather than against this is how -MaxTokens 32768 on an 8192-token profile
    # came to look like a legal 8192-token answer reserve.
    resolved = resolve_request_budget(requested_ceiling, args.max_tokens_source,
                                      args.n_predict, args.reasoning_budget)
    effective_ceiling = resolved["effective_generation_ceiling"] or 0
    profile_of_budget = resolved["budget_profile"]
    if not resolved["reserve_ok"] and not args.allow_constrained_request_budget:
        capped = ""
        if requested_ceiling and effective_ceiling < requested_ceiling:
            capped = (" (the request asks for %d, but n_predict %d is all the "
                      "server will emit)" % (requested_ceiling, args.n_predict))
        print("refusing to measure an impossible generation contract: a "
              "reasoning_budget of %d leaves %d answer tokens under a %d-token "
              "effective generation ceiling%s, and at least %d are required. "
              "Raise the ceiling, lower the budget, or pass "
              "--allow-constrained-request-budget."
              % (args.reasoning_budget, resolved["answer_reserve"],
                 effective_ceiling or MINIMUM_ANSWER_RESERVE, capped,
                 resolved["minimum_answer_reserve"]),
              file=sys.stderr)
        return 3

    os.makedirs(args.workdir, exist_ok=True)
    server = Server(args.base_url, args.alias, capture_dir=args.capture_dir or None)
    if not server.wait_ready(900):
        print("server never became ready", file=sys.stderr)
        return 2

    suites = [s.strip() for s in args.suites.split(",") if s.strip()]
    only = set(x.strip() for x in args.only.split(",") if x.strip())
    only_tools = set(x.strip() for x in args.only_tools.split(",") if x.strip())

    report = {
        "schema_version": 1,
        "scenario": "profile-qualification",
        "label": args.label,
        "started_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "server_props": {k: v for k, v in (server.props() or {}).items()
                         if k in ("n_ctx", "model_path", "default_generation_settings",
                                  "total_slots", "build_info", "chat_template")
                         and k != "chat_template"},
        # The generation contract this run measured, kept beside the results so
        # a NO_ANSWER can be read as either "the deployment budget was not
        # enough" or "the benchmark capped it below the deployment budget":
        # what the request asked for, what the server serves, the smaller of the
        # two -- the only one any budget rule may use -- and what that says about
        # which contract this run measured.
        "request_budget": dict(resolved, **{
            "fixture_defaults": {
                "coding": CODING_FIXTURE_DEFAULT_MAX_TOKENS,
                "tools": TOOL_SUITE_DEFAULT_MAX_TOKENS,
                "json": JSON_SUITE_DEFAULT_MAX_TOKENS,
                "retention": RETENTION_DEFAULT_OUTPUT_RESERVE,
            },
            "constrained_diagnostic": bool(args.allow_constrained_request_budget),
        }),
        # Baseline means "the profile as served", in both senses: the policies
        # it is served under AND the generation budget it is served with. Any
        # non-default policy, any budget_profile other than `deployment`, and any
        # deliberately constrained cap makes this a labelled diagnostic cell whose
        # score is not comparable to a baseline score.
        "policy_profile": resolve_policy_profile(
            args.system_policy, args.strict_tool_policy, args.tool_schema_policy,
            profile_of_budget, args.allow_constrained_request_budget),
        "diagnostic_config": {
            "max_tokens_override": args.max_tokens or None,
            "max_tokens_source": args.max_tokens_source,
            "budget_profile": profile_of_budget,
            "n_predict": args.n_predict or None,
            "temperature": args.temperature,
            "seed": args.seed,
            "system_policy": args.system_policy,
            "strict_tool_policy": bool(args.strict_tool_policy),
            "tool_schema_policy": bool(args.tool_schema_policy),
            "capture_dir": args.capture_dir or None,
            "only_coding": sorted(only),
            "only_tools": sorted(only_tools),
        },
        "suites": {},
    }

    if "chat" in suites:
        print("[chat]", flush=True)
        report["suites"]["chat"] = run_chat_smoke(
            server, temperature=args.temperature, seed=args.seed,
            system_policy=args.system_policy,
            strict_tool_policy=args.strict_tool_policy)

    if "coding" in suites:
        print("[coding]", flush=True)
        rows = run_coding(
            server, args.workdir, only or None,
            max_tokens_override=args.max_tokens,
            temperature=args.temperature,
            seed=args.seed,
            system_policy=args.system_policy,
            strict_tool_policy=args.strict_tool_policy)
        report["suites"]["coding"] = {
            "results": rows,
            "passed": sum(1 for r in rows if r.get("passed")),
            "total": len(rows),
            "compiled": sum(1 for r in rows if r.get("compiled_or_ran")),
            "no_answer": sum(1 for r in rows if r.get("no_answer")),
            "truncated": sum(1 for r in rows if r.get("truncated")),
        }

    if "hard" in suites:
        print("[hard]", flush=True)
        rows = run_coding(
            server, args.workdir, None,
            max_tokens_override=args.max_tokens,
            temperature=args.temperature,
            seed=args.seed,
            system_policy=args.system_policy,
            strict_tool_policy=args.strict_tool_policy,
            tasks=CT.HARD_TASKS,
            suite_name="hard")
        report["suites"]["hard"] = {
            "results": rows,
            "passed": sum(1 for r in rows if r.get("passed")),
            "total": len(rows),
            "compiled": sum(1 for r in rows if r.get("compiled_or_ran")),
            "no_answer": sum(1 for r in rows if r.get("no_answer")),
            "truncated": sum(1 for r in rows if r.get("truncated")),
        }

    if "tools" in suites:
        print("[tools]", flush=True)
        rows = run_tools(
            server, only_tools or None,
            max_tokens_override=args.max_tokens,
            temperature=args.temperature,
            seed=args.seed,
            system_policy=args.system_policy,
            strict_tool_policy=args.strict_tool_policy,
            tool_schema_policy=args.tool_schema_policy)
        report["suites"]["tools"] = {
            "results": rows,
            "passed": sum(1 for r in rows if r.get("passed")),
            "total": len(rows),
        }

    if "literal_tools" in suites:
        print("[literal_tools]", flush=True)
        rows = run_tools(
            server, None,
            max_tokens_override=args.max_tokens,
            temperature=args.temperature,
            seed=args.seed,
            system_policy=args.system_policy,
            strict_tool_policy=args.strict_tool_policy,
            tool_schema_policy=args.tool_schema_policy,
            tasks=CT.LITERAL_TOOL_TASKS,
            suite_name="literal")
        report["suites"]["literal_tools"] = {
            "results": rows,
            "passed": sum(1 for r in rows if r.get("passed")),
            "total": len(rows),
        }

    if "json" in suites:
        print("[json]", flush=True)
        report["suites"]["json"] = run_json(
            server,
            max_tokens_override=args.max_tokens,
            temperature=args.temperature,
            seed=args.seed,
            system_policy=args.system_policy,
            strict_tool_policy=args.strict_tool_policy)

    if "retention" in suites:
        print("[retention]", flush=True)
        levels = args.retention_tokens or [30000]
        report["suites"]["retention"] = [
            run_retention(server, t, request_ceiling=effective_ceiling) for t in levels]

    with open(args.out, "w", encoding="utf-8") as handle:
        json.dump(report, handle, indent=2, default=str)
    print("wrote %s" % args.out)
    return 0


if __name__ == "__main__":
    sys.exit(main())
