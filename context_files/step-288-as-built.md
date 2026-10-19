# Step 288 — as built

**The 0.6.7 scope.** No code changed.

[`v0.6.7-scope.md`](./v0.6.7-scope.md) takes `feature-gaps.md` §6, frame operations,
as 0.6.6's §9 promised.

- **Names and defaults:** every name and default in it is Polars 1.44.1's, read from
  its `DataFrame`, `LazyFrame` and `GroupBy` signatures in the bench's environment.
- **Answers probed:** the probe (`s288.py`, in the scratchpad) read Polars' answers
  for the behaviour a design turns on: `unique`'s four keeps, a negative slice,
  `partition_by`'s keys, `upsample` with and without groups, a pivot's names over
  several `On` columns, `having`, the concat modes, an integer index's groups, and a
  frame's aggregates over text.
- **Looked for first:** each item was looked for in ursus's API before it was
  listed, so the scope lists only what is missing.

**What it holds:**

- **rows:** `Unique` with Polars' keeps, duplicates over several columns, a negative
  `Slice` offset, a frame's sample and shuffle, and the row shortcuts;
- **columns and types:** a frame's `Cast`, `MatchToSchema`, a frame's aggregates,
  a strategy's limit, `DropNans` and `Interpolate`, `InsertColumn` and `HashRows`;
- **reshaping:**
  - a pivot over several `On` columns, and a lazy one with its columns given;
  - `Unpivot` without `On`, `PartitionBy`, `Upsample`;
  - `Concat`'s relaxed, horizontal and align modes;
- **group-by:** `Head`, `Tail`, `Having`, `All`, `First` and `Last` ignoring nulls,
  and an integer index for `GroupByDynamic` and `Rolling`, which also gives 0.6.6's
  rolling functions and ewms by time an integer by;
- **joins:** the remaining `maintain_order` modes, a numeric as-of tolerance, as-of
  `coalesce`, `JoinWhere`'s suffix, and `Update`;
- **Go values:** `FromRows[T]`, rows out by key and as maps, a lazy row iterator,
  and an eager `Series` with operations;
- **tier 2:** `ToDummies`, `Unstack`, `Transpose`'s names from a column, `MapGroups`
  and iteration over groups, and smaller conveniences.

**Decisions it makes:**

- **`Count(ctx)` stays the row count** (§7.1). Polars' frame `count()` is each
  column's non-null count, which is `Select(All().Count())` here.
- **Eager operations take a context,** a `Series`'s too (§7.2).
- **A frame's sample is ursus's generator's** (§7.3), as 0.6.6 decided for an
  expression's.
- **`ConcatStrict` keeps its meaning** (§7.4): Int32 and Int64 meet at Int64 there,
  where Polars' vertical refuses them, and Int64 and Float64 are refused, which
  Polars' `vertical_relaxed` meets. The probe showed both. The relaxed modes are new
  names.
- **A frame's aggregates keep every column** (§7.5), null where the aggregate cannot
  take it, as Polars' do. Polars' `sum` of a String column is null, and of a
  Boolean its trues. `GroupBy(k).Sum()`'s restriction is not changed.

**`feature-gaps.md` §6** says 0.6.7 takes it, marks the fill strategies built at step
249 with the limit still missing, and states the concat modes as they stand.

**Gate:** no code changed since step 287's gate.
