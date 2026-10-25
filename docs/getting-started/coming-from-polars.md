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
| `.group_by("k").agg(pl.col("x"), pl.col("x").cum_sum())`, a list a group | `.GroupBy(ursus.Col("k")).Agg(ursus.Col("x"), ursus.Col("x").CumSum(false))` (new in 0.6.7) |
| `.group_by("k", maintain_order=True)` | `.GroupBy(ursus.Col("k")).MaintainOrder()` |
| `.join(other, on="k", how="left")` | `.Join(other, ursus.JoinOn(ursus.Col("k")), ursus.JoinHow(ursus.JoinLeft))` |
| `import polars.selectors as cs`; `cs.numeric() - cs.by_name("id")`; `~cs.string()` | `import "github.com/advenn/ursus/selector"`; `selector.Numeric().Difference(selector.ByName("id")).Expr()`; `selector.String().Complement()` (new in 0.6.7) |
| `.join_asof(...)`, `.join_where(...)` | `.JoinAsOf(...)`, `.JoinWhere(...)` |
| `.join_asof(other, on="k", tolerance=5, coalesce=False)` | `.JoinAsOf(other, ursus.AsOfOn(ursus.Col("k")), ursus.AsOfTolerance(5), ursus.AsOfCoalesce(false))` (new in 0.6.7) |
| `.join_where(other, *preds, suffix="_r")` | `.JoinWhereSuffix(other, "_r", preds...)` (new in 0.6.7) |
| `df.update(other, on="k", how="full")` | `lf.Update(other, ursus.UpdateOptions{On: []string{"k"}, How: ursus.UpdateFull})` (new in 0.6.7) |
| `df.rows_by_key("k", named=True)`, `df.to_dicts()`, `df.iter_rows(named=True)` | `df.RowsByKey[K, T]("k")`, `df.ToMaps()`, `lf.IterRows[T](ctx)` (new in 0.6.7) |
| `s.sort()`, `s.unique()`, `s.mean()`, `s.cast(pl.Float64)` | `s.Sort(ctx)`, `s.Unique(ctx)`, `s.Mean(ctx)`, `s.Cast[float64](ctx, ursus.Float64)` (new in 0.6.7) |
| `pl.DataFrame(list_of_dataclasses)` | `ursus.FromRows(structs)` (new in 0.6.7) |
| `.join(other, on="k", maintain_order="right")` | `.Join(other, ursus.JoinOn(ursus.Col("k")), ursus.JoinMaintainOrder(ursus.JoinOrderRight))` (changed in 0.6.7) |
| `.over("k")` | `.Over(ursus.Col("k"))` |
| `.unique(subset)`, `.drop_nulls()` | `.Unique(names...)`, `.DropNulls(names...)` |
| `.fill_null(0)`, `.fill_null(strategy="forward")` | `.FillNullWith(0)`, `.FillNull(strategy)` |
| `.fill_null(strategy="forward", limit=1)` | `.FillNull(ursus.FillForward.Limit(1))` (new in 0.6.7) |
| `df.drop_nans()`, `df.interpolate()` | `lf.DropNans()`, `lf.Interpolate()` (new in 0.6.7) |
| `pl.col("x").interpolate_by("t")` | `ursus.Col("x").InterpolateBy(ursus.Col("t"))` (new in 0.6.7) |
| `pl.col("p").top_k_by("sales", 3)`, `.bottom_k_by(...)` | `ursus.Col("p").TopKBy(ursus.Col("sales"), 3)`, `.BottomKBy(...)`, a List (new in 0.6.7) |
| `.head(n)`, `.tail(n)`, `.slice(o, n)` | `.Head(n)`, `.Tail(n)`, `.Slice(o, n)` |
| `.drop("a")`, `.rename({"a": "b"})` | `.Drop("a")`, `.Rename(map[string]string{"a": "b"})` |
| `.with_row_index()` | `.WithRowIndex("index", 0)` |
| `df.insert_column(1, s)`, `df.hash_rows()` | `lf.InsertColumn(1, s)`, `df.HashRows(ctx, 0)` (new in 0.6.7) |
| `.explode("a")`, `.unnest("s")` | `.Explode("a")`, `.Unnest("s")` |
| `.unpivot(...)`, `df.pivot(...)` | `.Unpivot(ursus.UnpivotOptions{...})`, `df.Pivot(ctx, ursus.PivotOptions{...})` |
| `lf.pivot(on, on_columns=[...])` | `lf.Pivot(ursus.PivotOptions{On: ..., OnColumns: ...})` (new in 0.6.7) |
| `pl.concat(frames, how="vertical_relaxed")`, `"diagonal_relaxed"`, `"horizontal_extend"`, `"align"` | `ursus.Concat(frames, ursus.WithConcatMode(ursus.ConcatRelaxed))`, `ConcatDiagonalRelaxed`, `ConcatHorizontalExtend`, `ConcatAlign` (new in 0.6.7); `ursus.ConcatFrames` for `DataFrame`s |
| `pl.col("x").first(ignore_nulls=True)`, `.last(...)`; `df.group_by("k").all()` | `ursus.Col("x").FirstNonNull()`, `LastNonNull()`; `lf.GroupBy(ursus.Col("k")).All()` (new in 0.6.7) |
| `for key, g in df.group_by("k")`, `.map_groups(fn)` | `for p, err := range lf.GroupBy(ursus.Col("k")).Groups(ctx)`, `.MapGroups(ctx, fn)` (new in 0.6.7) |
| `df.extend(other)`, `df.glimpse()`, `df.estimated_size()` | `df.Extend(ctx, other)`, `df.Glimpse()`, `df.EstimatedSize()` (new in 0.6.7) |
| `df.gather([2, 0])`, `df.row(1)`, `df.row(by_predicate=p)`, `df.item()`, `df.item(1, "b")` | `df.Gather(ctx, []int{2, 0}, false)`, `df.Row(1)`, `df.RowWhere(ctx, p)`, `df.Item()`, `df.ItemAt(1, "b")` (new in 0.6.7) |
| `df.replace_column(0, s)`, `df.drop_in_place("b")`, `df.get_column_index("b")`, `df.to_series(0)`, `df.to_struct()` | `df.ReplaceColumn(0, s)`, `df.DropInPlace("b")`, `df.GetColumnIndex("b")`, `df.ToSeries(0)`, `df.ToStruct("")` (new in 0.6.7) |
| `df.corr()`, `df.sum_horizontal()`, `df.fold(lambda a, b: a + b)` | `df.Corr(ctx)`, `df.SumHorizontal(ctx, true)`, `df.Fold(ctx, func(a, b ursus.Expr) ursus.Expr { return a.Add(b) })` (new in 0.6.7) |
| `lf.collect_schema().names()`, `.dtypes()`, `.len()` | `lf.Columns(ctx)`, `lf.DTypes(ctx)`, `lf.Width(ctx)` (new in 0.6.7) |
| `df.group_by("k").having(pl.len() > 1).agg(...)` | `lf.GroupBy(ursus.Col("k")).Having(ursus.Len().Gt(1)).Agg(...)` (new in 0.6.7) |
| `df.group_by("k", maintain_order=True).head(2)`, `.tail(2)` | `lf.GroupBy(ursus.Col("k")).Head(2)`, `.Tail(2)` (new in 0.6.7) |
| `df.upsample("t", every="1d", group_by="g")` | `lf.Upsample("t", ursus.Every("1d"), "g")` (new in 0.6.7) |
| `df.to_dummies(["c"], drop_first=True)`, `df.unstack(step=3)` | `df.ToDummies(ctx, ursus.DummiesOptions{Columns: []string{"c"}, DropFirst: true})`, `df.Unstack(ctx, ursus.UnstackOptions{Step: 3})` (new in 0.6.7) |
| `df.transpose(column_names="id")` | `df.Transpose(ctx, ursus.TransposeOptions{ColumnNamesFrom: "id"})` (new in 0.6.7) |
| `df.partition_by("k", include_key=False)` | `df.PartitionBy(ctx, ursus.PartitionOptions{By: []string{"k"}, DropKey: true})` (new in 0.6.7) |
| `pl.concat([a, b])` | `ursus.Concat([]*ursus.LazyFrame{a, b})` |
| `.group_by_dynamic(...)`, `.rolling(...)` | `.GroupByDynamic(index, ursus.DynamicOptions{...})`, `.Rolling(index, ursus.RollingOptions{...})` |
| `.rolling("t", period="7d", offset="0d")` | `.Rolling(ursus.Col("t"), ursus.RollingOptions{Period: ursus.Every("7d"), Offset: ursus.NoOffset()})`, the next seven days (changed in 0.6.7) |
| `.rolling_rank_by("t", "3d")`, `.rolling_map(f, 5)` | `.RollingRankBy(ursus.Col("t"), ursus.Every("3d"), ursus.RankAverage)`, `.RollingMap[float64, float64]("f", ursus.Float64, 5, f)` (new in 0.6.7) |
| `pl.col("x").sum().rolling("t", period="2d")`, `....over("g")` | `ursus.Col("x").Sum().Rolling(ursus.Col("t"), ursus.RollingOptions{Period: ursus.Every("2d")})`, `....Over(ursus.Col("g"))` (new in 0.6.7) |
| `.group_by_dynamic("step", every="5i")`, `.rolling_sum_by("step", "3i")` | `ursus.DynamicOptions{Every: ursus.Every("5i")}`, `.RollingSumBy(ursus.Col("step"), ursus.Every("3i"))`, over an integer index (new in 0.6.7) |
| `.str.contains(p)`, `.dt.year()`, `.list.len()` | `.Str().Contains(p, false)`, `.Dt().Year()`, `.List().Len()` |
| `.struct.field("f")` | `.Struct().Field("f")` |
| `pl.Array(pl.Float64, 3)`, `.list.to_array(3)`, `.arr.to_list()` | `ursus.Array(ursus.Float64, 3)`, `.List().ToArray(3)`, `.Arr().ToList()`; a Go `[3]float64` in rows (new in 0.6.7) |
| `.arr.sum()`, `.arr.sort()`, `.arr.eval(pl.element() * 2)`, `.arr.to_struct()`, `.arr.dot(b)` | `.Arr().Sum()`, `.Arr().Sort()`, `.Arr().Eval(ursus.Element().Mul(2), false)`, `.Arr().ToStruct()`, `.Arr().Dot(b)` (new in 0.6.7) |
| `.reshape((-1, 3))`, `pl.concat_arr(a, b)` | `.Reshape(-1, 3)`, `ursus.ConcatArr(a, b)` (new in 0.6.7) |
| `pl.arctan2(y, x)`, `.truediv(v)`, `.limit(n)`, `.pipe(f)` | `ursus.Arctan2(y, x)`, `.TrueDiv(v)`, `.Limit(n)`, `.Pipe(f)` (new in 0.6.7) |
| `.item()`, `.lower_bound()`, `.upper_bound()`, `.is_sorted()` | `.Item(false)`, `.LowerBound()`, `.UpperBound()`, `.IsSorted(false, false)` (new in 0.6.7) |
| `.str.to_time("%H:%M")`, `.str.to_decimal(scale=2)`, `.dt.cast_time_unit("ms")` | `.Str().ToTime("%H:%M", true)`, `.Str().ToDecimal(2)`, `.Dt().CastTimeUnit(ursus.Milli)` (new in 0.6.7) |
| `.meta.root_names()`, `.meta.output_name()`, `.meta.undo_aliases()`, `.meta.tree_format()` | `.Meta().RootNames()`, `.Meta().OutputName()`, `.Meta().UndoAliases()`, `.Meta().TreeFormat()` (new in 0.6.7) |
| `.to_physical()`, `.reinterpret(signed=True)` | `.ToPhysical()`, `.Reinterpret(true)` (new in 0.6.7) |
| `.name.keep()`, `.name.to_uppercase()`, `.name.replace(p, v)`, `.name.prefix_fields("p_")`; `.struct.drop(["f"])` | `.Name().Keep()`, `.Name().ToUpper()`, `.Name().Replace(p, v, false)`, `.Name().PrefixFields("p_")`; `.Struct().Drop("f")` (new in 0.6.7) |
| `.map_elements(f)`, `.map_batches(f)` | `.MapElements(name, type, f)`, `.MapBatches(name, type, f)`, with a Go function |
| `df.map_rows(f)`, `df.map_columns(["a"], f)` | `df.MapRows(ctx, f)`, `df.MapColumns(ctx, []string{"a"}, f)` (new in 0.6.7) |
| `df.show(20)`, `lf.cache()` | `df.Show(20)`, `lf.Cache()` (new in 0.6.7) |
| `df.write_parquet(f)`, `df.write_csv(f)` | `lf.WriteParquet(ctx, w)`, `lf.WriteCSV(ctx, w)`, to any `io.Writer` |
| `pl.read_ndjson(p)`, `pl.scan_ndjson([p, q], infer_schema_length=n, ignore_errors=True)` | `ursus.ScanNDJSON(p)`, `ursus.ScanNDJSONFiles([]string{p, q}, ursus.WithInferSchemaLines(n), ursus.WithNDJSONIgnoreErrors(true))` (new in 0.6.7) |
| `pl.scan_parquet("ds/", hive_schema={"g": pl.Int8}, include_file_paths="path")` | `ursus.ScanParquet("ds", ursus.WithHiveSchema(s), ursus.WithIncludeFilePaths("path"))`, a hive dataset's keys read from its directories (new in 0.6.7) |
| `pl.scan_parquet(files, missing_columns="insert", extra_columns="ignore", cast_options=pl.ScanCastOptions(integer_cast="upcast"))` | `ursus.ScanParquetFiles(files, ursus.WithInsertMissingColumns(true), ursus.WithIgnoreExtraColumns(true), ursus.WithUpcastIntegers(true))` (new in 0.6.7) |
| `pl.read_csv(p, try_parse_dates=True)` | `ursus.ScanCSV(p, ursus.WithTryParseDates(true))` (new in 0.6.7) |
| `pl.read_csv(p, ignore_errors=True, infer_schema=False, raise_if_empty=False, decimal_comma=True, encoding="utf8-lossy")` | `ursus.ScanCSV(p, ursus.WithIgnoreErrors(true), ursus.WithInferSchema(false), ursus.WithRaiseIfEmpty(false), ursus.WithDecimalComma(true), ursus.WithEncoding(ursus.UTF8Lossy))` (new in 0.6.7) |
| `pl.read_csv("sales.csv.gz")`, a zlib or Zstandard file too | `ursus.ScanCSV("sales.csv.gz")`, known by its first bytes (new in 0.6.7) |
| `pl.read_json(p, schema_overrides={"a": pl.Float32})` | `ursus.ScanJSON(p, ursus.WithNDJSONSchemaOverrides(map[string]dtype.DataType{"a": ursus.Float32}))` (new in 0.6.7) |
| `df.write_csv(f, quote_style="always", datetime_format="%Y-%m-%d %H:%M", float_precision=2, compression="gzip")` | `lf.WriteCSV(ctx, w, ursus.WithQuoteStyle(ursus.QuoteAlways), ursus.WithDatetimeFormat("%Y-%m-%d %H:%M"), ursus.WithFloatPrecision(2), ursus.WithCSVCompression(ursus.CSVGzip))` (new in 0.6.7) |
| `df.write_ndjson(f)`, `df.write_json(f)`, `lf.sink_ndjson(p)` | `lf.WriteNDJSON(ctx, w)`, `lf.WriteJSON(ctx, w)`, `lf.SinkNDJSON(ctx, p)` (new in 0.6.7) |
| `pl.scan_ipc(p)`, `pl.read_ipc_stream(p)`, `df.write_ipc(f, compression="zstd")`, `lf.sink_ipc(p)`, `pl.read_ipc_schema(p)` | `ursus.ScanIPC(p)`, `ursus.ScanIPCStream(p)`, `lf.WriteIPC(ctx, w, ursus.WithIPCCompression(ursus.IPCZstd))`, `lf.SinkIPC(ctx, p)`, `ursus.ReadIPCSchema(p)` (new in 0.6.7) |
| `pl.read_database("SELECT * FROM t WHERE a > ?", conn, execute_options={"parameters": [5]}, schema_overrides={"a": pl.Int32})` | `ursus.ReadDatabase(ctx, db, "SELECT * FROM t WHERE a > ?", 5)`, any `database/sql` driver; `ursus.ReadDatabaseRows(rows, ursus.WithDatabaseSchemaOverrides(m))` (new in 0.6.7) |
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
- **`Cut` gives an Enum of its labels,** and `QCut` and `Hist`'s category a Categorical,
  as Polars' do (new in 0.6.7).
- **A `Categorical` is stored as its text** (new in 0.6.7): it costs what a String does,
  and its codes, `Cat().Physical()`, are computed over the frame, the order its values
  first appear in, where Polars' are a process's string cache.
- **A `Time` takes a unit,** where Polars' is nanoseconds.

The full list, with the step that made each choice, is in
[`feature-gaps.md` §9](https://github.com/advenn/ursus/blob/master/context_files/feature-gaps.md#9-where-ursus-differs-from-polars-on-purpose),
and what Polars has that ursus does not yet is in the rest of that file.
