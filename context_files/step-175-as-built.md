# Step 175 — as built

**F1 of `v0.6-scope.md`: the aggregates of two inputs,** `Corr`, `Cov`, `MinBy` and
`MaxBy`.

## 1. Evidence first: what Polars does

Asked of Polars 1.44.1 in the bench's environment, offline.

**`pl.corr` and `pl.cov`:**

| question | Polars' answer |
| --- | --- |
| a pair with a null in either | dropped: `[1, ∅, 3]` against `[1, 5, 2]` answers as `[1, 3]` against `[1, 2]` |
| `corr`, where it is undefined: no pairs, one, or a variable that does not vary | NaN, never null |
| `cov` with no pairs | null |
| `cov` of one pair, ddof 1, no nulls in the column | 0.0 |
| `cov` of one pair, ddof 1, left after nulls were dropped | null |
| `cov` of one pair, ddof 0 | 0.0 |
| a NaN in either | NaN, both |
| integers | Float64 |
| a Float32 pair | Float32 |
| `method="spearman"` | rank correlation |

**`min_by` and `max_by`:**

| question | Polars' answer |
| --- | --- |
| a tie | the earliest row, for both |
| a null `by` | skipped; if every `by` is null, null |
| a null value at the chosen row | null |
| a NaN `by` | skipped, unless every `by` is NaN, when the first row wins |
| `by` a string, an integer or a Date | ordered as that type |
| the answer's type | the value's |
| a literal `by` | refused: the lengths differ |

## 2. What changed

**The plan** (`internal/expr/agg.go`):

- Four ops are appended to the enum: `AggCorr`, `AggCov`, `AggMinBy` and `AggMaxBy`.
- As the scope planned, an `Agg` keeps its one child: `PairOf(a, b)` puts the two
  inputs in step 146's struct.
  - Each input is aliased to its position, `"0"` and `"1"`, so the two never collide.
    `Col("v").MinBy(Col("v"))` works.
  - The aggregate is still named after a's column: naming reads an Alias only at the
    root.
- `Agg.String` renders a paired aggregate as written, `col("x").corr(col("y"))`.
  The string is what deduplication keys on, so it keeps the second input and the ddof.
- `pairedBinding` types the ops:
  - `Corr` and `Cov` of two numbers are Float64, as `Var` is;
  - `MinBy` and `MaxBy` are the value's type, and `by` must be ordered.
- `MinBy` and `MaxBy` are order-dependent (`IsBy`), as `ArgMin` is. A tie keeps the
  earliest row, which a worker dealt rows round-robin cannot know.

**The kernel** (`internal/kernel/aggpair.go`):

- **`coMomentAcc`** keeps, per group, the pairs where both values are non-null,
  their means, and their co-moment. For `Corr` it keeps both second moments too.
  - The update is Welford's and the merge is Chan's, so `Corr` and `Cov` run on every
    worker.
  - **Each group's values are shifted by its first pair.** Welford's alone gave
    0.99862543953 for a correlation of values near 1e9 that vary by a few units,
    where Polars gives 0.99862542890352. Shifted, the values are their spread. A
    merge moves the other group's means to this group's shift.
- **`byAcc`** keeps each group's chosen value as `positionAcc` keeps `First`'s and
  `Last`'s: a part a batch, a row index a group, and superseded rows compacted away.
  - `positionAcc` gains `place` and `adopt`, the two halves `byAcc` shares.
  - Beside each group's row is its key, in `byKeys[T]`:
    - an order key for a Boolean, integer, float or instant, which is `orderKey`'s
      encoding;
    - the text for a string, cloned when kept, since a batch's strings alias its
      bytes;
    - the value for an Int128 or a Decimal.
  - A batch finds each group's best row first, then compares that row with the
    group's kept key.

**The public API** (`agg.go`): `Corr(a, b)`, `Cov(a, b, ddof)`, `e.MinBy(by)` and
`e.MaxBy(by)`.

**Where ursus differs from Polars, each written in the doc that answers it:**

- **`MaxBy` over a NaN** answers the NaN's row. ursus orders NaN greatest, as its
  `Max` and `ArgMax` do, so `v.MaxBy(by)` is always v at `by.ArgMax()`. Polars skips
  the NaN.
- **`Cov` of one pair, ddof 1, is null** both times Polars answers differently: 0.0
  when there were no nulls, and null when nulls left one pair. `Var` answers null
  for both.
- **A Float32 pair's `Corr` is Float64,** as ursus's `Var` of a Float32 is.
- **Spearman's rank correlation** is not built.
- **A literal `by`** is spread over the rows, as `Struct`'s literal operand is, where
  Polars refuses its length.

## 3. Tests

- **`TestCorrCovAnswerAsPolars`:**
  - three groups, with a null dropped pairwise, a one-pair group and a one-row group;
  - two rows;
  - a NaN;
  - every x null;
  - an x that does not vary;
  - near-equal large values;
  - integers.
- **`TestMinByMaxByAnswerAsPolars`:**
  - ties;
  - nulls in `by` and in the value;
  - NaN;
  - `by` a string, an integer, a Date, a Boolean or a Decimal;
  - per group, with `by` an expression;
  - `.Over(g)`.
- **`TestPairedAggregatesAgreeWithABruteForce`:** 40,000 rows in 1,500 groups, in
  batches of 1,000, against answers computed row by row:
  - `by` takes twenty values, so ties are common;
  - it runs on one worker, on four, and under a 256 KiB budget, which it asserts
    spilled.
- **`TestPairedAggregatesNameRenderAndExpand`:**
  - the names;
  - two `Cov`s of one first input kept apart;
  - `MaxBy` of a column by itself;
  - the plan's rendering;
  - `Col("a", "b").MinBy(Col("k"))` expanding to two aggregates.
- **`TestPairedAggregatesRefuse`:** a string's `Corr` and `Cov`, a negative ddof,
  and `MinBy` by a List.
- **The sweeps** classify the four ops:
  - the merge sweep gains two struct families, two numbers and a string by an integer
    with ties;
  - its row renderer learns structs, since `First`, `Last` and `Implode` reach them
    too.

## 4. Found on the way

- **`Var` loses eight digits over large near-equal values:** 2.333333283662796 for
  1e9+1, 1e9+2 and 1e9+4, where Polars gives 2.3333333333333357. It has the same
  cause as the co-moments, and gets the same fix next, keeping its tested overflow
  handling.
- **`ArgMin` and `ArgMax` over 200k groups take 7.8 s,** where `MinBy` takes 111 ms
  and `First` 42 ms. They still keep a one-row column per group, the shape step 168
  removed from `First` and `Last`, and `byKeys` fits them.
- **`NUnique` of a struct is typed at plan time and refused when it runs:** its kernel
  hashes the values as keys. The merge sweep skips that one pair, saying why.

## 5. Teeth

| tooth | result |
| --- | --- |
| a tie taking the later row in a batch | **bites:** ties, Boolean, only NaNs, and the brute force on every path |
| a merge's tie taking the later accumulator's row | **bites:** the merge sweep's string-by-integer family |
| a null `by` not skipped | **bites:** the three null cases |
| the values not shifted | **bites:** near-equal large values, and the merge sweep |
| a merge ignoring the two shifts | **bites:** the merge sweep's corr and cov |
| a covariance of ddof pairs answered | **bites:** the per-group table |
| a paired aggregate rendered without its second input | **bites:** the per-group table and the rendering test, two answers collapsed |
| `MinBy` and `MaxBy` not order-dependent | **bites:** the brute force on four workers |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
