# Step 295 — as built

**The user guide's first four chapters** (`docs-site-scope.md` §2):

- selecting and filtering;
- aggregation and group-by;
- window functions;
- joins.

They come with a landing page, and the home page's sentence on fit is reworded at the
maintainer's question.

## 1. The chapters

**`guide/index.md`:** a card for each chapter, and the data the chapters read, as the
code that builds it.

**`guide/selecting.md`:**

- columns by name, by pattern (`ColRegex`) and by type (`ColDType`); `Exclude`,
  `Drop` and `Rename`;
- `Filter`'s predicates, `IsIn`, `IsBetween` and `Or`, and an aggregate in a
  predicate;
- nulls in a filter, and `Remove`;
- `Sort` and `NullsLast`, then `TopK`;
- `Unique` and `UniqueKeep`;
- the slices, negative ones included, and `WithRowIndex`.

**`guide/aggregation.md`:**

- the statistics, and the counting forms;
- keys: computed ones, several of them, and their order with `MaintainOrder`;
- conditional aggregates: `Filter` before an aggregate, a `Bool`'s sum, `MaxBy`;
- lists per group;
- the `GroupBy` shortcuts;
- the whole frame, `ValueCounts` and `Describe`;
- what a memory limit does.

**`guide/windows.md`:**

- `Over`, a share of a partition and a grand total;
- `OverWith` and its order, the cumulative functions;
- `Shift`, `Diff` and `PctChange`;
- `Rank`'s five methods on ties, with a table;
- rolling windows by rows and by time, across a weekend, where the two differ;
- the ewms adjusted and not;
- a window without `Over`.

**`guide/joins.md`:**

- the seven kinds in tabs;
- row order;
- merged keys and `JoinCoalesce`, keys of different names, suffixes, null keys;
- validation;
- an as-of join with a tolerance;
- `JoinWhere` over date intervals;
- `WhereExists`.

**What is new since the latest release is marked** "new in 0.6.7": `UniqueKeep` and
the negative slice. `dev` is the only version that has these pages until v0.6.7 is
tagged.

**The examples:**

- the site's examples are 72 now, 47 of them added;
- `testdata/prices.csv` is two tickers over six trading days, a weekend before the
  sixth;
- every example passed at `-cpu` 1 and 4, under `GODEBUG=simd=128`, and with the
  experiment off.

## 2. Held to Polars

**Run in Polars 1.44.1 on the same CSVs, and equal:**

- the rolling means, a centred max, a rolling sum by time;
- the adjusted and unadjusted ewms;
- `pct_change`;
- the five ranks;
- the shares over a partition;
- `describe`.

**What differs:**

- **`TopK`.** The first draft wrote `TopK(3, Desc(qty))` and got the three smallest
  orders. `TopK` flips each key, so `Asc` is Polars' default and `Desc` its
  `reverse`. The page says so.
- **Three differences, now in `feature-gaps.md` §9:**
  - `Remove` drops a row whose predicate is null, which Polars' `remove` keeps. The
    page says how to keep it.
  - A right join keeps the left frame's order, with the key in the left frame's
    place, where Polars follows the right frame. The page says how to get Polars'
    rows.
  - A join on keys of different names keeps both keys, which `JoinCoalesce`'s doc
    chose and §9 had not listed.

## 3. The generator

**Each function in `docs/examples` that is not an Example** is `func.<name>.md`, its
whole declaration. The joins page shows `customers`, `orders` and `show` as they are,
and the windows page `prices`, where a copy by hand could drift from them.

**A code block is 84 columns wide at the site's width.** 31 example lines were wider,
so they scrolled sideways. They are broken across lines now, some with a local
`qty, region := ...` or `on := JoinOn(...)`, and each line is within 84 columns once
the generator removes a tab.

## 4. The home page's fit

The maintainer asked why "data that fits comfortably in memory" was the good fit, when
ursus spills. That sentence was about speed and read as capacity.

It now says, in the README and on the home page:

- the good fit is data where the speed difference is small in absolute terms: half a
  second against a tenth of one up to tens of millions of rows;
- larger data runs too, spilling past the memory limit, but the same ratio is
  seconds or minutes there, and a spilled query pays for its disk too.

## 5. Tests and teeth

**`TestHelpersAreTheirDeclarations`:** a helper's fragment is its declaration, and an
Example is never written as one.

**Teeth, all biting:**

- a helper's fragment emptied;
- helpers named otherwise, caught by the include check too;
- `TopK` without its flip, caught by `Example_topK`;
- `Remove` keeping null rows as Polars does, caught by `Example_filterNulls`.

The last two are the library's behaviour, held by the guide's examples.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
