# Step 228 — as built

**F5 of `v0.6.3-scope.md`, first part: the eager wrappers the survey found
missing.**

## 1. Why

A `DataFrame` mirrors a `LazyFrame`'s operations as `Lazy().<op>().Collect(ctx)`,
one implementation for both. The scope's survey found fifteen of the lazy
operations had no eager mirror.

## 2. What changed

**`eager.go`** gains, each `Lazy().<op>(...).Collect(ctx)` with the lazy
method's arguments after the context:

- `Slice`, `Limit`;
- `DropNulls`, `FillNull`, `FillNan`, generic as the lazy two are;
- `TopK`, `BottomK`;
- `HStack`, `VStack`, `JoinAsOf`, `MergeSorted`;
- `Explode`, `Unnest`;
- `WithRowIndex`, `Remove`.

**`GroupBy`, `GroupByDynamic` and `Rolling`** give the lazy builder, which is then
collected: `df.GroupBy(...).Agg(...).Collect(ctx)`. An eager answer would need a
second builder with every aggregate again, each taking a context. The context
belongs to Collect.

**Pivot and Transpose,** the rest of F5, are the next step.

## 3. Tests and teeth

**`TestEagerWrappersAnswerAsTheirLazyMethods`:** each wrapper against its lazy
method collected, over a frame where the methods' answers differ: nulls, a NaN,
lists, structs, a sorted frame for the merge and the as-of join.

| tooth | result |
| --- | --- |
| Slice from the start | **bites** |
| TopK as BottomK | **bites** |
| FillNull's subset dropped | **bites** |
| Remove as Filter | **bites** |
| WithRowIndex from 0 | **bites** |
| JoinAsOf's options dropped | **bites** |
| Explode as Unnest | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
