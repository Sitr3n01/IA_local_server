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
