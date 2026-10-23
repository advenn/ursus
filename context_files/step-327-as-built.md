# Step 327 — as built

**A frame's conveniences,** Polars' DataFrame and LazyFrame methods that ursus
answered only through expressions or not at all (`polars-parity.md` §4).

## 1. The API

| Method | Polars | What it answers |
| --- | --- | --- |
| `Corr(ctx)` | `corr` | the Pearson correlation of every pair of columns, a Float64 column a column, as NumPy's `corrcoef`, through which Polars computes it |
| `Gather(ctx, indices, nullOnOOB)` | `gather` | the rows at positions, in their order; a negative one from the end; one past an end refused, or a row of nulls |
| `Row(i)`, `RowNamed(i)` | `row(i)`, `row(i, named=True)` | a row's values as Go values, as `ToMaps` reads them, a null nil |
| `RowWhere(ctx, pred)` | `row(by_predicate=)` | the one row a predicate holds of; none or several refused, "predicate … returned 3 rows" |
| `Item()`, `ItemAt(row, column)` | `item()`, `item(row, column)` | a one-value frame's value, or any one |
| `GetColumnIndex(name)` | `get_column_index` | a column's place |
| `ReplaceColumn(i, c)` | `replace_column` | the frame with `c` at a place |
| `DropInPlace(name)` | `drop_in_place` | the column, and the frame without it |
| `ToSeries(i)`, `ToStruct(name)` | `to_series`, `to_struct` | a column by place; the frame as one Struct column |
| `SumHorizontal(ctx, ignoreNulls)`, `MeanHorizontal(ctx, ignoreNulls)`, `MinHorizontal(ctx)`, `MaxHorizontal(ctx)` | the frame's `*_horizontal` | each row across every column, named `sum`, `mean`, `min`, `max` |
| `Fold(ctx, fn)` | `fold` | `Reduce(fn, All())`, named after the first column |
| `lf.Columns(ctx)`, `lf.DTypes(ctx)`, `lf.Width(ctx)` | `columns`, `dtypes`, `width` | read from `CollectSchema` |

**Where ursus differs:**

- **`ReplaceColumn` and `DropInPlace` answer a new frame,** where Polars changes its
  own, as `Extend` does; a replacement named as another column is refused, where
  Polars' `replace_column` makes a frame with the name twice.
- **`Corr` agrees with NumPy to within rounding,** not to the bit: NumPy's BLAS sums
  in an order of its own. A Decimal column is read, which Polars' `to_numpy` cannot
  pass to NumPy.
- **The horizontal reductions are ursus's expressions':** an integer sum an Int128, a
  row of nulls null, text refused where Polars' `sum_horizontal` concatenates it.
- **`ItemAt` names its column,** where Polars' `item` takes a place too:
  `GetColumnIndex` and `ToSeries` give one from the other.

## 2. How

`frameconv.go`:

- **`Corr`** casts each column to Float64 by the cast kernel, a column with a null
  skipped. Then `covariances` sums the means and the centred products in spans of
  65536 rows, in parallel. Within a span it works in blocks of 1024 rows, whose
  centred values sit beside each other in the cache, and each pair's products are
  summed in four lanes. The spans' sums are added in order, so the answer does not
  depend on the worker count. Then come NumPy's `1/(n-1)`, its two divisions in its
  order, and its clip.
- **`Gather`** takes a short selection a column at a time, the columns in parallel.
  A long one is taken a column at a time, that column's chunks of 16384 rows in
  parallel, each written into its own rows of one answer. This uses
  `kernel.TakeChunked` and, for text, `data.TakeStringsChunked`, which counts each
  chunk's bytes first. `Take`'s validity is now one helper, `takeValidity`, shared
  by both.
- **The horizontal reductions** with `ignoreNulls` false AND each column's validity
  into the reduction's.
- **The rest** are thin: the row readers `ToMaps` uses, the frame's schema, and the
  expressions.

## 3. Tests

**`TestFrameConveniences`,** every answer Polars':

- `Corr`: NumPy's answers to the bit for two frames whose sums are exact, the two
  sides of the diagonal differing; a null, a constant column, no rows and one row,
  each NaN; text refused, a Decimal read; a self-correlation the arithmetic puts at 1.0000000000000002,
  clipped to 1; 140000 rows with a Bool, three spans, against `Corr` of two
  expressions;
- `Gather` with a negative index, out of bounds refused and as nulls, and 150000
  rows reversed, ten chunks a column, compared by value;
- `Row`, `RowNamed`, `RowWhere` of one, several and none; `Item`, `ItemAt`;
- `GetColumnIndex`, `ReplaceColumn` at 0 and −1 and of the wrong length,
  `DropInPlace`, `ToSeries`, `ToStruct`;
- the four horizontal reductions, a null skipped and not, and `Fold`;
- a lazy frame's columns, types and width.

**`TestTakeChunkedIsTake`,** in `internal/kernel`: `TakeChunked` equals `Take` to the
bit.

## 4. Performance

Against Polars 1.44.1, micro benchmarks, the median of 7, on this 8-core laptop:

| operation | first draft | as built | Polars |
| --- | --- | --- | --- |
| `Corr`, 1M rows × 8 Float64 | 29.6 ms | 7.4–8.1 ms | 88.0 ms |
| `Corr`, 1M × 2 | 4.4 ms | 1.2 ms | 8.2 ms |
| `Gather` of 1M random rows, 1M × 8 Float64 | 42.7 ms | 25–27 ms | 28.5 ms |
| `Gather`, 1M × (Int64, Float64, String) | 39.6 ms | 14.6–15.3 ms | 14.1 ms |
| `SumHorizontal(ctx, false)`, 1M × 8 | 23.5 ms | 10.6 ms | 9.2 ms |
| `SumHorizontal(ctx, true)`, 1M × 8 | 10.7 ms | 10.4 ms | 9.2 ms |
| 1000 rows by `Row` | 0.28 ms | 0.26 ms | 0.31 ms |

**What changed from the first draft:**

- **`Corr`:** first through a query, `Select(All().Cast(Float64))`, which split the
  frame into batches and joined them again, a third of the time. Pairs summed in
  parallel read each column once a pair; the spans read it twice in all.
- **`Gather`:** first a column a goroutine, so a frame of three columns used three
  cores, and its text column was all of the time. Then chunks, every chunk of every
  column at once, joined after (36.9 and 18.7 ms). The answer is a column at a time,
  its chunks at once, written in place. A float column of 1M rows is 8 MiB, the
  whole L3 of this machine, and eight at once missed it.
- **`SumHorizontal(ctx, false)`:** first `When(AnyHorizontal(All().IsNull()))`, now
  the validities ANDed.

**Where Polars is still ahead:**

- **Text in a gather,** by a tenth: Polars' strings are views, a gather copying 16
  bytes a row, where ursus copies each string's bytes.
- **The horizontal sum,** by a tenth: the expression's own speed, its kernel an
  aggregate a row, which this step does not change.

## 5. Docs

- **Selecting:** the column conveniences after `InsertColumn`, and the rows' after
  the slices.
- **Aggregation:** `Corr` and the horizontal reductions under "The whole frame".
- **Lazy and eager:** `Columns`, `DTypes` and `Width`.
- **Coming from Polars:** four rows.
- **The CHANGELOG, `feature-gaps.md`, `polars-parity.md`.**

## 6. Teeth, all 21 biting

`Corr`:

- a null breaking nothing;
- no clip;
- the divisions swapped, which NumPy's exact answers catch;
- divided by n, which the exact answers catch too, though a correlation does not
  depend on the scale;
- the last span's products dropped;
- the last span's sums dropped from the means;
- a row a block skipped.

`Gather`:

- a negative index from the start;
- `nullOnOOB` refusing;
- a long gather's chunks all written at the start;
- a long text gather's chunks all from offset 0;
- a chunk's characters not counted;
- a gather's nulls dropped;
- a long gather's last chunk left out.

The rest:

- several rows, the first, for `RowWhere`;
- an item of any frame;
- a replacement of any length;
- the dropped column kept;
- a negative column place from the start;
- a negative row from the start;
- nulls always ignored.

The first round's teeth on the pairs in parallel and on a column a goroutine went
with that code. A test of `TakeChunked` against `Take` was added for the kernel:
seven types and a null index, chunks of 1, 7, 256, 2500 and 4096 run backwards, and
a failed run.

**Gate:** test-all 130 ok, race 26 ok (the root package 646 s here, with the next
steps' tests running beside it), levels, vet ×3 and the bench engine tests clean; the
22 PDS-H answers match DuckDB.
