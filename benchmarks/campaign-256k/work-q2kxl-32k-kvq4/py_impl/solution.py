def merge_intervals(intervals):
    if not intervals:
        return []

    sorted_intervals = sorted((start, end) for start, end in intervals)
    merged = []

    for start, end in sorted_intervals:
        if not merged:
            merged.append([start, end])
        else:
            last_start, last_end = merged[-1]
            if start <= last_end:
                if end > last_end:
                    merged[-1][1] = end
            else:
                merged.append([start, end])

    return merged
