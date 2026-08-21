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
