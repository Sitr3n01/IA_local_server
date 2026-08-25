#!/usr/bin/env python3
"""Self-test for the qualification generation budget and failure taxonomy.

Pure functions only -- no server, no toolchain, no GPU -- so this runs in well
under a second and belongs in the fast gate.

Five things are pinned here.

The answer-reserve invariant, which must agree with `Test-V2AnswerReserve` in
scripts/v2/Common.ps1. Common.ps1 derives the request ceiling before a model is
loaded; this module verifies the ceiling it was handed. Two implementations of
one rule is how a manifest becomes legal and the benchmark built from it
impossible, so the arithmetic is asserted on both sides against the same table.

The effective generation ceiling, `min(request_max_tokens, n_predict)`. The
server stops at `n_predict` whatever the request asks for, so a request above it
is arithmetic and not headroom. Checking the reserve against the request instead
is what let `-NPredict 8192 -MaxTokens 32768 -ReasoningBudget 24576` -- a
contract with a *negative* answer reserve -- resolve to a legal-looking 8192.

The budget profile, `deployment | constrained | expanded`. Only `deployment` --
the profile measured exactly as it is served -- is a baseline. An explicit
ceiling that is not the profile's own contract is a diagnostic cell whatever
else the run does, and `policy_profile` has to say so.

The retention answer cap, which is the one place a profile ceiling cannot simply
be honoured: at 91% occupancy of a 262144-token window there is no room for a
32768-token answer, and asking for one turns a retention measurement into a
context overflow. Past the point where not even the 4096-token floor fits, no
request is sent at all -- the reserve resolves to
`INSUFFICIENT_CONTEXT_RESERVE`.

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
    # The floor is the minimum answer reserve the output contract chose. It is
    # explicitly NOT "the smallest default any suite uses" -- four of the six
    # suites ask for 4096 -- and a doc or comment that says otherwise is wrong.
    check("the floor is above the smallest per-suite default",
          Q.MINIMUM_ANSWER_RESERVE > min(Q.TOOL_SUITE_DEFAULT_MAX_TOKENS,
                                         Q.JSON_SUITE_DEFAULT_MAX_TOKENS,
                                         Q.RETENTION_DEFAULT_OUTPUT_RESERVE))
    equals("floor scales to a small ceiling", Q.minimum_answer_reserve(4096), 4096)
    equals("floor does not scale past itself", Q.minimum_answer_reserve(262144), 8192)


# --------------------------------------------------------------------------
# effective generation ceiling
# --------------------------------------------------------------------------

# (request max_tokens, n_predict, expected effective ceiling)
# Mirrored by the Assert-EffectiveCeiling table in Test-V2ConfigGeneration.ps1.
CEILING_TABLE = [
    # Neither stated: the suites keep their own fixture defaults.
    (0, 0, 0),
    # Only one side stated is that side.
    (8192, 0, 8192),
    (0, 32768, 32768),
    # The shipped canary shapes, where request and contract agree.
    (8192, 8192, 8192),
    (32768, 32768, 32768),
    # A request above the profile contract is truncated by the server, so the
    # ceiling that applies is the profile's. This is the regression: the request
    # is 32768 and the run can still only emit 8192.
    (32768, 8192, 8192),
    (16384, 8192, 8192),
    # A request below the contract is honoured as asked -- the server does not
    # raise a request.
    (4096, 32768, 4096),
]


def test_effective_generation_ceiling():
    print("[effective generation ceiling]")
    for request, npredict, expected in CEILING_TABLE:
        equals("min(request=%d, n_predict=%d)" % (request, npredict),
               Q.effective_generation_ceiling(request, npredict), expected)
    # Negative and None are "unset", never "zero tokens".
    equals("None request", Q.effective_generation_ceiling(None, 8192), 8192)
    equals("None n_predict", Q.effective_generation_ceiling(8192, None), 8192)


def test_impossible_expanded_budget_is_refused():
    """NPredict=8192, MaxTokens=32768, ReasoningBudget=24576.

    The shape the previous resolver accepted: the explicit ceiling outranked the
    profile, so the reserve was computed as 32768 - 24576 = 8192 and passed. The
    server would have stopped at 8192 with 24576 of those already spent thinking,
    which is a negative answer reserve and a physically impossible baseline.
    """
    print("[impossible expanded budget]")
    resolved = Q.resolve_request_budget(32768, "explicit", 8192, 24576)
    equals("effective ceiling", resolved["effective_generation_ceiling"], 8192)
    equals("request is still recorded", resolved["request_max_tokens"], 32768)
    equals("n_predict is recorded", resolved["n_predict"], 8192)
    equals("answer reserve", resolved["answer_reserve"], -16384)
    equals("minimum reserve", resolved["minimum_answer_reserve"], 8192)
    check("the reserve is not ok", resolved["reserve_ok"] is False)
    equals("budget profile", resolved["budget_profile"], "expanded")
    equals("it is never a baseline",
           Q.resolve_policy_profile("current", False, False,
                                    resolved["budget_profile"], False),
           "diagnostic")
    # Contrast: the same numbers as the profile's own contract are legal.
    served = Q.resolve_request_budget(32768, "profile", 32768, 24576)
    equals("the served Huge contract is ok", served["reserve_ok"], True)
    equals("the served Huge reserve", served["answer_reserve"], 8192)
    equals("the served Huge profile", served["budget_profile"], "deployment")


# --------------------------------------------------------------------------
# budget profile
# --------------------------------------------------------------------------

# (source, request max_tokens, n_predict, expected budget_profile)
# Mirrored by the Assert-BudgetProfile table in Test-V2ConfigGeneration.ps1.
BUDGET_PROFILE_TABLE = [
    # No override at all: every suite keeps its default, which is how a profile
    # without n_predict has always been measured.
    ("fixture", 0, 0, "deployment"),
    # Derived from the profile's own n_predict.
    ("profile", 8192, 8192, "deployment"),
    ("profile", 32768, 32768, "deployment"),
    # An explicit ceiling that restates the contract exactly is still the
    # contract -- the operator typed what the profile declares.
    ("explicit", 8192, 8192, "deployment"),
    ("explicit", 32768, 32768, "deployment"),
    # Below the contract: a NO_ANSWER may be the cap rather than the model.
    ("explicit", 4096, 8192, "constrained"),
    ("explicit", 8192, 32768, "constrained"),
    # Above the contract: the server truncates back to n_predict, so a pass here
    # is not evidence about the deployed profile.
    ("explicit", 32768, 8192, "expanded"),
    ("explicit", 16384, 8192, "expanded"),
    # No declared n_predict: the per-suite fixture defaults are the contract, and
    # a uniform explicit ceiling is compared against the widest of them.
    ("explicit", 8192, 0, "deployment"),
    ("explicit", 4096, 0, "constrained"),
    ("explicit", 16384, 0, "expanded"),
]


def test_budget_profile():
    print("[budget profile]")
    for source, request, npredict, expected in BUDGET_PROFILE_TABLE:
        equals("source=%s request=%d n_predict=%d" % (source, request, npredict),
               Q.budget_profile(source, request, npredict), expected)
    equals("the widest fixture ceiling matches Common.ps1",
           Q.WIDEST_FIXTURE_CEILING, 8192)
    equals("the widest fixture ceiling is the coding default",
           Q.WIDEST_FIXTURE_CEILING, Q.CODING_FIXTURE_DEFAULT_MAX_TOKENS)
    for name in ("deployment", "constrained", "expanded"):
        check("%s is a declared budget profile" % name, name in Q.BUDGET_PROFILES)


def test_policy_profile():
    print("[policy profile]")
    equals("the profile as served is a baseline",
           Q.resolve_policy_profile("current", False, False, "deployment", False),
           "baseline")
    # Every one of these on its own disqualifies the run from being a baseline.
    equals("an agentic system policy is diagnostic",
           Q.resolve_policy_profile("agentic", False, False, "deployment", False),
           "diagnostic")
    equals("a strict tool policy is diagnostic",
           Q.resolve_policy_profile("current", True, False, "deployment", False),
           "diagnostic")
    equals("a tool schema policy is diagnostic",
           Q.resolve_policy_profile("current", False, True, "deployment", False),
           "diagnostic")
    # The two this hardening pass added. An explicit ceiling that is not the
    # profile's contract used to sail through as a baseline.
    equals("a constrained budget is diagnostic",
           Q.resolve_policy_profile("current", False, False, "constrained", False),
           "diagnostic")
    equals("an expanded budget is diagnostic",
           Q.resolve_policy_profile("current", False, False, "expanded", False),
           "diagnostic")
    equals("a named constrained diagnostic is diagnostic",
           Q.resolve_policy_profile("current", False, False, "deployment", True),
           "diagnostic")


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
    # An unknown window is not an infinite one.
    equals("an unknown context window keeps the floor",
           Q.resolve_retention_reserve(16000, None, 32768), (floor, "fixture"))


def test_retention_insufficient_context_reserve():
    """A window with no room left is not a 4096-token request.

    The floor is a floor on what to ask for, never a promise that it fits. This
    used to return (4096, "fixture") for a prefill that leaves 120 tokens, which
    is a request the server has to reject -- prompt + answer > n_ctx. A rejected
    request measures nothing, and a row that carries a rejection reads like a
    model failure.
    """
    print("[retention insufficient context reserve]")
    floor = Q.RETENTION_DEFAULT_OUTPUT_RESERVE
    margin = Q.RETENTION_CONTEXT_MARGIN_TOKENS
    insufficient = Q.INSUFFICIENT_CONTEXT_RESERVE

    # 261000 in a 262144 window leaves 120 after the margin: nowhere near 4096.
    equals("a full window refuses instead of returning the floor",
           Q.resolve_retention_reserve(261000, 262144, 32768), (0, insufficient))
    # The same is true with no profile ceiling at all. This is the path the old
    # code never even checked the window on: `requested <= floor` returned the
    # floor before `room` was computed.
    equals("no profile ceiling does not exempt the window check",
           Q.resolve_retention_reserve(261000, 262144, 0), (0, insufficient))
    equals("a ceiling below the floor does not exempt it either",
           Q.resolve_retention_reserve(261000, 262144, 2048), (0, insufficient))

    # The exact boundary, asserted from both sides so the comparison cannot
    # drift into an off-by-one: room == floor is the last measurable prefill.
    exact = 262144 - floor - margin
    equals("room exactly equal to the floor is still measurable",
           Q.resolve_retention_reserve(exact, 262144, 0), (floor, "fixture"))
    equals("one token more of prefill is not",
           Q.resolve_retention_reserve(exact + 1, 262144, 0), (0, insufficient))

    # Whatever is returned, prompt + reserve + margin must fit the window.
    for prompt in (16000, 200000, 239893, exact, exact + 1, 261000, 262144):
        reserve, source = Q.resolve_retention_reserve(prompt, 262144, 32768)
        if source == insufficient:
            equals("an unmeasurable prefill asks for nothing (%d)" % prompt,
                   reserve, 0)
            continue
        check("prompt %d + reserve %d + margin fits 262144" % (prompt, reserve),
              prompt + reserve + margin <= 262144)

    check("the refusal is a canonical failure name",
          insufficient in Q.CANONICAL_FAILURES)


class _StubServer(object):
    """Just enough server to drive run_retention without a socket.

    `chat` records that it was reached at all. The point of the test below is
    that it never is: a probe whose prefill cannot hold even a floor-sized answer
    must not put a request on the wire.
    """

    def __init__(self, prompt_tokens, n_ctx):
        self._prompt_tokens = prompt_tokens
        self._n_ctx = n_ctx
        self.chat_calls = []

    def context_window(self):
        return self._n_ctx

    def count_tokens(self, _text):
        return self._prompt_tokens

    def chat(self, messages, **kwargs):
        self.chat_calls.append(kwargs)
        raise AssertionError("run_retention sent a request it should have refused")


def test_retention_sends_no_oversized_request():
    """The end-to-end half of the edge case: no request, and a row that says why.

    32000 prompt tokens in a 32768-token window leaves -256 after the margin. The
    old code resolved a 4096-token reserve here and posted it; llama-server can
    only reject that, and a rejected request in a results table reads as a model
    that failed to answer.
    """
    print("[retention sends no oversized request]")
    server = _StubServer(prompt_tokens=32000, n_ctx=32768)
    row = Q.run_retention(server, 32000, request_ceiling=8192)

    equals("no request was sent", server.chat_calls, [])
    equals("the row says so", row.get("requested"), False)
    equals("the reserve is zero, not a floor", row.get("output_reserve"), 0)
    equals("the source names the refusal", row.get("output_reserve_source"),
           Q.INSUFFICIENT_CONTEXT_RESERVE)
    equals("the taxonomy names the refusal", row.get("failure_taxonomy"),
           [Q.INSUFFICIENT_CONTEXT_RESERVE])
    check("the row carries a failure string", bool(row.get("failure")))
    # A refusal is not a score. Nothing here may be read as a retention result.
    for scored in ("score", "probes", "by_family", "exact_count"):
        check("a refused probe reports no %s" % scored, scored not in row)
    equals("the window is recorded for the reader", row.get("n_ctx"), 32768)
    equals("and so is the prompt that filled it", row.get("prompt_tokens"), 32000)


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
    test_effective_generation_ceiling()
    test_impossible_expanded_budget_is_refused()
    test_budget_profile()
    test_policy_profile()
    test_fixture_defaults_are_named()
    test_max_tokens_override()
    test_retention_reserve()
    test_retention_insufficient_context_reserve()
    test_retention_sends_no_oversized_request()
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
