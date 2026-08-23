#!/usr/bin/env python3
"""Self-test for tool-argument grading and for the tool fixtures themselves.

Two halves, and the second is the one that would have caught the 2026-08-23
defect.

The grading half pins `_arg_match` behaviour: nested objects, nested required
fields, a missing `exclude`, booleans, arrays, and byte-exact globs and regexes.
The strict contract exists to detect argument mutation, so every case that
accepts a near-miss is asserted to fail. String-encoded scalars are the single
documented latitude -- a model that emits {"verbose": "true"} picked the right
tool with the right intent and a loose serialization -- and that is asserted to
be accepted, deliberately and narrowly.

The fixture half asserts that every literal the grader demands byte-exactly is
supplied byte-exactly by the synthetic user. Four models failed
`tool_pick_search_many_nested` on 2026-08-23 for emitting `vendor/`, `vendor`,
`**/*.go`, or nothing at all, against a prompt that only said "excluding
vendor". That is a fixture asking a model to guess a literal and then grading it
for guessing wrong, and no amount of model quality fixes it.

Neither half talks to a server or a toolchain, so this runs in a second.

Run: python test_tool_grading.py
"""
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import coding_tasks as CT      # noqa: E402
import qualify as Q            # noqa: E402


FAILURES = []


def check(label, condition):
    if condition:
        print("  ok    %s" % label, flush=True)
    else:
        print("  FAIL  %s" % label, flush=True)
        FAILURES.append(label)


def accepts(label, got, want):
    ok, mismatches = Q._arg_match(got, want)
    check("%s -> accepted" % label, ok)
    if not ok:
        print("        mismatches: %s" % "; ".join(mismatches))


def rejects(label, got, want, expect_key=None):
    ok, mismatches = Q._arg_match(got, want)
    check("%s -> rejected" % label, not ok)
    if ok:
        return
    if expect_key is not None:
        check("%s -> names %s" % (label, expect_key),
              any(expect_key in m for m in mismatches))


NESTED_WANT = {
    "query": "resource_profile_incomplete",
    "filters": {"include": ["*.go"], "exclude": ["vendor/**"], "case_sensitive": False},
}


def test_nested_objects():
    print("[nested objects]")
    accepts("exact nested match", dict(NESTED_WANT), NESTED_WANT)
    accepts("nested key order is irrelevant",
            {"filters": {"case_sensitive": False, "exclude": ["vendor/**"],
                         "include": ["*.go"]},
             "query": "resource_profile_incomplete"},
            NESTED_WANT)
    rejects("a flattened call is not a nested call",
            {"query": "resource_profile_incomplete", "include": ["*.go"],
             "exclude": ["vendor/**"], "case_sensitive": False},
            NESTED_WANT, expect_key="filters")
    rejects("filters as a JSON string is not an object",
            {"query": "resource_profile_incomplete",
             "filters": '{"include":["*.go"]}'},
            NESTED_WANT, expect_key="filters")


def test_nested_required_fields():
    print("[nested required fields]")
    # Exactly what Agent and Huge emitted on 2026-08-23.
    rejects("missing nested exclude",
            {"query": "resource_profile_incomplete",
             "filters": {"include": ["*.go"], "case_sensitive": False}},
            NESTED_WANT, expect_key="filters")
    rejects("missing nested include",
            {"query": "resource_profile_incomplete",
             "filters": {"exclude": ["vendor/**"], "case_sensitive": False}},
            NESTED_WANT, expect_key="filters")
    rejects("missing nested case_sensitive",
            {"query": "resource_profile_incomplete",
             "filters": {"include": ["*.go"], "exclude": ["vendor/**"]}},
            NESTED_WANT, expect_key="filters")
    rejects("missing top-level query",
            {"filters": {"include": ["*.go"], "exclude": ["vendor/**"],
                         "case_sensitive": False}},
            NESTED_WANT, expect_key="query")
    accepts("an extra unrequested key is not a mutation",
            {"query": "resource_profile_incomplete", "max_results": 50,
             "filters": {"include": ["*.go"], "exclude": ["vendor/**"],
                         "case_sensitive": False}},
            NESTED_WANT)


def test_booleans():
    print("[booleans]")
    accepts("true", {"package": "internal/edge", "verbose": True},
            {"package": "internal/edge", "verbose": True})
    accepts("string-encoded true is the documented latitude",
            {"package": "internal/edge", "verbose": "true"},
            {"package": "internal/edge", "verbose": True})
    accepts("string-encoded TRUE is case-insensitive",
            {"package": "internal/edge", "verbose": "TRUE"},
            {"package": "internal/edge", "verbose": True})
    rejects("false is not true", {"package": "internal/edge", "verbose": False},
            {"package": "internal/edge", "verbose": True}, expect_key="verbose")
    rejects("1 is not the boolean true",
            {"package": "internal/edge", "verbose": 1},
            {"package": "internal/edge", "verbose": True}, expect_key="verbose")
    # Gemma flipped exactly this field on literal_cs_glob.
    rejects("case_sensitive flipped inside a nested object",
            {"query": "resource_profile_incomplete",
             "filters": {"include": ["*.go"], "exclude": ["vendor/**"],
                         "case_sensitive": True}},
            NESTED_WANT, expect_key="filters")


def test_arrays():
    print("[arrays]")
    accepts("exact array", {"paths": ["README.md", "docs/BENCHMARKS.md"]},
            {"paths": ["README.md", "docs/BENCHMARKS.md"]})
    rejects("reordered array", {"paths": ["docs/BENCHMARKS.md", "README.md"]},
            {"paths": ["README.md", "docs/BENCHMARKS.md"]}, expect_key="paths")
    rejects("short array", {"paths": ["README.md"]},
            {"paths": ["README.md", "docs/BENCHMARKS.md"]}, expect_key="paths")
    rejects("extra element", {"paths": ["README.md", "docs/BENCHMARKS.md", "go.mod"]},
            {"paths": ["README.md", "docs/BENCHMARKS.md"]}, expect_key="paths")
    rejects("a comma-joined string is not an array",
            {"paths": "README.md,docs/BENCHMARKS.md"},
            {"paths": ["README.md", "docs/BENCHMARKS.md"]}, expect_key="paths")


def test_exact_globs():
    print("[exact globs]")
    want = {"include": ["Assets/Scripts/**/*.cs"], "exclude": ["Library/**"],
            "case_sensitive": True}
    accepts("exact globs", dict(want), want)
    # Every near-miss below was emitted by a real model in a real campaign. The
    # verifier is never taught to accept them: mutation is what it detects.
    for mutated, label in (("vendor", "bare directory name"),
                           ("vendor/", "trailing slash"),
                           ("vendor/*", "single star"),
                           ("**/vendor/**", "broadened"),
                           ("Vendor/**", "recased")):
        rejects("exclude %s (%s) is not vendor/**" % (mutated, label),
                {"include": ["*.go"], "exclude": [mutated], "case_sensitive": False},
                NESTED_WANT["filters"], expect_key="exclude")
    rejects("include **/*.go is not *.go",
            {"include": ["**/*.go"], "exclude": ["vendor/**"], "case_sensitive": False},
            NESTED_WANT["filters"], expect_key="include")


def test_exact_regex():
    print("[exact regex]")
    accepts("exact regex", {"path": "src/Api.cs", "query": "foo[0-9]+"},
            {"path": "src/Api.cs", "query": "foo[0-9]+"})
    for mutated in ("foo[0-9]*", "foo\\d+", "foo[0-9]", "FOO[0-9]+", "foo[0-9]+ "):
        rejects("regex %r is not foo[0-9]+" % mutated,
                {"path": "src/Api.cs", "query": mutated},
                {"path": "src/Api.cs", "query": "foo[0-9]+"}, expect_key="query")


def test_string_encoded_scalars():
    print("[string-encoded scalars]")
    want = {"path": "config/models.schema.json", "start_line": 40, "end_line": 80}
    accepts("integers", dict(want), want)
    accepts("string-encoded integers",
            {"path": "config/models.schema.json", "start_line": "40", "end_line": "80"},
            want)
    rejects("a wrong integer is still wrong",
            {"path": "config/models.schema.json", "start_line": 41, "end_line": 80},
            want, expect_key="start_line")
    # Paths, globs, regexes and identifiers are compared as written, so no
    # normalisation latitude leaks in through the scalar path.
    rejects("a path is not normalised",
            {"path": "./config/models.schema.json", "start_line": 40, "end_line": 80},
            want, expect_key="path")
    rejects("a path with a spurious space",
            {"path": "config/models.schema.json ", "start_line": 40, "end_line": 80},
            want, expect_key="path")


# --------------------------------------------------------------------------
# Fixture self-consistency
# --------------------------------------------------------------------------

# Values the grader compares byte-for-byte and a model therefore cannot derive:
# globs, regex metacharacters, and paths. Ordinary words ("Release", "go") are
# excluded because a prompt can convey them in prose without quoting them.
LITERAL_MARKERS = re.compile(r"[*?\[\]{}\\]|/")


def _literals(value):
    """Every byte-exact string the grader will demand from one want_args tree."""
    out = []
    if isinstance(value, str):
        if LITERAL_MARKERS.search(value):
            out.append(value)
    elif isinstance(value, list):
        for item in value:
            out.extend(_literals(item))
    elif isinstance(value, dict):
        for item in value.values():
            out.extend(_literals(item))
    return out


def test_fixtures_supply_their_literals():
    print("[fixtures supply the literals they grade]")
    for task in list(CT.TOOL_TASKS) + list(CT.LITERAL_TOOL_TASKS):
        prompt = task["prompt"]
        for literal in _literals(task["want_args"]):
            check("%s states %r" % (task["id"], literal), literal in prompt)


def test_fixtures_state_required_booleans():
    print("[fixtures state the required booleans they grade]")
    # `search_files.filters` makes case_sensitive required, so the model must
    # send one. If the prompt does not say which, the grader is scoring a coin
    # flip -- the defect Gemma lost literal_cs_glob to.
    for task in list(CT.TOOL_TASKS) + list(CT.LITERAL_TOOL_TASKS):
        filters = task["want_args"].get("filters")
        if not isinstance(filters, dict) or "case_sensitive" not in filters:
            continue
        stated = "case-sensitive" if filters["case_sensitive"] else "case-insensitive"
        check("%s states %s" % (task["id"], stated), stated in task["prompt"].lower())


def test_tool_schema_matches_expectations():
    print("[expected arguments satisfy the advertised schema]")
    by_name = {t["function"]["name"]: t["function"] for t in CT.TOOLS}
    for task in list(CT.TOOL_TASKS) + list(CT.LITERAL_TOOL_TASKS):
        fn = by_name.get(task["want_name"])
        check("%s targets a declared tool" % task["id"], fn is not None)
        if fn is None:
            continue
        params = fn.get("parameters") or {}
        for required in params.get("required", []):
            check("%s supplies required %s" % (task["id"], required),
                  required in task["want_args"])
        nested = (params.get("properties") or {}).get("filters")
        if isinstance(nested, dict) and isinstance(task["want_args"].get("filters"), dict):
            for required in nested.get("required", []):
                check("%s supplies required filters.%s" % (task["id"], required),
                      required in task["want_args"]["filters"])


def main():
    test_nested_objects()
    test_nested_required_fields()
    test_booleans()
    test_arrays()
    test_exact_globs()
    test_exact_regex()
    test_string_encoded_scalars()
    test_fixtures_supply_their_literals()
    test_fixtures_state_required_booleans()
    test_tool_schema_matches_expectations()

    print("")
    if FAILURES:
        print("TOOL GRADING SELF-TEST FAILED (%d)" % len(FAILURES))
        for f in FAILURES:
            print("  - %s" % f)
        return 1
    print("TOOL GRADING SELF-TEST PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
