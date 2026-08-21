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
        long deltaSum = 0;
        int deltaCount = 0;

        if (entries != null)
        {
            foreach (var e in entries)
            {
                if (!totals.TryGetValue(e.Account, out int current))
                {
                    current = 0;
                    totals[e.Account] = current;
                    running[e.Account] = new List<int>();
                }

                current += e.Delta;
                totals[e.Account] = current;
                running[e.Account].Add(current);

                deltaSum += e.Delta;
                deltaCount++;
            }
        }

        double averageDelta = deltaCount == 0 ? 0.0 : (double)deltaSum / deltaCount;

        return (Running: running, Final: totals, AverageDelta: averageDelta);
    }
}
