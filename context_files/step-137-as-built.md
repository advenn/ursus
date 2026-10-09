# Step 137 — as built

**Where `j5` allocates.** After steps 133–136, `j5` over Parquet still allocated
17.4 GB under 3 GB, and had barely moved. This step is the allocation profile the
maintainer asked for, taken as step 132's was. No code changed.

## 1. How

**The runner was run directly** with `-allocprofile`:

- at `b153bd8`;
- under a 3 GB scope with swap off, on 8 threads;
- one warm-up and one timed iteration;
- over Parquet and over CSV.

The profile counts both iterations, sampled.

| run | time | allocated in all | sampled in the profile | heap in use | counted | collections |
| --- | --: | --: | --: | --: | --: | --: |
| Parquet | 4.1 s | 28.5 GB | 14.7 GB | 2.79 GB | 1.04 GB | 44 |
| CSV | 8.3 s | 23.9 GB | 9.8 GB | 2.89 GB | 1.21 GB | 38 |

The shares below are of the profile's total. Cumulative figures overlap: each counts
everything below it.

## 2. What it says

| where | Parquet | CSV |
| --- | --: | --: |
| **the result:** the join's output, `gatherOut` | 1.66 GB, 11% | 2.13 GB, 22% |
| **concatenation,** `ConcatOwned`: the result in `Collect`, and the build side at the join's freeze | 1.81 GB, 12% | 2.21 GB, 22% |
| **the join's build,** `admit` | 2.47 GB, 17% | 2.54 GB, 26% |
| — of it, the key table, `keyChunk.insert` | 1.65 GB, 11% | 1.72 GB, 17% |
| the join's freeze: the build side's concatenation, the CSR arrays | 1.20 GB, 8% | 1.43 GB, 15% |
| **the reader:** Parquet's String columns, `byteArrayCol.read` | 3.11 GB, 21% | |
| — Parquet's Float64 and Int32 columns, `readDirect` | 2.36 GB, 16% | |
| — the CSV reader, `reader.Next` | | 2.62 GB, 27% |

**In the key table, 1.65 GB:**

- 1.03 GB is `hashes` and `refs` doubling, `pushDoubling`;
- 0.51 GB is the slot ring doubling;
- 0.10 GB is the arena.

`j5` builds on a table of ten million keys, so the arrays double about twenty times on
the way. Step 133 made each doubling allocate twice the final size, not five times;
the doublings themselves remain.

**The rest of the build,** about 0.8 GB, is two per-row arrays grown by plain
`append`, quarter steps and all:

- `rowKey`: `s.rowKey = append(s.rowKey, make([]int32, n)...)`. The compiler
  allocates no temporary for the `make`, but the growth is `append`'s;
- `counts`: one entry per new key.

## 3. A second copy of step 136's mistake

**`fixedCol.finish`** (`internal/source/parquet/column.go`) drops its values slice
every batch. Its comment says `NewFixed` wraps without copying, as the CSV builder's
did. `NewFixed` copies, so each batch's values are allocated twice:

- 0.44 GB for Int32;
- 0.30 GB for Float64;
- 5% of the Parquet profile. `gb10` has it too.

**One more stale comment,** left by step 136: `stringBuilder.finish` in the CSV
reader still says the fixed-width builder must drop its slice.

## 4. What would move it, not built

In order of how sure and how simple:

1. **The Parquet fixed-width column keeps its slice,** as step 136 did for CSV, and
   the stale comment goes. About 0.74 GB of 14.7 for `j5` over Parquet.
2. **The join's `rowKey` and `counts` grow by doubling.** About 0.8 GB, 5% over
   Parquet and 8% over CSV, at most.
3. **The key table's growth, 1.65 to 1.72 GB.** Only sizing it in advance removes
   the doublings, and that needs the number of distinct keys. ursus knows only row
   counts, from Parquet footers.

   A table sized by row count, for a build with few keys, would be mostly empty
   slots that the budget counts, spilling a join that fits. Not obvious; not
   proposed.
4. **The result,** the output and its concatenation, a third or more. That is
   `Collect` returning one contiguous frame. A frame of several batches, as Polars
   keeps, would remove the concatenation, but it changes what a `DataFrame` is.

## 5. Files

The profiles `j5-parquet.allocs` and `j5-csv.allocs`, and the runner's JSON, were not
committed. The command is step 132's, with `--query j5`.
