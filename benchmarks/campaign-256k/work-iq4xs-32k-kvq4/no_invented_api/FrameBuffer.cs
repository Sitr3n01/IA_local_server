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
