#!/usr/bin/env python3
"""Contract probes that `qualify.py` deliberately does not cover.

`qualify.py` answers "does this model choose the right tool with the right
arguments". This answers a different question: does the *serving path* behave,
under the shapes a coding harness actually produces. The two are separable
failures - a model can pick tools perfectly through a direct llama-server port
and still be unusable through the edge, because the edge normalizes authority
messages, and because streaming and cancellation exercise code neither the model
nor the direct port ever runs.

It takes a base URL, so the same probes run against a direct `llama-server`, the
llama-swap router, and the full edge, and the three results are comparable. A
difference between them is the finding; a single run against one of them proves
nothing about the others.

Model-independent by construction: no probe asserts on wording, only on the
shape of the response. `--alias` names whichever model the endpoint serves.

Run:
  python edge_contract.py --base-url http://127.0.0.1:19399 --alias mymodel \
      --out edge-contract.json
"""
import argparse
import http.client
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


# --------------------------------------------------------------------------
# transport
# --------------------------------------------------------------------------

class Endpoint:
    def __init__(self, base_url, alias, api_key=None, timeout=900):
        self.base = base_url.rstrip("/")
        self.alias = alias
        self.api_key = api_key
        self.timeout = timeout

    def _headers(self):
        headers = {"Content-Type": "application/json"}
        if self.api_key:
            headers["Authorization"] = "Bearer %s" % self.api_key
        return headers

    def post(self, path, payload, timeout=None):
        """Returns (status, parsed_or_none, raw_text). Never raises on HTTP status.

        A 400 is a result here, not an error: several probes exist precisely to
        find out whether the path refuses a shape, and turning that into an
        exception would lose the status code the probe is about.
        """
        body = json.dumps(payload).encode("utf-8")
        request = urllib.request.Request(self.base + path, data=body,
                                         headers=self._headers(), method="POST")
        try:
            with urllib.request.urlopen(request, timeout=timeout or self.timeout) as response:
                raw = response.read().decode("utf-8", "replace")
                status = response.status
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode("utf-8", "replace")
            status = exc.code
        try:
            return status, json.loads(raw), raw
        except Exception:
            return status, None, raw

    def get(self, path, timeout=30):
        request = urllib.request.Request(self.base + path, headers=self._headers())
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                raw = response.read().decode("utf-8", "replace")
                status = response.status
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode("utf-8", "replace")
            status = exc.code
        try:
            return status, json.loads(raw), raw
        except Exception:
            return status, None, raw

    def stream(self, payload, on_chunk, stop_after=None, timeout=None):
        """Reads an SSE completion, optionally abandoning it part-way.

        Uses http.client rather than urllib so the socket can be closed
        mid-response: that abrupt close is what a cancelling harness does, and
        reproducing it is the whole point of the cancellation probe.
        """
        parts = urllib.parse.urlsplit(self.base)
        conn_cls = http.client.HTTPSConnection if parts.scheme == "https" else http.client.HTTPConnection
        conn = conn_cls(parts.hostname, parts.port, timeout=timeout or self.timeout)
        chunks = 0
        done = False
        aborted = False
        try:
            conn.request("POST", "/v1/chat/completions",
                         body=json.dumps(payload).encode("utf-8"), headers=self._headers())
            response = conn.getresponse()
            if response.status != 200:
                return {"status": response.status, "chunks": 0, "done": False,
                        "aborted": False, "body": response.read().decode("utf-8", "replace")[:800]}
            buffer = b""
            while True:
                block = response.read(1)
                if not block:
                    break
                buffer += block
                if not buffer.endswith(b"\n\n"):
                    continue
                for line in buffer.decode("utf-8", "replace").splitlines():
                    if not line.startswith("data:"):
                        continue
                    data = line[5:].strip()
                    if data == "[DONE]":
                        done = True
                        continue
                    try:
                        on_chunk(json.loads(data))
                    except Exception:
                        pass
                    chunks += 1
                buffer = b""
                if done:
                    break
                if stop_after is not None and chunks >= stop_after:
                    aborted = True
                    break
            return {"status": 200, "chunks": chunks, "done": done, "aborted": aborted}
        finally:
            try:
                conn.close()
            except Exception:
                pass


# --------------------------------------------------------------------------
# fixtures
# --------------------------------------------------------------------------

READ_FILE = {"type": "function", "function": {
    "name": "read_file",
    "description": "Read a file from the repository.",
    "parameters": {"type": "object",
                   "properties": {"path": {"type": "string"},
                                  "max_bytes": {"type": "integer"}},
                   "required": ["path"]}}}

SEARCH_FILES = {"type": "function", "function": {
    "name": "search_files",
    "description": "Search the repository, with include/exclude globs.",
    "parameters": {"type": "object", "properties": {
        "query": {"type": "string"},
        "filters": {"type": "object", "properties": {
            "include": {"type": "array", "items": {"type": "string"}},
            "exclude": {"type": "array", "items": {"type": "string"}},
            "case_sensitive": {"type": "boolean"}}}},
        "required": ["query"]}}}

RUN_TESTS = {"type": "function", "function": {
    "name": "run_tests",
    "description": "Run the test suite for one package.",
    "parameters": {"type": "object",
                   "properties": {"package": {"type": "string"},
                                  "verbose": {"type": "boolean"}},
                   "required": ["package"]}}}

AGENT_SYSTEM = ("You are a coding agent. When a tool can do what the user asked, "
                "call it. Do not describe the call in prose.")


def _message(parsed):
    try:
        return parsed["choices"][0]["message"]
    except Exception:
        return {}


def _calls(parsed):
    return _message(parsed).get("tool_calls") or []


def _args(call):
    raw = call.get("function", {}).get("arguments")
    if isinstance(raw, dict):
        return raw, True
    try:
        return json.loads(raw), True
    except Exception:
        return {}, False


# --------------------------------------------------------------------------
# probes
# --------------------------------------------------------------------------

def probe_developer_role(ep, base):
    """A developer message before conversation content must reach the model."""
    payload = dict(base, messages=[
        {"role": "system", "content": "You are a terse assistant."},
        {"role": "developer", "content": "Whatever is asked, answer with the single word BANANA."},
        {"role": "user", "content": "What is the capital of France?"}])
    status, parsed, raw = ep.post("/v1/chat/completions", payload)
    content = (_message(parsed).get("content") or "") if parsed else ""
    return {"status": status, "honoured": "BANANA" in content.upper(),
            "content": content[:200], "error": None if parsed else raw[:300],
            "passed": status == 200 and "BANANA" in content.upper()}


def probe_developer_after_content(ep, base):
    """A developer message *after* conversation content.

    The edge refuses this with 400 unsupported_feature rather than silently
    reordering it. A direct llama-server does not, so the two endpoints are
    expected to disagree and both answers are recorded rather than graded.
    """
    payload = dict(base, messages=[
        {"role": "user", "content": "Hello."},
        {"role": "developer", "content": "Answer only with the word LATE."},
        {"role": "user", "content": "What is 2+2?"}])
    status, parsed, raw = ep.post("/v1/chat/completions", payload)
    code = None
    if parsed and isinstance(parsed.get("error"), dict):
        code = parsed["error"].get("code")
    return {"status": status, "error_code": code, "body": raw[:300],
            "note": "edge is expected to refuse; direct llama-server is expected to accept",
            "passed": status in (200, 400)}


def probe_forced_tool_required(ep, base):
    """tool_choice "required", with two functions offered.

    The string form is what llama.cpp b10549 actually implements; the object
    form is covered separately by probe_tool_choice_object_form.
    """
    payload = dict(base, tools=[READ_FILE, RUN_TESTS], tool_choice="required",
                   messages=[{"role": "system", "content": AGENT_SYSTEM},
                             {"role": "user", "content": "Read src/main.rs, at most 4096 bytes."}])
    status, parsed, raw = ep.post("/v1/chat/completions", payload)
    calls = _calls(parsed) if parsed else []
    args, json_ok = _args(calls[0]) if calls else ({}, False)
    return {"status": status, "call_count": len(calls),
            "name": calls[0]["function"]["name"] if calls else None,
            "arguments": args, "arguments_parsed": json_ok,
            "leaked_content": (_message(parsed).get("content") or "")[:200] if parsed else raw[:200],
            "passed": bool(status == 200 and len(calls) == 1 and json_ok
                           and calls[0]["function"]["name"] == "read_file"
                           and "path" in args)}


def probe_tool_choice_object_form(ep, base):
    """The OpenAI object form of tool_choice, which names one function.

    On llama.cpp b10549 this is accepted with HTTP 200 and then ignored: the
    server logs `Wrong type supplied for parameter 'tool_choice'. Expected
    'string'` and falls back to its default. A harness that pins a specific
    function therefore gets ordinary automatic selection with nothing in the
    response to say so.

    The probe forces the *less* obvious function - the user's sentence describes
    reading a file, while tool_choice names run_tests - so that honouring it and
    ignoring it produce different answers. Informational: whether the correct
    behaviour is to honour it or to refuse it with 400 is a decision for the
    edge, not something this probe can assert.
    """
    payload = dict(base, tools=[READ_FILE, RUN_TESTS],
                   tool_choice={"type": "function", "function": {"name": "run_tests"}},
                   messages=[{"role": "system", "content": AGENT_SYSTEM},
                             {"role": "user", "content": "Read src/main.rs, at most 4096 bytes."}])
    status, parsed, raw = ep.post("/v1/chat/completions", payload)
    calls = _calls(parsed) if parsed else []
    name = calls[0]["function"]["name"] if calls else None
    return {"status": status, "call_count": len(calls), "name": name,
            "honoured": name == "run_tests",
            "silently_ignored": bool(status == 200 and name is not None and name != "run_tests"),
            "body": None if parsed else raw[:300],
            "note": "200 with the wrong function means the runtime discarded the constraint",
            "passed": True}


def probe_nested_arguments(ep, base):
    """Nested object plus two arrays in one call."""
    payload = dict(base, tools=[SEARCH_FILES, READ_FILE], tool_choice="required",
                   messages=[{"role": "system", "content": AGENT_SYSTEM},
                             {"role": "user", "content":
                              "Search for resource_profile_incomplete, but only in .go files, "
                              "skipping anything under vendor/, and ignore letter case."}])
    status, parsed, raw = ep.post("/v1/chat/completions", payload)
    calls = _calls(parsed) if parsed else []
    args, json_ok = _args(calls[0]) if calls else ({}, False)
    filters = args.get("filters") if isinstance(args, dict) else None
    nested_ok = isinstance(filters, dict) and isinstance(filters.get("include"), list)
    return {"status": status, "call_count": len(calls),
            "name": calls[0]["function"]["name"] if calls else None,
            "arguments": args, "arguments_parsed": json_ok, "nested_object_ok": nested_ok,
            "error": None if parsed else raw[:300],
            "passed": bool(status == 200 and json_ok and nested_ok)}


def probe_sequential_tool_calls(ep, base):
    """Two tool rounds in one conversation, feeding a result back each time."""
    messages = [{"role": "system", "content": AGENT_SYSTEM},
                {"role": "user", "content":
                 "Read status.json, then run the tests for whichever package it names."}]
    rounds = []
    ok = True
    for index in range(2):
        payload = dict(base, tools=[READ_FILE, RUN_TESTS], messages=messages)
        if index == 0:
            payload["tool_choice"] = "required"
        status, parsed, raw = ep.post("/v1/chat/completions", payload)
        calls = _calls(parsed) if parsed else []
        rounds.append({"round": index, "status": status, "call_count": len(calls),
                       "names": [c["function"]["name"] for c in calls],
                       "error": None if parsed else raw[:200]})
        if status != 200 or not calls:
            ok = False
            break
        assistant = {"role": "assistant", "content": _message(parsed).get("content") or "",
                     "tool_calls": calls}
        messages.append(assistant)
        for call in calls:
            messages.append({"role": "tool", "tool_call_id": call.get("id", "call_%d" % index),
                             "name": call["function"]["name"],
                             "content": json.dumps({"package": "internal/edge", "status": "dirty"})})
    return {"rounds": rounds,
            "passed": bool(ok and len(rounds) == 2 and rounds[1]["call_count"] >= 1)}


def probe_streaming_tool_call(ep, base):
    """Tool calls must arrive as SSE deltas, not only in a buffered response."""
    seen = {"tool_deltas": 0, "name": None, "argument_chars": 0, "finish_reason": None}

    def on_chunk(chunk):
        for choice in chunk.get("choices", []):
            delta = choice.get("delta") or {}
            for call in delta.get("tool_calls") or []:
                seen["tool_deltas"] += 1
                function = call.get("function") or {}
                if function.get("name"):
                    seen["name"] = function["name"]
                seen["argument_chars"] += len(function.get("arguments") or "")
            if choice.get("finish_reason"):
                seen["finish_reason"] = choice["finish_reason"]

    payload = dict(base, stream=True, tools=[READ_FILE, RUN_TESTS],
                   tool_choice={"type": "function", "function": {"name": "run_tests"}},
                   messages=[{"role": "system", "content": AGENT_SYSTEM},
                             {"role": "user", "content": "Run the tests for internal/edge, verbosely."}])
    result = ep.stream(payload, on_chunk)
    result.update(seen)
    result["passed"] = bool(result.get("status") == 200 and seen["tool_deltas"] > 0
                            and seen["name"] == "run_tests")
    return result


def probe_cancel_during_tool(ep, base):
    """Abandon a streamed tool generation, then prove the endpoint still serves.

    A cancellation that leaves the slot wedged looks identical to a healthy one
    until the next request, so the recovery request is the actual assertion.
    """
    payload = dict(base, stream=True, tools=[READ_FILE, SEARCH_FILES], tool_choice="required",
                   messages=[{"role": "system", "content": AGENT_SYSTEM},
                             {"role": "user", "content":
                              "Search the repository for every occurrence of resource_profile_incomplete."}])
    started = time.time()
    aborted = ep.stream(payload, lambda chunk: None, stop_after=1)
    abort_seconds = round(time.time() - started, 2)

    time.sleep(2)
    recovery = dict(base, messages=[{"role": "user", "content": "Reply with exactly: ALIVE"}],
                    max_tokens=base.get("max_tokens", 512))
    status, parsed, raw = ep.post("/v1/chat/completions", recovery, timeout=300)
    content = (_message(parsed).get("content") or "") if parsed else ""
    return {"abort": aborted, "abort_seconds": abort_seconds,
            "recovery_status": status, "recovery_content": content[:120],
            "recovery_error": None if parsed else raw[:300],
            "passed": bool(aborted.get("aborted") and status == 200)}


def probe_malformed_tool_result(ep, base):
    """A tool result that is not the JSON the model asked for.

    Harnesses emit these on a failed command. The contract is that the turn
    still terminates with an answer; recovering gracefully is quality, not
    crashing is the gate.
    """
    messages = [{"role": "system", "content": AGENT_SYSTEM},
                {"role": "user", "content": "Read status.json and tell me the status."},
                {"role": "assistant", "content": "",
                 "tool_calls": [{"id": "call_0", "type": "function",
                                 "function": {"name": "read_file",
                                              "arguments": "{\"path\": \"status.json\"}"}}]},
                {"role": "tool", "tool_call_id": "call_0", "name": "read_file",
                 "content": "ERROR: ENOENT no such file or directory, and this is not JSON {{{"}]
    status, parsed, raw = ep.post("/v1/chat/completions", dict(base, tools=[READ_FILE], messages=messages))
    content = (_message(parsed).get("content") or "") if parsed else ""
    finish = parsed["choices"][0].get("finish_reason") if parsed else None
    return {"status": status, "finish_reason": finish, "content": content[:300],
            "error": None if parsed else raw[:300],
            "passed": bool(status == 200 and content.strip())}


def probe_reasoning_separation(ep, base):
    """Reasoning must not consume the whole allowance before the answer starts."""
    payload = dict(base, messages=[{"role": "user", "content":
                                    "A bag holds 3 red and 5 blue marbles. Two are drawn without "
                                    "replacement. What is the probability both are blue? "
                                    "Give the final answer as a fraction."}])
    status, parsed, raw = ep.post("/v1/chat/completions", payload)
    message = _message(parsed) if parsed else {}
    reasoning = message.get("reasoning_content") or ""
    content = message.get("content") or ""
    usage = (parsed or {}).get("usage") or {}
    finish = parsed["choices"][0].get("finish_reason") if parsed else None
    return {"status": status, "finish_reason": finish,
            "reasoning_chars": len(reasoning), "answer_chars": len(content),
            "reasoning_field_present": "reasoning_content" in message,
            "completion_tokens": usage.get("completion_tokens"),
            "answer_preview": content[:200],
            "reasoning_markers_leaked": ("<think>" in content or "</think>" in content),
            "error": None if parsed else raw[:300],
            # An empty answer with finish_reason "length" is the exact failure
            # ADR 0012 added reasoning budgets for.
            "passed": bool(status == 200 and content.strip()
                           and not ("<think>" in content or "</think>" in content))}


def probe_reasoning_roundtrip(ep, base):
    """Send a previous turn's reasoning_content back and see it accepted.

    The server is stateless, so nothing on it remembers what the model thought
    last turn. A template that can re-render prior reasoning can only do so from
    what the harness resends, which makes "reasoning is preserved" a statement
    about the client, not about the server. This checks the half the server owns:
    that a `reasoning_content` field on a historical assistant message is
    accepted and does not corrupt the turn.

    Two requests, identical except for that field, so a 400 or a mangled reply
    isolates to it.
    """
    history = [
        {"role": "user", "content": "Think about what 17 * 23 is, then say only the number."},
        {"role": "assistant", "content": "391"},
        {"role": "user", "content": "Now multiply that by 2. Say only the number."},
    ]
    status_plain, plain, raw_plain = ep.post("/v1/chat/completions", dict(base, messages=history))

    with_reasoning = [dict(m) for m in history]
    with_reasoning[1]["reasoning_content"] = "17 * 23 = 17*20 + 17*3 = 340 + 51 = 391."
    status_r, parsed_r, raw_r = ep.post("/v1/chat/completions", dict(base, messages=with_reasoning))

    plain_text = (_message(plain).get("content") or "") if plain else ""
    reasoning_text = (_message(parsed_r).get("content") or "") if parsed_r else ""
    return {"status_without": status_plain, "status_with": status_r,
            "content_without": plain_text[:120], "content_with": reasoning_text[:120],
            "accepted": status_r == 200,
            "both_answered": bool(plain_text.strip() and reasoning_text.strip()),
            "error": None if parsed_r else raw_r[:300],
            "passed": bool(status_plain == 200 and status_r == 200 and reasoning_text.strip())}


def probe_image_rejected(ep, base):
    """Vision is out of scope, so this records what the path does with an image.

    No projector is loaded. Any outcome other than a clean refusal or a
    text-only answer would mean image tokens reached the model with nothing to
    fill them, which is the silent-corruption case worth knowing about.
    """
    payload = dict(base, messages=[{"role": "user", "content": [
        {"type": "text", "text": "What is in this image?"},
        {"type": "image_url", "image_url": {"url":
            "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="}}]}])
    status, parsed, raw = ep.post("/v1/chat/completions", payload)
    content = (_message(parsed).get("content") or "") if parsed else ""
    return {"status": status, "content": content[:200], "body": raw[:300],
            "note": "vision is unqualified for this campaign; this records behaviour, it does not gate",
            "passed": True}


PROBES = [
    ("developer_role", probe_developer_role),
    ("developer_after_content", probe_developer_after_content),
    ("forced_tool_required", probe_forced_tool_required),
    ("tool_choice_object_form", probe_tool_choice_object_form),
    ("nested_arguments", probe_nested_arguments),
    ("sequential_tool_calls", probe_sequential_tool_calls),
    ("streaming_tool_call", probe_streaming_tool_call),
    ("cancel_during_tool", probe_cancel_during_tool),
    ("malformed_tool_result", probe_malformed_tool_result),
    ("reasoning_separation", probe_reasoning_separation),
    ("reasoning_roundtrip", probe_reasoning_roundtrip),
    ("image_rejected", probe_image_rejected),
]

# Probes whose result is recorded but which cannot pass or fail on their own,
# because the correct answer differs between a direct port and the edge, or
# because no correct answer has been decided yet.
INFORMATIONAL = {"developer_after_content", "tool_choice_object_form", "image_rejected"}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default="http://127.0.0.1:19399")
    parser.add_argument("--alias", required=True)
    parser.add_argument("--label", default="")
    parser.add_argument("--out", required=True)
    parser.add_argument("--max-tokens", type=int, default=2048)
    parser.add_argument("--temperature", type=float, default=0.0)
    parser.add_argument("--timeout", type=int, default=900)
    parser.add_argument("--api-key-file", default="")
    parser.add_argument("--only", default="", help="comma-separated probe ids")
    args = parser.parse_args()

    api_key = None
    if args.api_key_file:
        with open(args.api_key_file, encoding="utf-8") as handle:
            api_key = handle.read().strip()

    endpoint = Endpoint(args.base_url, args.alias, api_key=api_key, timeout=args.timeout)
    base = {"model": args.alias, "max_tokens": args.max_tokens,
            "temperature": args.temperature, "stream": False}

    only = {item.strip() for item in args.only.split(",") if item.strip()}
    report = {"schema_version": 1, "scenario": "edge-contract",
              "base_url": args.base_url, "alias": args.alias,
              "label": args.label or args.alias,
              "started_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
              "probes": {}}

    status, models, _ = endpoint.get("/v1/models")
    report["models_status"] = status
    report["models"] = [entry.get("id") for entry in (models or {}).get("data", [])]

    graded = 0
    passed = 0
    for name, probe in PROBES:
        if only and name not in only:
            continue
        started = time.time()
        try:
            result = probe(endpoint, base)
        except Exception as exc:
            result = {"passed": False, "error": "%s: %s" % (type(exc).__name__, exc)}
        result["seconds"] = round(time.time() - started, 1)
        result["informational"] = name in INFORMATIONAL
        report["probes"][name] = result
        if name not in INFORMATIONAL:
            graded += 1
            passed += 1 if result.get("passed") else 0
            verdict = "PASS" if result.get("passed") else "FAIL"
        else:
            verdict = "INFO"
        print("  %-24s %-4s %5.1fs" % (name, verdict, result["seconds"]), flush=True)

    report["graded"] = graded
    report["passed"] = passed
    report["verdict"] = "PASS" if graded and passed == graded else "FAIL"

    directory = os.path.dirname(os.path.abspath(args.out))
    if directory:
        os.makedirs(directory, exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as handle:
        json.dump(report, handle, indent=2)
    print("")
    print("%s  %d/%d graded probes  ->  %s" % (report["verdict"], passed, graded, args.out))
    return 0 if report["verdict"] == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
