# Step 276 — as built

**M1 of `v0.6.6-scope.md`, first half: the moment, bitwise and small aggregates.**
These are Polars' `skew`, `kurtosis`, `bitwise_and`, `bitwise_or`, `bitwise_xor`,
`nan_min`, `nan_max`, `has_nulls`, `is_empty`, `approx_n_unique` and `dot`. The
second half, step 277, is `entropy`, `mode` and Spearman's correlation.

## 1. What changed

**Six new aggregates** (`expr.AggOp`, appended):

- **`AggSkew`, `AggKurtosis`:** with `AggParams.Bias` and `AggParams.Fisher`, which
  `Agg.String` renders, so two estimators never share one aggregate.
- **`AggBitAnd`, `AggBitOr`, `AggBitXor`:** the bitwise reductions.
- **`AggNanMin`:** the NaN-first minimum.
- **Typing:**
  - skew and kurtosis are Float64 of any number, as Polars';
  - the bitwise reductions keep an integer to 64 bits, or a Boolean; Int128, a float
    and anything else are refused, as Polars refuses a float;
  - `nan_min` is typed as `min`.

**`momentAcc`** (`internal/kernel/aggmoment.go`), skew and kurtosis:

- **The state:** the second, third and fourth central moments, kept online by
  Terriberry's extension of Welford's update and merged by Pébay's pairwise formulas.
  So a group-by runs N-ways as var's does.
- **Shifted** by each group's first value, as `varAcc` is.
  - Over 2,000 values near 1e9, unshifted, the running mean rounds and the skew is
    wrong in its ninth digit; shifted, it is the exact answer, computed in
    rationals, to about 1e-15.
  - Three such values were not enough to show it: the running mean is exact after
    the first. The first tooth was silent until the 2,000-value test was added.
- **Polars' estimators,** scipy's:
  - the biased skew is m3/m2^1.5, and unbiased it is times √(n(n−1))/(n−2), null
    below 3 values;
  - the kurtosis is m4/m2², corrected when unbiased, null below 4, less 3 with
    `fisher`;
  - over values all alike, or one value, either is NaN; a group of no value is null.
- **Not to the bit:** Polars computes the moments in two passes, the mean first, so
  the answers agree to the last few bits. The tests compare to a relative 1e-12.

**`bitAcc`:** each group's non-null values folded in their bits, read as uint64,
signed values sign-extended. The answer is narrowed to the input's type, so an
Int8's AND of 1, 2 and 3 is 0. A Boolean folds its one bit. A group of no value is
null.

**`nan_min`:** the extremum accumulator with an order that puts NaN first, so a NaN
wins. ursus's `Max` already puts NaN above every number, so `NanMax` is `Max`. `Min`
skips a NaN as Polars' `min` does, and `Max` does not, as `feature-gaps.md` §9
already records.

**Composed of aggregates there were** (`agg.go`):

- **`HasNulls`:** `NullCount() > 0`.
- **`IsEmpty`:** `Len() == 0`, so a column of nulls is not empty, as Polars answers.
- **`ApproxNUnique`:** `NUnique()` plus one where there is a null.
  - Polars counts a null as a value; ursus's `NUnique` does not.
  - It is exact, an estimate's best case (§6.3).
  - A UInt64, as every count of ursus's is; Polars' is a UInt32.
- **`Dot(other)`:** `Mul(other).Sum()`, over the rows where both are non-null. It is
  `Sum`'s, so an integer dot product is an Int128, and one of no row is null where
  Polars' is 0. §9 records both of `Sum`.

**Every aggregate sweep took the new ones** and was updated:

- the Decimal types (`skew` and `kurtosis` Float64 and scaled, the bitwise ones
  refused, `nan_min` a Decimal);
- the behaviour classes of a one-row group (the bitwise ones and `nan_min` the value
  itself, the moments approximate);
- the merge equivalence over Float64: both skews, two kurtoses and `nan_min`, in 2,
  3 and 5 parts each way;
- and a new merge test of the bitwise reductions over integers, negatives among them.

**Not there yet:** an aggregate of these over a reshaped column in an aggregation,
`Col("x").Sort().Skew(true)` in `Agg`. It needs a list kernel and is refused as the
other aggregates without one are.

## 2. Tests and teeth

Every answer is Polars 1.44.1's.

- **`TestSkewAndKurtosis`:**
  - skew both ways and kurtosis three ways, over the frame and by group, to 1e-12;
  - the nulls of the unbiased estimators, and the NaN of one value;
  - skew of integers;
  - three and 2,000 values near 1e9;
  - one thread and four alike;
  - all null; NaN; text refused.
- **`TestBitwiseAndSmallAggregates`:**
  - the three bitwise reductions, `has_nulls`, `is_empty`, `nan_min`,
    `approx_n_unique` and `dot`, over the frame and by group;
  - Booleans, the input's integer type, an Int8;
  - all null, no rows, and NaN against `min` and `nan_max`;
  - an integer dot product;
  - a float's and an Int128's bits refused.
- **`internal/kernel`:** `TestMergeEquivalence` takes the moments and `nan_min`;
  `TestMergeEquivalenceOfBits` is new.

| tooth | result |
| --- | --- |
| Pebay's m3 cross term dropped (merge) | **bites** |
| Pebay's m4 cross term dropped (merge) | **bites** |
| m4's update off by one | **bites** |
| the unbiased skew's factor | **bites** |
| the unbiased skew of 2 values | **bites** |
| Fisher's is Pearson's | **bites** |
| no shift | **bites**, once the 2,000-value test was added |
| xor is or | **bites** |
| nan_min skips NaN | **bites** |
| approx_n_unique skips a null | **bites** |
| is_empty counts values | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
