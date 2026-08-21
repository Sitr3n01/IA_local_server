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
