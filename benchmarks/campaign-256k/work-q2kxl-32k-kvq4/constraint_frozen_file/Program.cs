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
