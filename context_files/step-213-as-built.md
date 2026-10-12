# Step 213 — as built

**The 0.6.3 scope, and the traces it rests on.** The engine is unchanged; the bench
runner gains three flags.

## 1. Why

0.6.2 cut PDS-H's CPU by 8.6% and its wall time by only 2.5%. A CPU profile samples
running goroutines only, so it cannot show work that waits. Reading the Parquet
reader suggested three places where it might:

- the ordered row-group window;
- arrow-go's ZSTD decoder, shared by the whole process;
- the copies of each column chunk.

## 2. What changed

**The bench runner** (`bench/engines/go/engine/engine.go`) takes `-trace`,
`-blockprofile` and `-mutexprofile`. Each covers the timed iterations only, as
`-cpuprofile` does:

- the block and mutex rates are set after the warm-up and reset before the profile is
  written;
- the trace starts and stops around the timed loop.

**`context_files/v0.6.3-scope.md`** is written: speed, out-of-core and features, the
three themes the maintainer chose.

## 3. Measured

0.6.2's code, two timed iterations at eight threads. Cores busy is CPU samples over
the profile's duration. Delays are from the block profile, divided by the wall time
of the two runs.

| query | wall per run | cores busy | decode blocked at its send | `Next` waiting on the head | ZSTD waits |
| --- | --: | --: | --: | --: | --: |
| h2o gb4 | 270, 228 ms | 4.1 | 2.44 s, 4.9× wall | 0.31 s, 62% | 15 ms |
| PDS-H q1, SF=1 | 295, 282 ms | 5.6 | 3.09 s, 5.3× wall | 0.37 s, 64% | 32 ms |
| PDS-H q6, SF=1 | 92, 92 ms | 4.3 | 0.74 s, 4.0× wall | 0.11 s, 59% | 30 ms |

**What it says:**

- **The ordered window is the bound.** On average four to five row-group workers sit
  parked at their send (`parallel.go:188`), and the consumer waits on the head row
  group for about 60% of the run. The parallel operator's workers downstream are
  starved: most of the blocked time is theirs, waiting for batches.
- **The ZSTD cap does not show yet,** since only about two decodes run at once. The
  design pass expected exactly this, which is why the window comes first.

**The claims stay as planned:** gb4 at least 35% less wall time, PDS-H SF=1 at least 7%.

**Gate:** the bench engine's tests and vet are clean. The engine itself is unchanged.
