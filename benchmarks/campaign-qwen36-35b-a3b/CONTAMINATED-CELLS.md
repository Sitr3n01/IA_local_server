# Discarded measurements

Cells recorded here were produced while something else held GPU memory, so the
figures describe the contention rather than the configuration. They are listed
rather than deleted quietly: a re-run overwrites the artifact, and without this
note the two numbers for the same cell would be indistinguishable in the git
history.

## 2026-08-22 — `iq4xs-ctx32768-kvq4-ncpumoe16`, whole cell

**Discarded:** pp32768 204.11 ± 85.17 t/s, and tg128 40.01 ± 5.70 t/s.
**Re-measured:** see `throughput/throughput-iq4xs-ctx32768-kvq4-ncpumoe16-remeasure.json`.

The pp32768 figure was identified first and the rest of the cell was initially
assumed clean. It was not. `tg128` for the same cell started at 12:20:00 —
seconds after the intruding server was killed — and returned a **14.3% relative
standard deviation** against 1.2% to 5.1% for every other decode cell in the
campaign:

| Cell | tg128 | relative spread |
|---|---|---|
| Q2_K_XL `n0` | 103.21 ± 1.19 | 1.2% |
| Q2_K_XL `n16` | 44.41 ± 1.64 | 3.7% |
| Q3_K_S `n16` | 42.54 ± 2.09 | 4.9% |
| IQ4_XS `n20` | 34.37 ± 1.75 | 5.1% |
| **IQ4_XS `n16`** | **40.01 ± 5.70** | **14.3%** |

The whole cell is therefore re-run rather than the one test that was obviously
wrong. Spread is the signal that caught it, which is the same instrument
`docs/BENCHMARKS.md` §8.1 uses to identify a configuration oscillating across a
paging boundary.

A `Test-V2ProfileContracts.ps1` run was started at 12:12 against a Qwen3.8
profile while Phase 2 was in flight. That script loads a real `llama-server`,
which is documented in its own synopsis; it was launched in the belief that it
was a static contract check. The Phase 2 `pp32768` cell for this configuration
ran from roughly 12:11 to 12:19:55 and overlapped it.

The adapter samples taken alongside the benchmark are what identify it, not the
timing coincidence:

| Cell | dedicated MiB (min..max) | shared MiB (min..max) | result |
|---|---|---|---|
| pp512 (clean) | 2833 .. 14094 | 331 .. 425 | 355.85 ± 24.96 |
| pp8192 (clean) | 2845 .. 14163 | 331 .. 429 | 396.04 ± 1.39 |
| **pp32768 (contaminated)** | 2837 .. **16048** | 331 .. **13363** | 204.11 ± **85.17** |

13363 MiB of shared memory against 429 MiB in the neighbouring cell is a second
process on the adapter, and the 42% relative standard deviation is the paging
oscillation that follows from it. `docs/BENCHMARKS.md` treats spread of that kind
as evidence in its own right.

**Rule this enforces for the rest of the campaign:** nothing that loads a model
runs while a phase is in flight, including the repository's own test scripts.
`Test-V2ProfileContracts.ps1`, `Test-V2WorkstationSmoke.ps1` and
`Test-V2AgenticHarness.ps1` all start a server and are scheduled after the
measurement phases, never beside them.

## 2026-08-22 — `q3ks-ctx32768-kvq4-ncpumoe8`, all four tests: re-measured as a precaution

**Not known to be contaminated. Re-measured because it could not be ruled out.**

A dry run of `run-overnight.ps1` at about 12:57 ended in an unconditional
teardown — `Get-Process llama-server, llama-bench | Stop-Process -Force` — which
killed whatever was on the adapter rather than only what that script had started.
Phase 2 was mid-flight on this cell.

The four results that landed look intact: pp512 672.59 ± 8.34, pp8192 713.38 ±
6.17, pp32768 636.73 ± 11.39, tg128 58.08 ± 2.19. The curve is coherent, the
spreads are the tightest in the campaign, and `llama-bench` writes its JSON only
after completing every repetition — a killed run produces nothing parseable, not
a plausible number. On that evidence the kill most likely landed between cells.

"Most likely" is not a measurement. The cell was re-run rather than argued about,
and both sets of figures are kept so the comparison itself is evidence: if they
agree, the reasoning above is confirmed; if they do not, the original was
corrupt and would have been reported as fact.

**The bug is fixed rather than worked around.** `run-overnight.ps1` now records
the llama process ids that exist before it starts and excludes them from its
teardown, so the teardown means "clean up after me". It also warns when it finds
a pre-existing process, because a measurement taken beside one is not valid
regardless of who cleans up afterwards.
