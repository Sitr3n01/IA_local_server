from solution import find_insert_position as f
cases = [
    (([1, 3, 5, 7], 4), 2),
    (([1, 3, 5, 7], 1), 0),
    (([1, 3, 5, 7], 0), 0),
    (([1, 3, 5, 7], 9), 4),
    (([1, 1, 1, 1], 1), 0),
    (([2, 2, 4, 4, 6], 4), 2),
    (([], 5), 0),
    (([1], 1), 0),
    (([1], 2), 1),
    ((list(range(0, 200, 2)), 101), 51),
]
bad = []
for args, want in cases:
    got = f(*args)
    if got != want:
        bad.append((args, want, got))
if bad:
    raise SystemExit("FAIL " + repr(bad))
print("OK")
