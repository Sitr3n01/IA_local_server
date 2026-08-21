#!/usr/bin/env python3
"""Coding, tool-calling and instruction-adherence fixtures with real verifiers.

Every task in the coding half is scored by a compiler or a test runner, not by a
judge and not by a regex over prose. Python runs hidden tests, Go runs `go test`,
TypeScript runs under Node's native type stripping, C# and the Unity fixture are
compiled by `dotnet build` (the Unity one against a minimal UnityEngine shim, so
the MonoBehaviour code is genuinely type-checked without a Unity install).

That distinction matters for this campaign specifically. The question is whether
a 3-bit or 2-bit quantization still writes code that *works*, and a grader that
accepts plausible-looking code cannot answer it — a degraded quant's failure mode
is code that reads correctly and does not compile, or compiles and returns the
wrong answer on the second edge case.

Two families are deliberately not compiled, because what they test is not
compilation: `no_invented_api` passes only if the model refuses to call a method
that does not exist on the provided surface (compilation does catch that one),
and the constraint family passes only if the model leaves a named file alone.
"""
import json
import os
import re
import shutil
import subprocess
import sys

GO_EXE = r"C:\IA\toolchains\go1.26.5\go\bin\go.exe"
DOTNET_EXE = "dotnet"
NODE_EXE = "node"
PY_EXE = sys.executable

FENCE_RE = re.compile(r"```[ \t]*([A-Za-z0-9_+#-]*)[ \t]*\r?\n(.*?)```", re.S)
THINK_RE = re.compile(r"(?is)<think>.*?</think>")


def strip_reasoning(text):
    if not text:
        return ""
    text = THINK_RE.sub("", text)
    # A truncated reasoning block leaves an opening tag with no close; keep what
    # follows the last stray tag rather than discarding the whole answer.
    if "</think>" in text:
        text = text.rsplit("</think>", 1)[1]
    return text.strip()


def extract_code(text, langs=(), pick="last"):
    """Pull a fenced block out of a reply.

    Prefers a fence tagged with one of `langs`; falls back to any fence; falls
    back to the whole reply when the model answered with bare code. `pick`
    decides which block wins when several match, because a model that shows the
    broken original before the fix puts the answer last, while one that answers
    first and then explains puts it first.
    """
    text = strip_reasoning(text)
    blocks = FENCE_RE.findall(text)
    if not blocks:
        return text.strip()
    tagged = [body for tag, body in blocks if tag.lower() in langs]
    chosen = tagged or [body for _, body in blocks]
    return (chosen[-1] if pick == "last" else chosen[0]).strip()


def _run(cmd, cwd, timeout=180, env=None):
    merged = dict(os.environ)
    if env:
        merged.update(env)
    try:
        proc = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True,
                              timeout=timeout, env=merged, encoding="utf-8",
                              errors="replace")
        return proc.returncode, (proc.stdout or "") + (proc.stderr or "")
    except subprocess.TimeoutExpired:
        return 124, "TIMEOUT after %ss" % timeout
    except FileNotFoundError as exc:
        return 127, "toolchain missing: %s" % exc


def _fresh(workdir, name):
    path = os.path.join(workdir, name)
    if os.path.isdir(path):
        shutil.rmtree(path, ignore_errors=True)
    os.makedirs(path, exist_ok=True)
    return path


def _write(path, name, content):
    full = os.path.join(path, name)
    os.makedirs(os.path.dirname(full), exist_ok=True)
    with open(full, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(content)
    return full


# --------------------------------------------------------------------------
# Python
# --------------------------------------------------------------------------

PY_BUGFIX_SRC = '''\
def find_insert_position(sorted_values, target):
    """Return the index where target should be inserted to keep the list sorted.

    If target is already present, return the index of its FIRST occurrence.
    """
    low = 0
    high = len(sorted_values)
    while low < high:
        mid = (low + high) // 2
        if sorted_values[mid] < target:
            low = mid
        else:
            high = mid
    return low
'''

PY_BUGFIX_TESTS = '''\
from solution import find_insert_position as f
cases = [
    (([1, 3, 5, 7], 4), 2),
    (([1, 3, 5, 7], 1), 0),
    (([1, 3, 5, 7], 0), 0),
    (([1, 3, 5, 7], 9), 4),
    (([1, 1, 1, 1], 1), 0),
    (([2, 2, 4, 4, 6], 4), 2),
    (([], 5), 0),
    (([1], 1), 0),
    (([1], 2), 1),
    ((list(range(0, 200, 2)), 101), 51),
]
bad = []
for args, want in cases:
    got = f(*args)
    if got != want:
        bad.append((args, want, got))
if bad:
    raise SystemExit("FAIL " + repr(bad))
print("OK")
'''

PY_IMPL_TESTS = '''\
from solution import merge_intervals as m
cases = [
    ([[1, 3], [2, 6], [8, 10], [15, 18]], [[1, 6], [8, 10], [15, 18]]),
    ([[1, 4], [4, 5]], [[1, 5]]),
    ([[1, 4], [5, 6]], [[1, 4], [5, 6]]),
    ([], []),
    ([[5, 6], [1, 2]], [[1, 2], [5, 6]]),
    ([[1, 10], [2, 3], [4, 5]], [[1, 10]]),
    ([[1, 1]], [[1, 1]]),
    ([[3, 4], [1, 2], [2, 3]], [[1, 4]]),
]
bad = []
for args, want in cases:
    got = m([list(x) for x in args])
    if [list(x) for x in got] != want:
        bad.append((args, want, got))
if bad:
    raise SystemExit("FAIL " + repr(bad))
print("OK")
'''


def verify_python(code, workdir, tests, tag):
    path = _fresh(workdir, tag)
    _write(path, "solution.py", code + "\n")
    _write(path, "check.py", tests)
    rc, out = _run([PY_EXE, "check.py"], path, timeout=60)
    return rc == 0 and "OK" in out, out[-1500:]


# --------------------------------------------------------------------------
# Go
# --------------------------------------------------------------------------

GO_BUGFIX_SRC = '''\
package pipeline

import "sync"

// FanIn runs fn over every input concurrently and returns the results in the
// same order as the inputs. It currently deadlocks on any non-empty input.
func FanIn(inputs []int, workers int, fn func(int) int) []int {
	out := make([]int, len(inputs))
	jobs := make(chan int)
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				out[i] = fn(inputs[i])
			}
		}()
	}

	for i := range inputs {
		jobs <- i
	}
	wg.Wait()
	close(jobs)
	return out
}
'''

GO_BUGFIX_TESTS = '''\
package pipeline

import "testing"

func TestFanInOrder(t *testing.T) {
	in := make([]int, 500)
	for i := range in {
		in[i] = i
	}
	got := FanIn(in, 8, func(v int) int { return v * 3 })
	for i, v := range got {
		if v != i*3 {
			t.Fatalf("index %d: got %d want %d", i, v, i*3)
		}
	}
}

func TestFanInEmpty(t *testing.T) {
	if got := FanIn(nil, 4, func(v int) int { return v }); len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestFanInSingleWorker(t *testing.T) {
	got := FanIn([]int{1, 2, 3}, 1, func(v int) int { return v + 1 })
	want := []int{2, 3, 4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %d want %d", i, got[i], want[i])
		}
	}
}
'''

GO_IMPL_TESTS = '''\
package cache

import "testing"

func TestLRUBasic(t *testing.T) {
	c := NewLRU(2)
	c.Put("a", 1)
	c.Put("b", 2)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("a: got %v %v", v, ok)
	}
	c.Put("c", 3) // evicts b, because a was just read
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if v, ok := c.Get("c"); !ok || v != 3 {
		t.Fatalf("c: got %v %v", v, ok)
	}
	if c.Len() != 2 {
		t.Fatalf("len: got %d want 2", c.Len())
	}
}

func TestLRUUpdateDoesNotGrow(t *testing.T) {
	c := NewLRU(2)
	c.Put("a", 1)
	c.Put("a", 9)
	if c.Len() != 1 {
		t.Fatalf("len: got %d want 1", c.Len())
	}
	if v, _ := c.Get("a"); v != 9 {
		t.Fatalf("a: got %v want 9", v)
	}
}

func TestLRUEvictionOrder(t *testing.T) {
	c := NewLRU(3)
	c.Put("x", 1)
	c.Put("y", 2)
	c.Put("z", 3)
	c.Get("x")
	c.Put("w", 4) // y is now the least recently used
	if _, ok := c.Get("y"); ok {
		t.Fatal("y should have been evicted")
	}
	if _, ok := c.Get("x"); !ok {
		t.Fatal("x should have survived")
	}
}
'''


def verify_go(code, workdir, tests, tag, pkg):
    path = _fresh(workdir, tag)
    _write(path, "go.mod", "module eval/%s\n\ngo 1.24\n" % pkg)
    _write(path, "%s.go" % pkg, code + "\n")
    _write(path, "%s_test.go" % pkg, tests)
    rc, out = _run([GO_EXE, "test", "./..."], path, timeout=240,
                   env={"GOFLAGS": "-mod=mod", "GOCACHE": os.path.join(workdir, ".gocache"),
                        "GOMODCACHE": os.path.join(workdir, ".gomodcache"),
                        "GOTOOLCHAIN": "local"})
    return rc == 0, out[-1500:]


# --------------------------------------------------------------------------
# TypeScript (Node native type stripping)
# --------------------------------------------------------------------------

TS_IMPL_TESTS = '''\
import { groupConsecutive } from "./solution.ts";

function eq(a: unknown, b: unknown, label: string) {
  const sa = JSON.stringify(a), sb = JSON.stringify(b);
  if (sa !== sb) throw new Error(`${label}: got ${sa} want ${sb}`);
}

eq(groupConsecutive([1, 2, 4, 5, 6, 9]), [[1, 2], [4, 5, 6], [9]], "basic");
eq(groupConsecutive([]), [], "empty");
eq(groupConsecutive([7]), [[7]], "single");
eq(groupConsecutive([3, 3, 4]), [[3], [3, 4]], "duplicate breaks run");
eq(groupConsecutive([5, 4, 3]), [[5], [4], [3]], "descending never groups");
eq(groupConsecutive([-2, -1, 0, 2]), [[-2, -1, 0], [2]], "negatives");
console.log("OK");
'''

TS_REFACTOR_SRC = '''\
export type Fetcher = (id: string) => Promise<{ id: string; parent: string | null }>;

// Walks from a node to the root and returns the chain, root last.
// Rewrite this to use async/await and a plain loop. Behaviour must not change,
// including that a cycle throws Error("cycle detected") and that the chain is
// capped at 50 entries with Error("chain too long").
export function ancestry(fetch: Fetcher, start: string): Promise<string[]> {
  const seen = new Set<string>();
  const chain: string[] = [];
  function step(id: string): Promise<string[]> {
    if (seen.has(id)) return Promise.reject(new Error("cycle detected"));
    seen.add(id);
    chain.push(id);
    if (chain.length > 50) return Promise.reject(new Error("chain too long"));
    return fetch(id).then((node) => {
      if (node.parent === null) return chain;
      return step(node.parent);
    });
  }
  return step(start);
}
'''

TS_REFACTOR_TESTS = '''\
import { ancestry } from "./solution.ts";

const tree: Record<string, string | null> = {
  a: "b", b: "c", c: null, loopX: "loopY", loopY: "loopX",
};
const fetch = async (id: string) => ({ id, parent: tree[id] ?? null });

function eq(a: unknown, b: unknown, label: string) {
  const sa = JSON.stringify(a), sb = JSON.stringify(b);
  if (sa !== sb) throw new Error(`${label}: got ${sa} want ${sb}`);
}

const main = async () => {
  eq(await ancestry(fetch, "a"), ["a", "b", "c"], "chain");
  eq(await ancestry(fetch, "c"), ["c"], "root only");

  let cycled = "";
  try { await ancestry(fetch, "loopX"); } catch (e) { cycled = (e as Error).message; }
  eq(cycled, "cycle detected", "cycle");

  const deep: Record<string, string> = {};
  for (let i = 0; i < 80; i++) deep[`n${i}`] = `n${i + 1}`;
  const deepFetch = async (id: string) => ({ id, parent: deep[id] ?? null });
  let tooLong = "";
  try { await ancestry(deepFetch, "n0"); } catch (e) { tooLong = (e as Error).message; }
  eq(tooLong, "chain too long", "cap");

  // The refactor must not leave recursion behind.
  const src = await (await import("node:fs/promises")).readFile("solution.ts", "utf8");
  if (/\\bfunction\\s+step\\b/.test(src) || /\\.then\\s*\\(/.test(src)) {
    throw new Error("still uses the callback/recursive form");
  }
  console.log("OK");
};
main().catch((e) => { console.error(String(e)); process.exit(1); });
'''


def verify_ts(code, workdir, tests, tag):
    path = _fresh(workdir, tag)
    _write(path, "package.json", json.dumps({"name": "eval", "type": "module"}))
    _write(path, "solution.ts", code + "\n")
    _write(path, "check.ts", tests)
    rc, out = _run([NODE_EXE, "check.ts"], path, timeout=120)
    return rc == 0 and "OK" in out, out[-1500:]


# --------------------------------------------------------------------------
# C# and the Unity shim
# --------------------------------------------------------------------------

CSPROJ = '''<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <OutputType>Exe</OutputType>
    <TargetFramework>net10.0</TargetFramework>
    <Nullable>disable</Nullable>
    <ImplicitUsings>enable</ImplicitUsings>
    <AssemblyName>evalapp</AssemblyName>
    <RootNamespace>evalapp</RootNamespace>
    <EnableDefaultCompileItems>true</EnableDefaultCompileItems>
    <WarningLevel>0</WarningLevel>
    <NoWarn>CS0169;CS0414;CS0649;CS8524</NoWarn>
  </PropertyGroup>
</Project>
'''

CS_BUGFIX_SRC = '''\
using System;
using System.Collections.Generic;
using System.Linq;

public static class Ledger
{
    // Returns, for each account, the running balance after every entry, and the
    // final balance. Entries are (account, delta) in chronological order.
    // BUG: callers report that the running balances are all identical to the
    // final balance, and that AverageDelta is truncated to a whole number.
    public static (Dictionary<string, List<int>> Running, Dictionary<string, int> Final, double AverageDelta)
        Summarise(IEnumerable<(string Account, int Delta)> entries)
    {
        var totals = new Dictionary<string, int>();
        var running = new Dictionary<string, List<int>>();
        var buffer = new List<int>();

        foreach (var e in entries)
        {
            if (!totals.ContainsKey(e.Account)) { totals[e.Account] = 0; running[e.Account] = buffer; }
            totals[e.Account] += e.Delta;
            running[e.Account].Add(totals[e.Account]);
        }

        var deltas = entries.Select(e => e.Delta);
        var average = deltas.Sum() / deltas.Count();
        return (running, totals, average);
    }
}
'''

CS_BUGFIX_MAIN = '''\
using System;
using System.Collections.Generic;
using System.Linq;

public static class Program
{
    public static int Main()
    {
        var entries = new List<(string, int)>
        {
            ("a", 10), ("b", 5), ("a", -3), ("b", 2), ("a", 1)
        };
        var (running, final, avg) = Ledger.Summarise(entries);

        var problems = new List<string>();
        if (!running.ContainsKey("a") || !running["a"].SequenceEqual(new[] { 10, 7, 8 }))
            problems.Add("running[a]=" + string.Join(",", running.TryGetValue("a", out var ra) ? ra : new List<int>()));
        if (!running.ContainsKey("b") || !running["b"].SequenceEqual(new[] { 5, 7 }))
            problems.Add("running[b]=" + string.Join(",", running.TryGetValue("b", out var rb) ? rb : new List<int>()));
        if (final["a"] != 8) problems.Add("final[a]=" + final["a"]);
        if (final["b"] != 7) problems.Add("final[b]=" + final["b"]);
        if (Math.Abs(avg - 3.0) > 1e-9) problems.Add("avg=" + avg);

        if (problems.Count > 0) { Console.WriteLine("FAIL " + string.Join(" | ", problems)); return 1; }
        Console.WriteLine("OK");
        return 0;
    }
}
'''

UNITY_SHIM = '''\
// Minimal UnityEngine surface. Enough to type-check a MonoBehaviour without a
// Unity install; deliberately NOT a Unity emulator.
namespace UnityEngine
{
    public class Object { public string name; public static void Destroy(Object o) { } }
    public class Component : Object { public GameObject gameObject; public Transform transform; }
    public class Behaviour : Component { public bool enabled; }
    public class Transform : Component { public Vector3 position; public void SetParent(Transform p) { } }
    public class GameObject : Object
    {
        public GameObject() { }
        public GameObject(string n) { name = n; }
        public Transform transform { get; } = new Transform();
        public bool activeSelf { get; private set; }
        public void SetActive(bool v) { activeSelf = v; }
        public T GetComponent<T>() where T : Component => null;
        public T AddComponent<T>() where T : Component, new() => new T();
    }
    public struct Vector3
    {
        public float x, y, z;
        public Vector3(float x, float y, float z) { this.x = x; this.y = y; this.z = z; }
        public static Vector3 zero => new Vector3(0, 0, 0);
    }
    public class MonoBehaviour : Behaviour
    {
        public static T Instantiate<T>(T original) where T : Object => original;
        public static T Instantiate<T>(T original, Transform parent) where T : Object => original;
    }
    public class ScriptableObject : Object { }
    public static class Debug
    {
        public static void Log(object m) { }
        public static void LogWarning(object m) { }
        public static void LogError(object m) { }
    }
    // Mathf is ordinary UnityEngine surface and a model reaching for Mathf.Max
    // is writing correct Unity. Its absence was a gap in this shim that scored
    // as a model compile failure; measured once against IQ4_XS on unity_impl.
    public static class Mathf
    {
        public static int Max(int a, int b) => a > b ? a : b;
        public static int Min(int a, int b) => a < b ? a : b;
        public static int Clamp(int v, int lo, int hi) => v < lo ? lo : (v > hi ? hi : v);
        public static float Max(float a, float b) => a > b ? a : b;
        public static float Min(float a, float b) => a < b ? a : b;
        public static float Clamp(float v, float lo, float hi) => v < lo ? lo : (v > hi ? hi : v);
        public static float Clamp01(float v) => Clamp(v, 0f, 1f);
        public static float Abs(float v) => v < 0 ? -v : v;
        public static int Abs(int v) => v < 0 ? -v : v;
        public static int CeilToInt(float v) => (int)System.Math.Ceiling(v);
        public static int FloorToInt(float v) => (int)System.Math.Floor(v);
        public static int RoundToInt(float v) => (int)System.Math.Round(v);
        public const float Epsilon = 1.401298E-45f;
    }
    public class SerializeFieldAttribute : System.Attribute { }
    public class RequireComponent : System.Attribute { public RequireComponent(System.Type t) { } }
}
'''

UNITY_MAIN = '''\
public static class Program
{
    // The Unity fixture is a compile gate: if the MonoBehaviour type-checks
    // against the shim, the API surface the model used exists.
    public static int Main()
    {
        System.Console.WriteLine("OK");
        return 0;
    }
}
'''

NETMANAGER_SRC = '''\
// NetworkManager.cs -- FROZEN. Reproduces a wire format other clients depend on.
using System;
using System.Collections.Generic;

public class NetworkManager
{
    private readonly List<string> _outbox = new List<string>();

    // Serialises a payload for the wire. Pads every field to 8 characters and
    // uppercases the opcode. This is the shipped format and cannot change.
    public string Encode(string opcode, string payload)
    {
        return opcode.ToUpperInvariant().PadRight(8, ' ') + payload.PadRight(8, ' ');
    }

    public void Enqueue(string frame) { _outbox.Add(frame); }
    public IReadOnlyList<string> Outbox => _outbox;
}
'''

NETMANAGER_MAIN = '''\
using System;
using System.Linq;

public static class Program
{
    public static int Main()
    {
        var net = new NetworkManager();
        var sender = new TelemetrySender(net);
        sender.Send("ping", "abc");
        sender.Send("verylongopcode", "xy");

        var problems = new System.Collections.Generic.List<string>();
        if (net.Outbox.Count != 2) problems.Add("outbox=" + net.Outbox.Count);
        // The requirement: opcodes longer than 8 characters must be rejected by
        // the CALLER, never truncated, and never by editing NetworkManager.
        else
        {
            if (net.Outbox[0] != "PING    abc     ") problems.Add("frame0=[" + net.Outbox[0] + "]");
            if (net.Outbox[1] != "VERYLONGxy      ") problems.Add("frame1=[" + net.Outbox[1] + "]");
        }
        if (problems.Count > 0) { Console.WriteLine("FAIL " + string.Join(" | ", problems)); return 1; }
        Console.WriteLine("OK");
        return 0;
    }
}
'''


def verify_csharp(code, workdir, tag, extra_files=(), run=True):
    path = _fresh(workdir, tag)
    _write(path, "eval.csproj", CSPROJ)
    _write(path, "Solution.cs", code + "\n")
    for name, content in extra_files:
        _write(path, name, content)
    nuget = os.path.join(workdir, ".nuget")
    env = {"DOTNET_CLI_TELEMETRY_OPTOUT": "1", "DOTNET_NOLOGO": "1",
           "NUGET_PACKAGES": nuget, "DOTNET_SKIP_FIRST_TIME_EXPERIENCE": "1"}
    verb = ["run", "--no-launch-profile"] if run else ["build"]
    rc, out = _run([DOTNET_EXE] + verb + ["-v", "q", "--nologo"], path, timeout=420, env=env)
    ok = rc == 0 and (("OK" in out) if run else True)
    return ok, out[-1800:]


# --------------------------------------------------------------------------
# Task table
# --------------------------------------------------------------------------

SYS_CODER = (
    "You are a senior engineer working in an existing repository. Return the "
    "complete corrected or requested source file in a single fenced code block. "
    "Do not add commentary outside the block. Do not invent APIs that are not "
    "shown to you."
)


def _t(**kw):
    kw.setdefault("system", SYS_CODER)
    # Matches max_output_tokens in the shipped manifest profiles. It is not
    # generous: Qwen3.8 reasons before answering, and a 2560-token budget was
    # measured spending all 2560 inside reasoning_content and emitting an empty
    # content field, which the grader then scored as broken code. The output cap
    # has to sit above what the model actually spends thinking or the suite
    # measures the harness.
    kw.setdefault("max_tokens", 8192)
    kw.setdefault("langs", ())
    kw.setdefault("pick", "last")
    return kw


TASKS = [
    _t(id="py_bugfix", family="bugfix", lang="python", langs=("python", "py"),
       prompt="This function hangs on some inputs and returns the wrong index on "
              "others. Fix it. Keep the name and signature.\n\n```python\n"
              + PY_BUGFIX_SRC + "```",
       verify=lambda code, wd: verify_python(code, wd, PY_BUGFIX_TESTS, "py_bugfix")),

    _t(id="py_impl", family="implement", lang="python", langs=("python", "py"),
       prompt="Implement `merge_intervals(intervals)` in Python. It takes a list of "
              "[start, end] pairs in any order and returns a new list of "
              "non-overlapping [start, end] pairs sorted by start, merging any "
              "that overlap or touch (so [1,4] and [4,5] merge into [1,5]). Do not "
              "mutate the input. Return only the function.",
       verify=lambda code, wd: verify_python(code, wd, PY_IMPL_TESTS, "py_impl")),

    _t(id="go_bugfix", family="bugfix", lang="go", langs=("go", "golang"),
       prompt="This Go package deadlocks. Fix it so the tests pass, keeping the "
              "exported signature and the ordering guarantee. Return the whole "
              "file.\n\n```go\n" + GO_BUGFIX_SRC + "```",
       verify=lambda code, wd: verify_go(code, wd, GO_BUGFIX_TESTS, "go_bugfix", "pipeline")),

    _t(id="go_impl", family="implement", lang="go", langs=("go", "golang"),
       prompt="Write a Go file for `package cache` implementing a fixed-capacity "
              "LRU cache with:\n"
              "  func NewLRU(capacity int) *LRU\n"
              "  func (c *LRU) Get(key string) (int, bool)\n"
              "  func (c *LRU) Put(key string, value int)\n"
              "  func (c *LRU) Len() int\n"
              "Get counts as a use. Put on an existing key updates it without "
              "growing the cache. When full, Put evicts the least recently used "
              "entry. Use only the standard library. Return the whole file.",
       verify=lambda code, wd: verify_go(code, wd, GO_IMPL_TESTS, "go_impl", "cache")),

    _t(id="ts_impl", family="implement", lang="typescript", langs=("typescript", "ts"),
       prompt="Write a TypeScript module exporting:\n\n"
              "  export function groupConsecutive(values: number[]): number[][]\n\n"
              "It splits the array into runs where each element is exactly one "
              "greater than the previous one. A repeated value starts a new run. "
              "An empty input returns an empty array. Use only erasable type "
              "syntax (no enums, no namespaces, no parameter properties). Return "
              "the whole module.",
       verify=lambda code, wd: verify_ts(code, wd, TS_IMPL_TESTS, "ts_impl")),

    _t(id="ts_refactor", family="refactor", lang="typescript", langs=("typescript", "ts"),
       prompt="Refactor this module to use async/await and an iterative loop "
              "instead of the recursive `.then()` chain. Every observable "
              "behaviour must be preserved exactly, including both error "
              "messages and the 50-entry cap. Keep the exported names. Return the "
              "whole module.\n\n```typescript\n" + TS_REFACTOR_SRC + "```",
       verify=lambda code, wd: verify_ts(code, wd, TS_REFACTOR_TESTS, "ts_refactor")),

    _t(id="cs_bugfix", family="bugfix", lang="csharp", langs=("csharp", "cs", "c#"),
       prompt="Two bugs are reported against this C# class: every account's "
              "running-balance list contains the same entries, and AverageDelta "
              "comes back as a whole number. Fix both. Keep the public signature. "
              "Return the whole file.\n\n```csharp\n" + CS_BUGFIX_SRC + "```",
       verify=lambda code, wd: verify_csharp(code, wd, "cs_bugfix",
                                             extra_files=[("Program.cs", CS_BUGFIX_MAIN)])),

    _t(id="unity_impl", family="implement", lang="csharp", langs=("csharp", "cs", "c#"),
       prompt="Write a Unity MonoBehaviour `ProjectilePool` in C# that pools "
              "GameObjects. Requirements:\n"
              "  - a [SerializeField] GameObject prefab and int prewarmCount\n"
              "  - Awake() prewarms `prewarmCount` inactive instances\n"
              "  - public GameObject Rent() returns a pooled instance, activating "
              "it, and instantiates a new one only when the pool is empty\n"
              "  - public void Return(GameObject go) deactivates it and puts it back\n"
              "  - Return(null) is ignored; returning the same object twice must "
              "not add it twice\n"
              "Use only UnityEngine and System.Collections.Generic. Return the "
              "whole file.",
       verify=lambda code, wd: verify_csharp(code, wd, "unity_impl", run=False,
                                             extra_files=[("UnityShim.cs", UNITY_SHIM),
                                                          ("Program.cs", UNITY_MAIN)])),

    _t(id="constraint_frozen_file", family="constraint", lang="csharp",
       langs=("csharp", "cs", "c#"),
       system=SYS_CODER + " DO NOT MODIFY NetworkManager.cs UNDER ANY "
              "CIRCUMSTANCES. It is frozen.",
       prompt="DO NOT MODIFY NetworkManager.cs.\n\n"
              "Here is the frozen file, for reference only:\n\n```csharp\n"
              + NETMANAGER_SRC + "```\n\n"
              "Write a NEW file `TelemetrySender.cs` containing a class "
              "`TelemetrySender` with a constructor taking a NetworkManager and a "
              "method `public void Send(string opcode, string payload)`.\n"
              "Send must encode the frame through the NetworkManager and enqueue "
              "it. Opcodes longer than 8 characters must be truncated to their "
              "first 8 characters BY YOUR CLASS before encoding — "
              "NetworkManager.Encode must not be changed to do it.\n"
              "Return only TelemetrySender.cs.",
       # The frozen file is supplied by the harness, not by the model. If the
       # model returned an edited NetworkManager instead, the extracted code
       # fails to compile as TelemetrySender and the constraint check below
       # records it separately.
       verify=lambda code, wd: verify_csharp(code, wd, "constraint_frozen_file",
                                             extra_files=[("NetworkManager.cs", NETMANAGER_SRC),
                                                          ("Program.cs", NETMANAGER_MAIN)]),
       forbidden_markers=["class NetworkManager", "public string Encode("]),

    _t(id="no_invented_api", family="honesty", lang="csharp",
       langs=("csharp", "cs", "c#"),
       prompt="Here is the complete surface of a class you must use. It has no "
              "other members.\n\n```csharp\npublic sealed class FrameBuffer\n{\n"
              "    public int Count { get; }\n"
              "    public byte Get(int index);\n"
              "    public void Append(byte value);\n}\n```\n\n"
              "Write `public static class FrameUtil` with "
              "`public static int CountZeroes(FrameBuffer buffer)` returning how "
              "many bytes equal zero. Use only the members shown above — "
              "FrameBuffer has no Clear, no ToArray, no indexer, no enumerator, "
              "and no Length. Return only FrameUtil.",
       verify=lambda code, wd: verify_csharp(code, wd, "no_invented_api", run=False,
                                             extra_files=[("FrameBuffer.cs", '''\
public sealed class FrameBuffer
{
    private byte[] _data = new byte[0];
    public int Count => _data.Length;
    public byte Get(int index) => _data[index];
    public void Append(byte value)
    {
        var next = new byte[_data.Length + 1];
        System.Array.Copy(_data, next, _data.Length);
        next[_data.Length] = value;
        _data = next;
    }
}
'''), ("Program.cs", UNITY_MAIN)])),
]


# --------------------------------------------------------------------------
# Tool-calling and structured-output fixtures
# --------------------------------------------------------------------------

TOOLS = [
    {"type": "function", "function": {
        "name": "read_file",
        "description": "Read a file from the repository.",
        "parameters": {"type": "object", "properties": {
            "path": {"type": "string", "description": "Repository-relative path."},
            "start_line": {"type": "integer"},
            "end_line": {"type": "integer"}},
            "required": ["path"]}}},
    {"type": "function", "function": {
        "name": "edit_file",
        "description": "Replace the full contents of a file.",
        "parameters": {"type": "object", "properties": {
            "path": {"type": "string"},
            "contents": {"type": "string"}},
            "required": ["path", "contents"]}}},
    {"type": "function", "function": {
        "name": "run_tests",
        "description": "Run the test suite for one package.",
        "parameters": {"type": "object", "properties": {
            "package": {"type": "string"},
            "verbose": {"type": "boolean"}},
            "required": ["package"]}}},
    {"type": "function", "function": {
        "name": "run_build",
        "description": "Build one target.",
        "parameters": {"type": "object", "properties": {
            "target": {"type": "string"},
            "configuration": {"type": "string", "enum": ["Debug", "Release"]}},
            "required": ["target"]}}},
    {"type": "function", "function": {
        "name": "list_dir",
        "description": "List a directory.",
        "parameters": {"type": "object", "properties": {
            "path": {"type": "string"}}, "required": ["path"]}}},
]

TOOL_TASKS = [
    {"id": "tool_pick_tests",
     "prompt": "The package `internal/edge` has a failing test and I need to see "
               "the full output. Run its tests verbosely. Use a tool.",
     "want_name": "run_tests",
     "want_args": {"package": "internal/edge", "verbose": True}},
    {"id": "tool_pick_build",
     "prompt": "Build the target `cia-edge` in Release configuration. Use a tool.",
     "want_name": "run_build",
     "want_args": {"target": "cia-edge", "configuration": "Release"}},
    {"id": "tool_pick_read_range",
     "prompt": "Show me lines 40 through 80 of config/models.schema.json. Use a tool.",
     "want_name": "read_file",
     "want_args": {"path": "config/models.schema.json", "start_line": 40, "end_line": 80}},
    {"id": "tool_refuse_edit",
     "prompt": "NetworkManager.cs is frozen and must never be edited. The opcode "
               "padding looks wrong in it. Do not edit it. Instead, list the "
               "directory `src/net` so I can find the caller. Use a tool.",
     "want_name": "list_dir",
     "want_args": {"path": "src/net"},
     "forbid_name": "edit_file"},
]

JSON_TASK = {
    "id": "structured_json",
    "prompt": "Reply with a single JSON object and nothing else, with exactly the "
              "keys \"language\", \"lines\", \"has_tests\", \"entrypoints\". "
              "For a Go package with 412 lines, a _test.go file, and the exported "
              "functions NewLRU, Get, Put and Len, fill it in. \"lines\" must be a "
              "number, \"has_tests\" a boolean, \"entrypoints\" an array of strings.",
    "want": {"language": "go", "lines": 412, "has_tests": True,
             "entrypoints": ["NewLRU", "Get", "Put", "Len"]},
}
