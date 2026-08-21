public static class FrameUtil
{
    public static int CountZeroes(FrameBuffer buffer)
    {
        int count = 0;

        for (int i = 0; i < buffer.Count; i++)
        {
            if (buffer.Get(i) == 0)
            {
                count++;
            }
        }

        return count;
    }
}
