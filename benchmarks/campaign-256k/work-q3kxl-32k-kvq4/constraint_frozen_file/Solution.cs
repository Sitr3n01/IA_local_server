public class TelemetrySender
{
    private readonly NetworkManager _networkManager;

    public TelemetrySender(NetworkManager networkManager)
    {
        _networkManager = networkManager;
    }

    public void Send(string opcode, string payload)
    {
        if (opcode != null && opcode.Length > 8)
        {
            opcode = opcode.Substring(0, 8);
        }

        string frame = _networkManager.Encode(opcode ?? string.Empty, payload ?? string.Empty);
        _networkManager.Enqueue(frame);
    }
}
