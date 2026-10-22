# Step 319 — as built

**V3, a `Series` with operations:** Polars' Series operations on one column
(`v0.6.7-scope.md` §6).

## 1. The API

`Series` is ursus's own type now, the view it was an alias of embedded:

```go
type Series[T any] struct{ *data.Series[T] }
```

Every method the view had is promoted, the generic `Map[U]` and `Fold[A]` as well; a
Go 1.27 experiment checked that generic methods promote through embedding. Code that
used a `*Series` compiles as it did. New:

| Kind | Methods |
| --- | --- |
| Same type | `Sort`, `SortDesc`, `Unique`, `Reverse`, `DropNulls`, `FillNull(strategy)` |
| No engine, the column shared | `Head`, `Tail`, `Slice(offset, length)` with Polars' window, `Rename` |
| Another type | `Cast[U](ctx, dt)` |
| Aggregates | `Min`, `Max` (T, ok); `Sum` (T: an integer sum past T is an error, where Polars' wraps); `Mean`, `Median`, `Std(ddof)`, `Var(ddof)` (float64, ok); `Count`, `NUnique` (int) |

`Std` and `Var` take their degrees of freedom, as `Expr`'s do; Polars' default is 1.

## 2. How

Each operation is the engine's: an expression over the series' column as a one-column
frame, `run`, its answer read back as a `T` (`same`), or one value (`one`). So a
`Series` sorts, uniques and aggregates exactly as a frame does, nulls and NaN alike.

`TypedColumn` and `DataFrame.Column` build the new type (`seriesOf`).

## 3. Speed

A million distinct Int64 values, medians of seven:

| | `Series` | a frame's | Polars' Series |
| --- | --- | --- | --- |
| sort | 93 ms | 154 ms | 12 ms |
| unique | 406 ms | 457 ms | 35 ms |
| mean | 4.5 ms | 4.8 ms | 0.47 ms |

**A `Series` costs what the engine's own operation costs.** A one-row query takes
74 µs, so planning is not the time.

**The gap to Polars is the engine's:**

- the sort runs on one thread;
- the unique encodes each key, where the join's integer keys have skipped encoding
  since step 159;
- the mean of integers is slower than a sum should be.

`feature-gaps.md` §8 records the three, for the performance release.

## 4. Tests

**`TestSeriesOperations`,** against Polars' answers on `[3, null, 1, 3, 2]`:

- each operation, the slices from the end and from before the start;
- the aggregates, a variance and a standard deviation to 1e-15;
- `NUnique` 3 where Polars' `n_unique` is 4: ursus's does not count the null, §9;
- `Cast` to float64, and to string refused;
- the aggregates of no values false;
- text sorted, nulls first, and its `Sum` refused.

## 5. Docs

- **Go values:** a paragraph on the operations, under "A column out: Series".
- **Coming from Polars:** a row.
- **The CHANGELOG:** the feature.
- **`feature-gaps.md`:** the V3 row closed, and the engine's three gaps in §8.
- **`polars-parity.md`:** V3 struck.

## 6. Also: the race detector's time

The root package took 716 s under the race detector here. One test was 116 s of it:
`TestUnkeyedTemporalGroupsHandOutTheirWindows`, whose small half walks 3,000 rows at
batches of 1, 7 and 64.

Under the race detector it walks 1,000 rows, which still end segments everywhere. That
is 36 s, about 80 s saved. CI's race job ran the root package in 760 s of its
20-minute limit at step 317, and in 425 s at step 318, so its runners vary.

## 7. Also: a memory limit at its edge

**The first gate's race pass failed** in `TestV066UnderAMemoryLimit`, the float
moments by 2,000 keys under 64 KiB: the query held 64.2 KiB, 1.5 KiB of it the
group-by's. It is the flake `feature-gaps.md` recorded at step 309, and nothing this
step changed is on its path.

Under the race detector here it refused 2 runs in 100 at 64 KiB, and 1 in 15 at
48 KiB and 4 threads. At 80 KiB and above none of 45 refused at any thread count; at
96 KiB none of 100, each still spilling at least 16 times, the spill directory empty
after. The case runs under 96 KiB now, and passes three runs under each of
test-all's five configurations and twenty under the race detector. The gap is
closed.

## 8. Teeth, all 10 biting

- `Sort` descending;
- `Unique` reversed;
- a negative offset taken from the start;
- a start before the first not cutting the length;
- `Sum` in the aggregate's type;
- `FillNull`'s strategy ignored;
- `Count` of the distinct;
- `Tail` from the start;
- `Max` as the minimum;
- `Var` of no degrees of freedom.

**Gate:** test-all 125 ok, levels, vet ×3 and the bench engine tests clean; the 22
PDS-H answers match DuckDB. The race pass failed once, on the flake of §7; after the
margin, 25 ok, the root package in 566 s here.
