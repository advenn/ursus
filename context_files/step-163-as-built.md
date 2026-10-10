# Step 163 — as built

**Item 1 of `v0.6-scope.md`: the Parquet reader's double copies.**

## 1. Evidence first

**Every column the reader produced was written twice.**

- **A fixed-width column.** The values were decoded into a Go slice the reader kept
  from batch to batch. `data.NewFixed` then copied them into a fresh Arrow buffer.
  Step 137 kept the slice, which stopped it being allocated twice, but the copy
  stayed.
- **A String column.** The offsets and characters were built in Go slices made for
  each batch and dropped after it. `data.NewStringParts` copied both.

Nothing covered a String column's allocation. The reader's allocation test, with a
nullable String column added, read on step 162's reader:

| | allocated | produced | ratio |
| --- | --: | --: | --: |
| 1 thread | 22.9 MB | 13.8 MB | 1.67 |
| 4 threads | 21.6 MB | 13.8 MB | 1.57 |

## 2. What changed

**`data.NewFixedOwned` and `data.NewStringOwned`** are `NewFixed` and
`NewStringParts`, taking their slices rather than copying them:

- they keep the type check and the offsets' limit (`MaxStringBytes`);
- a buffer wraps the slice's whole allocation, so it reports its capacity, as
  `Buffers` measures every buffer by what it holds (`ownedBuffer`, in `unsafe.go`
  with the package's other unsafe code);
- the memory is Go heap, kept alive by the buffer, and 8-byte rather than 64-byte
  aligned, which `arrowx.Alignment` allows and every kernel treats as a hint.

**The reader hands its slices over:**

- a fixed-width column's next batch is decoded into a new slice, sized for that
  batch, by `growExact`;
- a String column's slices were already new each batch.

**Not changed:**

- the list elements' builder, which reuses its slices from batch to batch and relies
  on the copy;
- the CSV reader's builders, which do the same;
- the kernels that build a String column through `NewStringParts`. They could take
  `NewStringOwned`, each once its slices are shown to be its own.

## 3. Measured

The same test after:

| | allocated | produced | ratio |
| --- | --: | --: | --: |
| 1 thread | 16.6 MB | 13.8 MB | 1.21 (was 1.67) |
| 4 threads | 15.7 MB | 13.8 MB | 1.14 (was 1.57) |

Step 162's runner against this one, alternated, three rounds of five iterations at
SF=1. CPU time per iteration is from rusage, warm-up included:

| query | CPU per iteration | median wall |
| --- | --- | --- |
| q19 | 1,288 → 1,202 ms (−7%) | 324 → 292 ms (−10%) |
| q1 | 2,244 → 2,220 ms (−1%) | 450 → 434 ms |
| q7 | 2,625 → 2,624 ms | 467 → 459 ms |

## 4. Tests

- **`TestReadingAllocatesAboutWhatItProduces`** reads a nullable String column beside
  the three Int64 ones:
  - its bound, 1.3×, fails step 162's reader, at 1.67× and 1.57×;
  - the first batch's strings, nulls included, read as written after every later
    batch was read.
- **`TestOwnedColumnsReadTheirSlices`:**
  - an owned column reads the caller's memory, with no copy;
  - its buffers report the slices' allocations, by capacity;
  - an empty column is empty.
- **`TestAnOwnedColumnChecksItsType`.**
- **`TestStringColumnsRefusePastTheirOffsets`** gains `NewStringOwned`, at its limit
  and past it.

## 5. Teeth

| tooth | result |
| --- | --- |
| a fixed-width column's slice reused next batch | **bites:** `TestReadingAllocatesAboutWhatItProduces`, its first batch overwritten |
| a String column's slices reused next batch | **bites:** the same |
| strings copied again | **bites:** the same, by its bound |
| an owned buffer measured by its length | **bites:** `TestOwnedColumnsReadTheirSlices` |
| an owned String column past its offsets | **bites:** `TestStringColumnsRefusePastTheirOffsets` |
| an owned column of the wrong type | **bites:** `TestAnOwnedColumnChecksItsType` |

**The fourth tooth was a bug before it was a tooth.** The first `ownedBuffer`
wrapped only the slice's length, so a column reported 24 bytes where it held 80. The
new test failed on it, and it was fixed before the gate.

## 6. What the gate found

**The first gate's race run failed the allocation test**, at 2.15× under `-race`.
Step 162's Int64-only test failed there too, at 2.0×, so the String column was not
the cause:

- a fixed-width column's slice was grown by `slices.Grow`, which is
  `append(s, make([]T, n)...)`;
- once the slice was new every batch, that ran every batch, and under the race
  detector the `make` is not elided, so every batch's values were allocated twice;
- `growExact` makes one allocation of exactly the batch in any build. The Int64
  columns read 1.01× under race after it.

**What remained, 1.49× under race against 1.21× without it,** is arrow-go's own
string decoding. Its allocator gave 56.8 MB under race and 42.0 MB without, in
`byteArrayCol.read`'s calls, and arrow-go has no race-specific code. So the bound is
asserted outside the race detector (`raceDetector`, set by build tag), and the ratio
is logged and every first batch checked in both builds. The gate ran again after.

**Gate,** run again after §6: test-all 115 ok, race 23 ok, levels, vet ×3 and the
bench engine tests clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
