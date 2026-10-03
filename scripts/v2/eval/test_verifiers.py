#!/usr/bin/env python3
"""Self-test for the coding verifiers.

Every verifier is exercised twice: once with a reference solution that must
pass, and once with a plausible-but-wrong solution that must fail. A grader that
accepts everything would report a 2-bit quantization as equal to a 4-bit one,
and the whole campaign would be built on it, so it is checked before any model
is asked a question.

REGRESSIONS adds named cases on top of that pair, each pinning a verdict that
was argued about once. They exist so a claim like "unity_impl is a broken
fixture" can be settled by running something rather than by re-reading a report:
a known-good ProjectilePool compiles against UNITY_SHIM, and the answer
qwen38-27b-deep-32k produced on 2026-08-23 does not, for a reason the C#
compiler names.

Run: python test_verifiers.py <workdir> [task-id ...]
"""
import os
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import coding_tasks as CT  # noqa: E402


GOOD = {}
BAD = {}

GOOD["py_bugfix"] = '''
def find_insert_position(sorted_values, target):
    low, high = 0, len(sorted_values)
    while low < high:
        mid = (low + high) // 2
        if sorted_values[mid] < target:
            low = mid + 1
        else:
            high = mid
    return low
'''
BAD["py_bugfix"] = '''
def find_insert_position(sorted_values, target):
    low, high = 0, len(sorted_values)
    while low < high:
        mid = (low + high) // 2
        if sorted_values[mid] <= target:
            low = mid + 1
        else:
            high = mid
    return low
'''

GOOD["py_impl"] = '''
def merge_intervals(intervals):
    if not intervals:
        return []
    ordered = sorted(([int(a), int(b)] for a, b in intervals), key=lambda p: p[0])
    out = [list(ordered[0])]
    for start, end in ordered[1:]:
        if start <= out[-1][1]:
            out[-1][1] = max(out[-1][1], end)
        else:
            out.append([start, end])
    return out
'''
BAD["py_impl"] = '''
def merge_intervals(intervals):
    if not intervals:
        return []
    ordered = sorted(([int(a), int(b)] for a, b in intervals), key=lambda p: p[0])
    out = [list(ordered[0])]
    for start, end in ordered[1:]:
        if start < out[-1][1]:
            out[-1][1] = max(out[-1][1], end)
        else:
            out.append([start, end])
    return out
'''

GOOD["go_bugfix"] = '''
package pipeline

import "sync"

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
	close(jobs)
	wg.Wait()
	return out
}
'''
BAD["go_bugfix"] = '''
package pipeline

func FanIn(inputs []int, workers int, fn func(int) int) []int {
	out := make([]int, len(inputs))
	for i := range inputs {
		out[i] = fn(inputs[i]) + 1
	}
	return out
}
'''

GOOD["go_impl"] = '''
package cache

import "container/list"

type entry struct {
	key   string
	value int
}

type LRU struct {
	capacity int
	order    *list.List
	items    map[string]*list.Element
}

func NewLRU(capacity int) *LRU {
	if capacity < 1 {
		capacity = 1
	}
	return &LRU{capacity: capacity, order: list.New(), items: make(map[string]*list.Element)}
}

func (c *LRU) Get(key string) (int, bool) {
	el, ok := c.items[key]
	if !ok {
		return 0, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*entry).value, true
}

func (c *LRU) Put(key string, value int) {
	if el, ok := c.items[key]; ok {
		el.Value.(*entry).value = value
		c.order.MoveToFront(el)
		return
	}
	if c.order.Len() >= c.capacity {
		oldest := c.order.Back()
		if oldest != nil {
			c.order.Remove(oldest)
			delete(c.items, oldest.Value.(*entry).key)
		}
	}
	c.items[key] = c.order.PushFront(&entry{key: key, value: value})
}

func (c *LRU) Len() int { return c.order.Len() }
'''
BAD["go_impl"] = '''
package cache

type LRU struct {
	capacity int
	items    map[string]int
}

func NewLRU(capacity int) *LRU { return &LRU{capacity: capacity, items: map[string]int{}} }

func (c *LRU) Get(key string) (int, bool) { v, ok := c.items[key]; return v, ok }

func (c *LRU) Put(key string, value int) { c.items[key] = value }

func (c *LRU) Len() int { return len(c.items) }
'''

GOOD["ts_impl"] = '''
export function groupConsecutive(values: number[]): number[][] {
  const out: number[][] = [];
  let run: number[] = [];
  for (const v of values) {
    if (run.length === 0 || v === run[run.length - 1] + 1) {
      run.push(v);
    } else {
      out.push(run);
      run = [v];
    }
  }
  if (run.length) out.push(run);
  return out;
}
'''
BAD["ts_impl"] = '''
export function groupConsecutive(values: number[]): number[][] {
  const out: number[][] = [];
  let run: number[] = [];
  for (const v of values) {
    if (run.length === 0 || v >= run[run.length - 1]) {
      run.push(v);
    } else {
      out.push(run);
      run = [v];
    }
  }
  if (run.length) out.push(run);
  return out;
}
'''

GOOD["ts_refactor"] = '''
export type Fetcher = (id: string) => Promise<{ id: string; parent: string | null }>;

export async function ancestry(fetch: Fetcher, start: string): Promise<string[]> {
  const seen = new Set<string>();
  const chain: string[] = [];
  let id: string | null = start;
  while (id !== null) {
    if (seen.has(id)) throw new Error("cycle detected");
    seen.add(id);
    chain.push(id);
    if (chain.length > 50) throw new Error("chain too long");
    const node = await fetch(id);
    id = node.parent;
  }
  return chain;
}
'''
BAD["ts_refactor"] = '''
export type Fetcher = (id: string) => Promise<{ id: string; parent: string | null }>;

export async function ancestry(fetch: Fetcher, start: string): Promise<string[]> {
  const chain: string[] = [];
  let id: string | null = start;
  while (id !== null) {
    chain.push(id);
    const node = await fetch(id);
    id = node.parent;
  }
  return chain;
}
'''

GOOD["cs_bugfix"] = '''
using System;
using System.Collections.Generic;
using System.Linq;

public static class Ledger
{
    public static (Dictionary<string, List<int>> Running, Dictionary<string, int> Final, double AverageDelta)
        Summarise(IEnumerable<(string Account, int Delta)> entries)
    {
        var materialised = entries.ToList();
        var totals = new Dictionary<string, int>();
        var running = new Dictionary<string, List<int>>();

        foreach (var e in materialised)
        {
            if (!totals.ContainsKey(e.Account)) { totals[e.Account] = 0; running[e.Account] = new List<int>(); }
            totals[e.Account] += e.Delta;
            running[e.Account].Add(totals[e.Account]);
        }

        var average = materialised.Count == 0 ? 0d : materialised.Sum(e => (double)e.Delta) / materialised.Count;
        return (running, totals, average);
    }
}
'''
BAD["cs_bugfix"] = '''
using System;
using System.Collections.Generic;
using System.Linq;

public static class Ledger
{
    public static (Dictionary<string, List<int>> Running, Dictionary<string, int> Final, double AverageDelta)
        Summarise(IEnumerable<(string Account, int Delta)> entries)
    {
        var materialised = entries.ToList();
        var totals = new Dictionary<string, int>();
        var running = new Dictionary<string, List<int>>();
        var buffer = new List<int>();

        foreach (var e in materialised)
        {
            if (!totals.ContainsKey(e.Account)) { totals[e.Account] = 0; running[e.Account] = buffer; }
            totals[e.Account] += e.Delta;
            running[e.Account].Add(totals[e.Account]);
        }

        var average = materialised.Sum(e => e.Delta) / materialised.Count;
        return (running, totals, average);
    }
}
'''

GOOD["unity_impl"] = '''
using System.Collections.Generic;
using UnityEngine;

public class ProjectilePool : MonoBehaviour
{
    [SerializeField] private GameObject prefab;
    [SerializeField] private int prewarmCount = 8;

    private readonly Stack<GameObject> _available = new Stack<GameObject>();
    private readonly HashSet<GameObject> _pooled = new HashSet<GameObject>();

    private void Awake()
    {
        for (int i = 0; i < prewarmCount; i++)
        {
            var go = Create();
            go.SetActive(false);
            _available.Push(go);
            _pooled.Add(go);
        }
    }

    private GameObject Create()
    {
        var go = Instantiate(prefab, transform);
        return go;
    }

    public GameObject Rent()
    {
        GameObject go = _available.Count > 0 ? _available.Pop() : Create();
        _pooled.Remove(go);
        go.SetActive(true);
        return go;
    }

    public void Return(GameObject go)
    {
        if (go == null) return;
        if (_pooled.Contains(go)) return;
        go.SetActive(false);
        _pooled.Add(go);
        _available.Push(go);
    }
}
'''
BAD["unity_impl"] = '''
using System.Collections.Generic;
using UnityEngine;

public class ProjectilePool : MonoBehaviour
{
    [SerializeField] private GameObject prefab;

    public GameObject Rent()
    {
        // GameObject has no Clone() in the shim, and none in Unity either.
        return prefab.Clone();
    }
}
'''

GOOD["constraint_frozen_file"] = '''
public class TelemetrySender
{
    private readonly NetworkManager _net;

    public TelemetrySender(NetworkManager net) { _net = net; }

    public void Send(string opcode, string payload)
    {
        var safe = opcode.Length > 8 ? opcode.Substring(0, 8) : opcode;
        _net.Enqueue(_net.Encode(safe, payload));
    }
}
'''
BAD["constraint_frozen_file"] = '''
public class TelemetrySender
{
    private readonly NetworkManager _net;
    public TelemetrySender(NetworkManager net) { _net = net; }
    public void Send(string opcode, string payload)
    {
        _net.Enqueue(_net.Encode(opcode, payload));
    }
}
'''

GOOD["no_invented_api"] = '''
public static class FrameUtil
{
    public static int CountZeroes(FrameBuffer buffer)
    {
        int n = 0;
        for (int i = 0; i < buffer.Count; i++)
        {
            if (buffer.Get(i) == 0) n++;
        }
        return n;
    }
}
'''
BAD["no_invented_api"] = '''
using System.Linq;

public static class FrameUtil
{
    public static int CountZeroes(FrameBuffer buffer)
    {
        return buffer.ToArray().Count(b => b == 0);
    }
}
'''

GOOD["hard_missing_info"] = '''
The supplied context does not contain the checksum. Please read the release
notes mentioning REL-42 and the `archive.json` file to retrieve it.
'''
BAD["hard_missing_info"] = '''
The checksum written to archive.json was d41d8cd98f00b204e9800998ecf8427e.
'''


# --------------------------------------------------------------------------
# Named regressions
# --------------------------------------------------------------------------
# Extra cases beyond one GOOD and one BAD per task, each pinning a verdict that
# was argued about once and must never be re-argued from memory. A regression
# may assert on a substring of the toolchain's own output, so a fixture that
# starts failing for a different reason is not silently accepted as still
# working.

# Reproduced verbatim from benchmarks/campaign-20260823-full, the
# qwen38-27b-deep-32k coding suite, task unity_impl. `instance` is declared
# inside the `if (pool.Count > 0)` block and again at method scope, which C#
# rejects with CS0136 regardless of the order the compiler sees them in.
#
# It is here to keep the 2026-08-23 verdict falsifiable rather than remembered:
# the fixture is a real compile gate, this answer really does not compile, and
# the failure belongs to the model. If anyone ever "fixes" unity_impl by
# loosening the gate, this case starts passing and the self-test fails.
DEEP_20260823_CS0136 = '''
using System.Collections.Generic;
using UnityEngine;

public class ProjectilePool : MonoBehaviour
{
    [SerializeField] private GameObject prefab;
    [SerializeField] private int prewarmCount;

    private readonly List<GameObject> pool = new List<GameObject>();

    private void Awake()
    {
        if (prefab == null)
        {
            return;
        }

        if (prewarmCount < 0)
        {
            prewarmCount = 0;
        }

        for (int i = 0; i < prewarmCount; i++)
        {
            GameObject instance = Instantiate(prefab);
            instance.SetActive(false);
            pool.Add(instance);
        }
    }

    public GameObject Rent()
    {
        if (pool.Count > 0)
        {
            int lastIndex = pool.Count - 1;
            GameObject instance = pool[lastIndex];
            pool.RemoveAt(lastIndex);
            instance.SetActive(true);
            return instance;
        }

        if (prefab == null)
        {
            return null;
        }

        GameObject instance = Instantiate(prefab);
        instance.SetActive(true);
        return instance;
    }

    public void Return(GameObject go)
    {
        if (go == null)
        {
            return;
        }

        go.SetActive(false);

        if (!pool.Contains(go))
        {
            pool.Add(go);
        }
    }
}
'''

# The same answer with the second declaration scoped correctly, and nothing else
# changed. Proves the gate rejected the scoping defect and not the approach:
# a List-backed pool written this way compiles against UNITY_SHIM.
DEEP_20260823_CS0136_REPAIRED = DEEP_20260823_CS0136.replace(
    """        GameObject instance = Instantiate(prefab);
        instance.SetActive(true);
        return instance;""",
    """        GameObject spawned = Instantiate(prefab);
        spawned.SetActive(true);
        return spawned;""")

QWEN36_FINAL_MOE_20260825_MISSING_INFO = '''
The supplied context does not contain the checksum. Please read the release
notes mentioning REL-42 and the `archive.json` file to retrieve it.
'''

REGRESSIONS = [
    # (task id, label, source, expect_pass, substring the detail must contain)
    ("unity_impl", "known-good ProjectilePool compiles against UNITY_SHIM",
     GOOD["unity_impl"], True, None),
    ("unity_impl", "Deep 2026-08-23 answer really does not compile (CS0136)",
     DEEP_20260823_CS0136, False, "CS0136"),
    ("unity_impl", "the same answer with the scope repaired does compile",
     DEEP_20260823_CS0136_REPAIRED, True, None),
    ("hard_missing_info", "Qwen3.6 final MoE 2026-08-25 missing-info answer passes",
     QWEN36_FINAL_MOE_20260825_MISSING_INFO, True, None),
]


def run_regressions(workdir, only=None):
    failures = []
    # HARD_TASKS as well as TASKS: the honesty and multi-file regressions live
    # there, and looking in only one registry raises KeyError on a task that
    # does exist.
    tasks = {task["id"]: task for task in list(CT.TASKS) + list(CT.HARD_TASKS)}
    for tid, label, code, expect_pass, expect_detail in REGRESSIONS:
        if only and tid not in only:
            continue
        task = tasks[tid]
        ok, detail = task["verify"](code, workdir)
        text = str(detail)
        problems = []
        if bool(ok) != expect_pass:
            problems.append("expected pass=%s, got %s" % (expect_pass, bool(ok)))
        if expect_detail and expect_detail not in text:
            problems.append("expected %r in the toolchain output" % expect_detail)
        status = "OK  " if not problems else "MISGRADED"
        print("  %-24s regr -> %-5s  %s  (%s)"
              % (tid, bool(ok), status, label), flush=True)
        if problems:
            failures.append("%s regression '%s': %s :: %s"
                            % (tid, label, "; ".join(problems), text[-400:]))
    return failures


def main():
    workdir = sys.argv[1] if len(sys.argv) > 1 else tempfile.mkdtemp(prefix="verif")
    os.makedirs(workdir, exist_ok=True)
    only = set(sys.argv[2:]) if len(sys.argv) > 2 else None

    failures = run_regressions(workdir, only)
    for task in CT.TASKS:
        tid = task["id"]
        if only and tid not in only:
            continue
        if tid not in GOOD:
            failures.append("%s: no reference solution in the self-test" % tid)
            continue

        for expect_pass, code in ((True, GOOD[tid]), (False, BAD[tid])):
            constraint_ok = True
            for marker in task.get("forbidden_markers", []):
                if marker in code:
                    constraint_ok = False
            ok, detail = task["verify"](code, workdir)
            got = bool(ok and constraint_ok)
            tag = "good" if expect_pass else "bad "
            status = "OK  " if got == expect_pass else "MISGRADED"
            print("  %-24s %s -> %-5s  %s" % (tid, tag, got, status), flush=True)
            if got != expect_pass:
                failures.append("%s (%s): expected %s, got %s :: %s"
                                % (tid, tag.strip(), expect_pass, got,
                                   str(detail)[-400:]))

    print("")
    if failures:
        print("VERIFIER SELF-TEST FAILED (%d)" % len(failures))
        for f in failures:
            print("  - %s" % f)
        return 1
    print("VERIFIER SELF-TEST PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
