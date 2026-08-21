def find_insert_position(sorted_values, target):
    """Return the index where target should be inserted to keep the list sorted.

    If target is already present, return the index of its FIRST occurrence.
    """
    low = 0
    high = len(sorted_values)
    while low < high:
        mid = (low + high) // 2
        if sorted_values[mid] < target:
            low = mid + 1
        else:
            high = mid
    return low
