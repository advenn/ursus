# Step 106 — as built

**`TopK` and `BottomK`, bounded aggregates, and h2o gb8 asked the way Polars asks
it.** This is `v0.4-scope.md` item 14, "top-n within a group as an operator", and
`TopK` from §2.3's list of promised expressions.

## 1. What was wrong

gb8, the two largest v3 per id6, had no aggregate to say it with. The port used an
ordinal rank over a window partitioned by id6, filtered to rank ≤ 2. That is the SQL
formulation, and `engines/sql/h2o/gb8.sql`'s. Step 24 measured the window's sort at
92% of the query.

Polars answers it with `group_by("id6").agg(pl.col("v3").top_k(2)).explode(...)`: a
bounded accumulator per group, no sort.

## 2. The aggregate

`Expr.TopK(k)` gives the k largest values of each group, largest first, as a List.
`Expr.BottomK(k)` gives the k smallest, smallest first.

- **Bounded.** Each group holds at most k values, copied out of their batch:
  - its state is O(groups × k), and no batch is kept alive;
  - unlike `Implode`, it neither grows with the data nor forces the group-by onto
    one thread.
- **Cheap per row.** A group keeps its values sorted, best first. A value that does
  not beat the k-th is rejected with one comparison, which is what nearly every row
  of a warmed-up group meets; the rest are placed by binary search.
- **Merges.** The best k of a union is the best k of the two best ks, so the
  accumulator merges, and its answer does not depend on arrival order: equal values
  are equal.
- **Order.** ursus's total order: NaN is the largest float, as `Sort` places it.
- **Nulls** are skipped, as every reduction skips them. A group with fewer than k
  values gives a shorter list, and one with none gives null, as its `min` would.
- **Types:** numeric, temporal, Decimal and string. Boolean, nested types and
  anything unordered are refused, with a type error.
- **k.** Fewer than one is refused while the query is planned. The kernel refuses it
  again rather than index slot −1, which is what an inventory test's zero-valued
  parameters found.
- **`K` is an `AggParams` field and renders in the aggregate's name,** `top_k(2)`,
  so `TopK(2)` and `TopK(3)` never deduplicate into one computation: the step 41
  rule.

Three inventories refused the new ops until each was given its answer:

- the stated type over a Decimal (`List(Decimal(10, 2))`);
- the merge-coverage sweep's parameters (K = 3);
- the one-row-group sweep's behaviour class: a one-element list, as `Implode`'s.

## 3. gb8

```go
scan("g1").DropNulls("v3").
    GroupBy(Col("id6")).Agg(Col("v3").TopK(2).Alias("largest2_v3")).
    Explode("largest2_v3")
```

At 2M rows its answer is the old port's: 40,000 rows, equal once sorted (checked
with pyarrow).

Best of three, the old port and the new one alternately:

| | before | after |
| --- | --- | --- |
| wall time | 914–1,068 ms | 181–204 ms (**−80%**) |
| peak RSS | 623–673 MB | 178–197 MB |

## 4. Tests and teeth

**`TestTopKValues`:**

- values, nulls and short groups over integers, floats with a NaN, strings, Dates
  and Decimals;
- an all-null group's list is null, read before exploding, since explode cannot
  tell a null list from an empty one;
- k = 0 refused as a value error;
- a Boolean column refused as a type error.

**`TestTopKIsTheRankWindow`:**

- **Fixture:** 20,000 rows in 900 groups, with ties.
- **TopK(2), exploded, equals the rank-window rows;** TopK(1) equals `Max`.
- **Both run at 1 and at 8 threads,** which exercises the merge.

**The kernel's merge-coverage sweep** now runs both ops over every type family it
has, single pass against two, three and five merged partitions.

| tooth | result |
| --- | --- |
| the admission test inverted | **bites** |
| `Merge` folds nothing | **bites:** every family in the merge sweep |
| nulls offered | **bites** (a first mutation failed to build, which does not count) |
| an empty group given an empty list, not null | silent until the null-list case was written for it, then **bites** |
| `BottomK` ordering as `TopK` | **bites** |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
