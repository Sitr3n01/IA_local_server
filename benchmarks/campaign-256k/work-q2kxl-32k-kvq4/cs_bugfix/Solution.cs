using System;
using System.Collections.Generic;

public static class Ledger
{
    // Returns, for each account, the running balance after every entry, and the
    // final balance. Entries are (account, delta) in chronological order.
    public static (Dictionary<string, List<int>> Running, Dictionary<string, int> Final, double AverageDelta)
        Summarise(IEnumerable<(string Account, int Delta)> entries)
    {
        var totals = new Dictionary<string, int>();
        var running = new Dictionary<string, List<int>>();
        long sumDeltas = 0;
        long count = 0;

        foreach (var e in entries)
        {
            if (!totals.ContainsKey(e.Account))
            {
                totals[e.Account] = 0;
                running[e.Account] = new List<int>();
            }

            totals[e.Account] += e.Delta;
            running[e.Account].Add(totals[e.Account]);

            sumDeltas += e.Delta;
            count++;
        }

        var average = count == 0 ? 0.0 : (double)sumDeltas / count;

        return (running, totals, average);
    }
}
