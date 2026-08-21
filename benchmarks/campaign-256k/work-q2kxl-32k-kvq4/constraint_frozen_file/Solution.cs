public class TelemetrySender
{
    private readonly NetworkManager _networkManager;

    public TelemetrySender(NetworkManager networkManager)
    {
        _networkManager = networkManager;
    }

    public void Send(string opcode, string payload)
    {
        string truncatedOpcode = opcode;

        if (truncatedOpcode != null && truncatedOpcode.Length > 8)
        {
            truncatedOpcode = truncatedOpcode.Substring(0, 8);
        }

        string frame = _networkManager.Encode(truncatedOpcode, payload);
        _networkManager.Enqueue(frame);
    }
}
