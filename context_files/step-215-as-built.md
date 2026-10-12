# Step 215 — as built

**A narrow integer's sum and mean add in int64.** Found by profiling gb4 once step
214 had let the reader run ahead.

## 1. Why

After step 214, gb4 is bound by the CPU on this laptop's four physical cores, so its
wall time now comes from removing work. Its profile on step 214's runner, a mean of
three columns by a key of 100 groups:

- **`meanAcc.AddBatch`** was 25% of the CPU, **`widenToInt128`** 15%. A mean of an
  integer widened every value to an Int128 first: a slice of sixteen bytes a row,
  then a 128-bit add a row.
- **A sum** widened each value to an int64 slice, then added it in 128 bits.

The sums are exact by design, so Int128 is the type of the result. It need not be
the type of every addition.

## 2. What changed

`internal/kernel/aggnarrow.go`:

- **`narrowSums`** holds a group's partial sum in an int64, beside the exact Int128
  sum it folds into.
- **A value of 32 bits or fewer** is under 2³² in magnitude, so 2³⁰ of them sum to
  under 2⁶² in an int64. A narrow input adds into the partials, read straight from
  the column, a type at a time (`addNarrow`).
- **The partials fold** into the exact sums before 2³⁰ rows have been added since the
  last fold, a chunk at a time within a longer batch, and at Finish.
- **Merge** adds the other's exact sum and its partial together, and writes neither:
  a partitioned fold merges one worker into several partitions at once.
- **Which inputs:** Int8 to Int32 and Uint8 to Uint32 (`isNarrowInt`). Not a Date,
  an int32 underneath: its mean is in microseconds, which a day of overflows. Wider
  integers keep the 128-bit path.

`sumAcc` and `meanAcc` keep a `narrow` flag and their partials, and count them in
`NBytes`.

## 3. Tests and teeth

- **`TestANarrowIntegersSumsAreExact`** (kernel), with the fold lowered to every 37
  rows: each narrow type, its extremes, with nulls, 50 groups, in batches of up to
  60 rows.
  - Every sum is the exact one, and every mean matches it.
  - Never more than 37 rows are left unfolded.
  - A merge leaves the other's partials as they were.
- **`TestANarrowIntegersSumAndMeanAnswerAsInt64s`** (root): each narrow type's sum
  and mean, grouped on one thread and on eight and as a window, answer as the same
  column cast to Int64 does.

**The fold within a batch was found by the first test.** A batch longer than the
fold threshold left that many rows unfolded, since the fold ran only between
batches. At 2³⁰ rows that batch is very unlikely, but nothing forbids it. `add` now
works in chunks of at most the threshold.

| tooth | result |
| --- | --- |
| never narrow | **bites:** the kernel test |
| a sum's partials not folded at Finish | **bites:** both tests |
| a mean's partials not folded at Finish | **bites:** both tests |
| a sum's merge ignores the other's partial | **bites:** both, on eight threads and in the kernel |
| a mean's merge ignores the other's partial | **bites:** both |
| no fold within a batch | **bites:** the kernel test |
| a fold leaves the partials | **bites:** the kernel test |
| a Date taken for narrow | **bites:** the temporal mean against Polars, and the merge inventory |
| a sum's group never marked seen | **bites:** both |
| a mean's rows never counted | **bites:** both, and the nearest-double mean |

## 4. Measured

Step 214's runner against this step's, h2o 10M over Parquet, alternated three
rounds, three iterations:

| query | CPU | wall |
| --- | --: | --: |
| gb4, a mean of v1, v2 and v3 by id4 | 1,504 → 1,286 ms, **−15%** | 247 → 211 ms, **−15%** |
| gb5, a sum of v1, v2 and v3 by id6 | 4,372 → 4,074 ms, −7% | 688 → 635 ms, −8% |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
