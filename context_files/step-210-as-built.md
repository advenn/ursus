# Step 210 — as built

**A spilled window's merge holds a batch across its files, not the input.** Found in
step 209.

## 1. Why

A window that spills routes its rows to sixteen partition files, computes each
file in a sink of its own, writes each answer to a result file, and merges the
results on the input ordinal. The merge holds a written batch of every file at
once.

A sink that answers in memory answers its whole file in one batch. So the merge held
every file's answer, the whole input, whatever the limit. Over 20,000 rows across
1,000 keys it peaked at 971 KB under a 121 KB limit. It did so before step 209, and
without any skew.

## 2. What changed

`runPass` writes a sink's answer to its result file a sixteenth of a batch at a
time, as the partition files are already written. The merge then holds a batch
across the sixteen.

**A batch apiece was not enough.** The first version wrote whole batches: sixteen of
them, of 500 rows of about 92 bytes, are 736 KB, and the peak only fell from 971 to
738 KB.

## 3. Tests and teeth

- **`TestASpilledWindowHoldsABatchAFile`:** 40,000 rows across 1,000 keys, in eighty
  frames of 500 rows so that a batch is charged for itself, under a tenth of their
  bytes. A sum and a cumulative sum over the key:
  - the answer is the in-memory one, row for row;
  - the peak is at most the limit, a quarter more, and the sixteen writers' 8 KiB
    buffers. Measured, it is 276 KB under 210 KB, of a 2.1 MB input.
- **Step 209's tests** still pass. Its skewed fixture peaked at 519 KB under its
  121 KB limit, and now at 144 KB.

| tooth | result |
| --- | --- |
| a file's answer written whole | **bites:** the peak |
| a batch apiece, not a sixteenth | **bites:** the peak |
| a slice past the answer's end | **bites** |

**What is left.** The writers' buffers, 128 KiB, are the floor of any keyed window
that spills; a limit below it is overshot by them.

## 4. The race run's ten minutes

The first gate's race run timed out in the root package after ten minutes, with no
race reported. Step 207's 200,000-row half of
`TestUnkeyedTemporalGroupsHandOutTheirWindows` was running. That half measures the
budget, not concurrency, and under the race detector, about ten times slower on a
machine short of memory, it pushed the package past the limit.

- **`race_test.go` and `norace_test.go`** set `raceDetector` by build tag, as the
  Parquet package already does.
- **The large half moves to `measureUnkeyed`,** which is skipped under the race
  detector. The small cases, at batches of 1, 7 and 64, run under it as before.

**Gate,** rerun: test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine
tests clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
