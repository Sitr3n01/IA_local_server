from solution import merge_intervals as m
cases = [
    ([[1, 3], [2, 6], [8, 10], [15, 18]], [[1, 6], [8, 10], [15, 18]]),
    ([[1, 4], [4, 5]], [[1, 5]]),
    ([[1, 4], [5, 6]], [[1, 4], [5, 6]]),
    ([], []),
    ([[5, 6], [1, 2]], [[1, 2], [5, 6]]),
    ([[1, 10], [2, 3], [4, 5]], [[1, 10]]),
    ([[1, 1]], [[1, 1]]),
    ([[3, 4], [1, 2], [2, 3]], [[1, 4]]),
]
bad = []
for args, want in cases:
    got = m([list(x) for x in args])
    if [list(x) for x in got] != want:
        bad.append((args, want, got))
if bad:
    raise SystemExit("FAIL " + repr(bad))
print("OK")
