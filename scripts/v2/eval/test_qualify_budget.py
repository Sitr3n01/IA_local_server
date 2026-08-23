#!/usr/bin/env python3
"""Self-test for the qualification generation budget and failure taxonomy.

Pure functions only -- no server, no toolchain, no GPU -- so this runs in well
under a second and belongs in the fast gate.

Three things are pinned here.

The answer-reserve invariant, which must agree with `Test-V2AnswerReserve` in
scripts/v2/Common.ps1. Common.ps1 derives the request ceiling before a model is
loaded; this module verifies the ceiling it was handed. Two implementations of
one rule is how a manifest becomes legal and the benchmark built from it
impossible, so the arithmetic is asserted on both sides against the same table.

The retention answer cap, which is the one place a profile ceiling cannot simply
be honoured: at 91% occupancy of a 262144-token window there is no room for a
32768-token answer, and asking for one turns a retention measurement into a
context overflow.

The failure taxonomy, whose whole purpose is that a report cannot confuse a
model that wrote bad code with a model that never got to write anything, or with
a request that never came back. The 2026-08-23 campaign report described a
900-second HTTP timeout as "empty response, no output or reasoning tokens at
all" precisely because the row carried no taxonomy at all.

Run: python test_qualify_budget.py
"""
import os
import socket
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import qualify as Q      # noqa: E402
import coding_tasks as CT  # noqa: E402


FAILURES = []


def check(label, condition):
    if condition:
        print("  ok    %s" % label, flush=True)
    else:
        print("  FAIL  %s" % label, flush=True)
        FAILURES.append(label)


def equals(label, got, want):
    check("%s == %r" % (label, want), got == want)
    if got != want:
        print("        got %r" % (got,))


# --------------------------------------------------------------------------
# answer reserve
# --------------------------------------------------------------------------

# (ceiling, reasoning budget, expected reserve, expected minimum, expected ok)
# Mirrored verbatim by the Assert-Budget table in Test-V2ConfigGeneration.ps1.
RESERVE_TABLE = [
    # The three shipped canary shapes.
    (8192, 0, None, None, True),         # Deep / Agent: no reasoning budget
    (32768, 24576, 8192, 8192, True),    # Huge: 32768 - 24576 = 8192, exactly the floor
    # The combination the 2026-08-23 campaign actually ran.
    (8192, 24576, -16384, 8192, False),
    # One token short of the floor is still short.
    (32768, 24577, 8191, 8192, False),
    # A ceiling below the floor is judged against itself, which means it cannot
    # fund bounded reasoning at all until it is raised.
    (4096, 1024, 3072, 4096, False),
    (4096, 0, None, None, True),
    # Raising the ceiling is the way out.
    (16384, 8192, 8192, 8192, True),
    # A negative budget means "unset", never "zero".
    (8192, -1, None, None, True),
]


def test_answer_reserve():
    print("[answer reserve]")
    for ceiling, budget, reserve, minimum, ok in RESERVE_TABLE:
        verdict = Q.answer_reserve_verdict(ceiling, budget)
        label = "ceiling=%d budget=%d" % (ceiling, budget)
        equals("%s reserve" % label, verdict["answer_reserve"], reserve)
        equals("%s minimum" % label, verdict["minimum_answer_reserve"], minimum)
        equals("%s ok" % label, verdict["ok"], ok)

    equals("floor matches Common.ps1", Q.MINIMUM_ANSWER_RESERVE, 8192)
    equals("floor scales to a small ceiling", Q.minimum_answer_reserve(4096), 4096)
    equals("floor does not scale past itself", Q.minimum_answer_reserve(262144), 8192)


def test_fixture_defaults_are_named():
    print("[fixture defaults]")
    # The coding default has to come from the fixture table, not be duplicated
    # here: raising it to suit one profile would silently change what every
    # other profile is measured against, which is why a profile ceiling is
    # resolved outside the fixture table instead.
    equals("coding default comes from coding_tasks",
           Q.CODING_FIXTURE_DEFAULT_MAX_TOKENS, CT.DEFAULT_MAX_TOKENS)
    equals("coding default", CT.DEFAULT_MAX_TOKENS, 8192)
    for task in CT.TASKS + CT.HARD_TASKS:
        check("%s uses the shared coding default" % task["id"],
              task["max_tokens"] == CT.DEFAULT_MAX_TOKENS)
    equals("tools default", Q.TOOL_SUITE_DEFAULT_MAX_TOKENS, 4096)
    equals("json default", Q.JSON_SUITE_DEFAULT_MAX_TOKENS, 4096)
    equals("retention default", Q.RETENTION_DEFAULT_OUTPUT_RESERVE, 4096)


def test_max_tokens_override():
    print("[per-request override]")
    equals("no override keeps the fixture default", Q.max_tokens(8192, 0), 8192)
    equals("a profile ceiling replaces it", Q.max_tokens(8192, 32768), 32768)
    equals("a smaller explicit cap also replaces it", Q.max_tokens(8192, 2048), 2048)
    equals("a negative override is ignored", Q.max_tokens(8192, -1), 8192)


# --------------------------------------------------------------------------
# retention answer cap
# --------------------------------------------------------------------------

def test_retention_reserve():
    print("[retention answer cap]")
    floor = Q.RETENTION_DEFAULT_OUTPUT_RESERVE

    equals("no profile ceiling keeps the floor",
           Q.resolve_retention_reserve(16000, 131072, 0), (floor, "fixture"))
    equals("a ceiling below the floor keeps the floor",
           Q.resolve_retention_reserve(16000, 131072, 2048), (floor, "fixture"))
    # Shallow occupancy on Huge: the whole 32768-token contract fits.
    equals("a shallow prefill honours the profile ceiling",
           Q.resolve_retention_reserve(32620, 262144, 32768), (32768, "profile"))
    # The deepest 2026-08-23 Huge probe: 239893 prompt tokens in a 262144-token
    # window leaves 22251, minus the margin. Asking for 32768 would overflow.
    reserve, source = Q.resolve_retention_reserve(239893, 262144, 32768)
    equals("a deep prefill clamps to the remaining window", source, "context-clamped")
    equals("the clamped cap is the room that is left", reserve,
           262144 - 239893 - Q.RETENTION_CONTEXT_MARGIN_TOKENS)
    check("the clamped cap never exceeds the window",
          239893 + reserve + Q.RETENTION_CONTEXT_MARGIN_TOKENS <= 262144)
    # A window with no room left falls back to the floor rather than to a
    # negative or zero cap, so the probe still runs and its failure is a real
    # measurement rather than a malformed request.
    equals("a full window falls back to the floor",
           Q.resolve_retention_reserve(261000, 262144, 32768), (floor, "fixture"))
    # An unknown window is not an infinite one.
    equals("an unknown context window keeps the floor",
           Q.resolve_retention_reserve(16000, None, 32768), (floor, "fixture"))


# --------------------------------------------------------------------------
# failure taxonomy
# --------------------------------------------------------------------------

def taxonomy(task_id="unity_impl", passed=False, constraint_ok=True, no_answer=False,
             truncated=False, detail="", code=""):
    task = {"id": task_id}
    return Q.classify_coding_failure(task, passed, constraint_ok, no_answer,
                                     truncated, detail, code)


def test_coding_taxonomy():
    print("[coding taxonomy]")
    check("a pass is not classified", taxonomy(passed=True) == [])

    # Gemma and Huge, 2026-08-23: 8192 output tokens, ~28000 reasoning chars,
    # empty content. Exhaustion, and specifically not a compile failure.
    exhausted = taxonomy(no_answer=True, truncated=True,
                         detail="no answer: generation stopped at the output cap")
    check("exhaustion is NO_ANSWER", "NO_ANSWER" in exhausted)
    check("exhaustion is OUTPUT_LENGTH", "OUTPUT_LENGTH" in exhausted)
    check("exhaustion is REASONING_EXHAUSTED", "REASONING_EXHAUSTED" in exhausted)
    check("exhaustion is not a compile error", "COMPILE_ERROR" not in exhausted)
    check("exhaustion is not a model output failure",
          "MODEL_OUTPUT_FAILURE" not in exhausted)

    # Truncated but with code emitted: the output ran out, and there is still
    # nothing to say about the answer's quality.
    cut = taxonomy(truncated=True, detail="build failed", code="class X {")
    check("truncation alone is OUTPUT_LENGTH", "OUTPUT_LENGTH" in cut)
    check("truncation alone is not a model output failure",
          "MODEL_OUTPUT_FAILURE" not in cut)

    # Deep, 2026-08-23: a complete answer that does not compile. The detail is
    # the real pt-BR dotnet output, because the host speaks pt-BR and an
    # English-only matcher classified this as a bare TEST_FAILURE.
    ptbr = ("Solution.cs(36,24): error CS0136: Um local ou um parametro "
            "denominado \"instance\" nao pode ser declarado neste escopo\n"
            "FALHA da compilacao.\n    0 Aviso(s)\n    1 Erro(s)")
    real = taxonomy(detail=ptbr, code="public class ProjectilePool {}")
    check("a wrong answer is MODEL_OUTPUT_FAILURE", "MODEL_OUTPUT_FAILURE" in real)
    check("a pt-BR compiler failure is COMPILE_ERROR", "COMPILE_ERROR" in real)
    check("a wrong answer is not NO_ANSWER", "NO_ANSWER" not in real)
    check("MODEL_OUTPUT_FAILURE leads the list", real[0] == "MODEL_OUTPUT_FAILURE")
    english = taxonomy(detail="error CS0136: a local named 'instance'\nBuild FAILED.")
    check("an English compiler failure is still COMPILE_ERROR",
          "COMPILE_ERROR" in english)

    violated = taxonomy(constraint_ok=False, detail="OK")
    check("a frozen-file edit is CONSTRAINT_VIOLATION",
          "CONSTRAINT_VIOLATION" in violated)

    # A toolchain that hangs on the model's code is a property of the answer,
    # and deliberately not the same event as an HTTP timeout.
    slow = taxonomy(detail="TIMEOUT after 180s")
    check("a verifier timeout is VERIFIER_TIMEOUT", "VERIFIER_TIMEOUT" in slow)
    check("a verifier timeout is not a request timeout",
          "REQUEST_TIMEOUT" not in slow)

    check("every canonical name is documented",
          set(["MODEL_OUTPUT_FAILURE", "COMPILE_ERROR", "NO_ANSWER",
               "REASONING_EXHAUSTED", "OUTPUT_LENGTH", "REQUEST_TIMEOUT",
               "REQUEST_ERROR", "TOOL_ARGUMENT_ERROR", "STRUCTURED_OUTPUT_ERROR"])
          <= set(Q.CANONICAL_FAILURES))


def test_request_exception_taxonomy():
    print("[transport taxonomy]")
    # Agent's unity_impl and Deep's hard_go_retry_multifile on 2026-08-23. Both
    # were 900-second HTTP timeouts recorded with no taxonomy, and the campaign
    # report then described one of them as an empty model response.
    equals("socket timeout", Q.classify_request_exception(socket.timeout("timed out")),
           ["REQUEST_TIMEOUT"])
    equals("timeout by message",
           Q.classify_request_exception(Exception("The read operation timed out")),
           ["REQUEST_TIMEOUT"])
    equals("connection refused",
           Q.classify_request_exception(ConnectionRefusedError("refused")),
           ["REQUEST_ERROR"])
    equals("anything else", Q.classify_request_exception(ValueError("bad json")),
           ["REQUEST_ERROR"])
    for exc in (socket.timeout("timed out"), ValueError("nope")):
        tags = Q.classify_request_exception(exc)
        check("a transport failure is never a model verdict (%s)" % type(exc).__name__,
              "MODEL_OUTPUT_FAILURE" not in tags and "COMPILE_ERROR" not in tags)


def main():
    test_answer_reserve()
    test_fixture_defaults_are_named()
    test_max_tokens_override()
    test_retention_reserve()
    test_coding_taxonomy()
    test_request_exception_taxonomy()

    print("")
    if FAILURES:
        print("QUALIFICATION BUDGET SELF-TEST FAILED (%d)" % len(FAILURES))
        for f in FAILURES:
            print("  - %s" % f)
        return 1
    print("QUALIFICATION BUDGET SELF-TEST PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
