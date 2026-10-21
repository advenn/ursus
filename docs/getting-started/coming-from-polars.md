---
description: >-
  ursus for Polars users: the Polars API mapped to Go, method by method, and the
  places where the Go dataframe library deliberately answers differently.
---

# Coming from Polars

ursus is modelled on Polars: the same expressions, the same contexts, mostly the same
names, and its answers are tested against Polars 1.44's on the same inputs. What
changes is what Go asks for.

=== "Polars (Python)"

    ```python
    import polars as pl

    df = (
        pl.scan_csv("sales.csv")
        .filter(pl.col("qty") > 3)
        .group_by("region")
        .agg(
            (pl.col("qty") * pl.col("price")).sum().alias("revenue"),
            pl.len().alias("orders"),
        )
        .sort("revenue", descending=True)
        .collect()
    )
    ```

=== "ursus (Go)"

    ```go
    df, err := ursus.ScanCSV("sales.csv").
        Filter(ursus.Col("qty").Gt(3)).
        GroupBy(ursus.Col("region")).
        Agg(
            ursus.Col("qty").Mul(ursus.Col("price")).Sum().Alias("revenue"),
            ursus.Len().Alias("orders"),
        ).
        Sort(ursus.Desc(ursus.Col("revenue"))).
        Collect(ctx)
    ```

## What Go changes

- **Methods, not operators.** Go has no operator overloading, so `pl.col("a") > 3` is
  `Col("a").Gt(3)`, and `a * b` is `a.Mul(b)`.
- **Lazy is the real API.** A `LazyFrame` is a query and `Collect` runs it. A
  `DataFrame`'s methods are a lazy query collected at once, so each takes a
  `context.Context`: `df.Filter(ctx, pred)`.
- **Errors come at the end.** A query being built never fails: an unknown column or a
  type mismatch is kept and returned by `Collect`, with a hint, so a chain stays a
  chain.
- **Values come back typed.** `df.Column[int64]("qty")` is a `Series[int64]`, and
  `df.Rows[Sale]()` a `[]Sale`, through Go 1.27's generic methods.
- **Options are functions.** `Join(other, JoinOn(Col("k")), JoinHow(JoinLeft))` where
  Polars takes keyword arguments.

## The API, side by side

| Polars | ursus |
|---|---|
| `pl.DataFrame({"a": [1, 2]})` | `ursus.Frame(ursus.Values("a", []int64{1, 2}))` |
| `pl.scan_csv(path)`, `pl.read_csv(path)` | `ursus.ScanCSV(path)`, then `Collect(ctx)` |
| `pl.scan_parquet(path)` | `ursus.ScanParquet(path)` |
| `lf.collect()` | `lf.Collect(ctx)` |
| `lf.collect(engine="streaming")` | `lf.CollectBatches(ctx)`, an iterator of frames |
| `lf.explain()` | `lf.Explain(ctx)` |
| `pl.col("a")`, `pl.col("a", "b")` | `ursus.Col("a")`, `ursus.Col("a", "b")` |
| `pl.all()`, `pl.exclude("a")` | `ursus.All()`, `ursus.Exclude("a")` |
| `pl.lit(1)` | `ursus.Lit(1)`; a Go value is a literal wherever an expression goes |
| `pl.len()` | `ursus.Len()` |
| `pl.when(p).then(a).otherwise(b)` | `ursus.When(p).Then(a).Otherwise(b)` |
| `.alias("x")` | `.Alias("x")` |
| `.cast(pl.Int64)` | `.Cast(ursus.Int64)`, strict; `.CastLossy(ursus.Int64)` turns failures into nulls |
| `df.sum()`, `df.mean()`, `df.null_count()` and the other frame aggregates | `lf.Sum()`, `lf.Mean()`, `lf.NullCount()` (new in 0.6.7); `df.Sum(ctx)` eagerly |
| `df.match_to_schema(schema, ...)` | `lf.MatchToSchema(schema, ursus.MatchOptions{...})` (new in 0.6.7) |
| `df.cast({"a": pl.Int32})`, `df.cast(pl.String)` | `lf.Cast(map[string]ursus.DataType{"a": ursus.Int32})`, `lf.CastAll(ursus.String)`; `CastTypes` by type (new in 0.6.7) |
| `.filter(p)`, `.select(...)`, `.with_columns(...)` | `.Filter(p)`, `.Select(...)`, `.WithColumns(...)` |
| `.sort("a", descending=True)` | `.Sort(ursus.Desc(ursus.Col("a")))` |
| `.group_by("k").agg(...)` | `.GroupBy(ursus.Col("k")).Agg(...)` |
| `.group_by("k", maintain_order=True)` | `.GroupBy(ursus.Col("k")).MaintainOrder()` |
| `.join(other, on="k", how="left")` | `.Join(other, ursus.JoinOn(ursus.Col("k")), ursus.JoinHow(ursus.JoinLeft))` |
| `.join_asof(...)`, `.join_where(...)` | `.JoinAsOf(...)`, `.JoinWhere(...)` |
| `.over("k")` | `.Over(ursus.Col("k"))` |
| `.unique(subset)`, `.drop_nulls()` | `.Unique(names...)`, `.DropNulls(names...)` |
| `.fill_null(0)`, `.fill_null(strategy="forward")` | `.FillNullWith(0)`, `.FillNull(strategy)` |
| `.fill_null(strategy="forward", limit=1)` | `.FillNull(ursus.FillForward.Limit(1))` (new in 0.6.7) |
| `df.drop_nans()`, `df.interpolate()` | `lf.DropNans()`, `lf.Interpolate()` (new in 0.6.7) |
| `.head(n)`, `.tail(n)`, `.slice(o, n)` | `.Head(n)`, `.Tail(n)`, `.Slice(o, n)` |
| `.drop("a")`, `.rename({"a": "b"})` | `.Drop("a")`, `.Rename(map[string]string{"a": "b"})` |
| `.with_row_index()` | `.WithRowIndex("index", 0)` |
| `df.insert_column(1, s)`, `df.hash_rows()` | `lf.InsertColumn(1, s)`, `df.HashRows(ctx, 0)` (new in 0.6.7) |
| `.explode("a")`, `.unnest("s")` | `.Explode("a")`, `.Unnest("s")` |
| `.unpivot(...)`, `df.pivot(...)` | `.Unpivot(ursus.UnpivotOptions{...})`, `df.Pivot(ctx, ursus.PivotOptions{...})` |
| `pl.concat([a, b])` | `ursus.Concat([]*ursus.LazyFrame{a, b})` |
| `.group_by_dynamic(...)`, `.rolling(...)` | `.GroupByDynamic(index, ursus.DynamicOptions{...})`, `.Rolling(index, ursus.RollingOptions{...})` |
| `.str.contains(p)`, `.dt.year()`, `.list.len()` | `.Str().Contains(p, false)`, `.Dt().Year()`, `.List().Len()` |
| `.struct.field("f")` | `.Struct().Field("f")` |
| `.map_elements(f)`, `.map_batches(f)` | `.MapElements(name, type, f)`, `.MapBatches(name, type, f)`, with a Go function |
| `df.write_parquet(f)`, `df.write_csv(f)` | `lf.WriteParquet(ctx, w)`, `lf.WriteCSV(ctx, w)`, to any `io.Writer` |
| `df.to_arrow()` | `df.Record()`, zero-copy |
| `df["a"].to_list()` | `df.Column[int64]("a")`, then `.Values()` |
| `df.rows(named=True)` | `df.Rows[T]()`, into Go structs |
| `pl.Config.set_streaming_chunk_size(n)` | `ursus.WithBatchSize(n)`, an option of `Collect` |

The [API reference](../reference/api.md) documents every method.

## Where ursus answers differently

These are deliberate, and each is documented beside the method:

- **An integer sum is an `Int128`,** as DuckDB's is, so it cannot overflow. Polars'
  sum of `Int64` is an `Int64`.
- **The sum of an all-null group is null,** not 0, and its product null, not 1: a
  null can be filled with 0 afterwards, but a 0 cannot be told from a real one.
- **`Count(ctx)` of a frame is its number of rows.** Polars' frame `count()` is each
  column's non-null count; in ursus that is `Select(ursus.All().Count())`.
- **`NUnique` does not count null** as a value; Polars counts it.
- **`Round` rounds half away from zero;** Polars rounds half to even, which
  `RoundHalfToEven` selects.
- **`Max` and `Min` propagate NaN;** Polars' `max` ignores it, and its `nan_max`
  propagates it, as `NanMax` does here.
- **A literal has its Go value's type:** `Col("a").Add(3)` of an `Int32` column is an
  `Int64`, since a Go integer constant is an `int`; write `int32(3)` to keep `Int32`.
  Polars' literals take the column's type, as a fill value's does here.
- **A cast is exact or refused:** a float with a fraction cast to an integer is an
  error, or null when lossy, where Polars truncates it toward zero. `Truncate(0)` first
  for Polars' answer.
- **`Div` and `Pow` always give a float.**
- **An expression is named after its leftmost column.** `Col("a").Alias("x").Add(1)`
  is `x` in Polars and `a` here: put the alias last.
- **`Remove` drops a row whose condition is null,** as `Filter` does; Polars'
  `remove` keeps it.
- **`GroupByDynamic` keeps empty windows,** a row of `Len` 0 for a period with no
  rows; Polars leaves them out.
- **`ToInteger`, `ToDate` and `ToDatetime` give null for what does not parse;**
  Polars' fail by default.
- **`Cut` and `Hist` give text,** where Polars gives a Categorical, which ursus does not
  have yet.
- **A `Time` takes a unit,** where Polars' is nanoseconds.

The full list, with the step that made each choice, is in
[`feature-gaps.md` §9](https://github.com/advenn/ursus/blob/master/context_files/feature-gaps.md#9-where-ursus-differs-from-polars-on-purpose),
and what Polars has that ursus does not yet is in the rest of that file.
