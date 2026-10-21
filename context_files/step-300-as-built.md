# Step 300 — as built

**C3: a frame's aggregates** (`v0.6.7-scope.md` §2), as Polars' `DataFrame.sum`,
`mean`, `median`, `min`, `max`, `std`, `var`, `quantile`, `product` and `null_count`.
On the way, `Quantile` takes instants, durations and times.

## 1. The API

`Sum()`, `Mean()`, `Median()`, `Min()`, `Max()`, `Std(ddof)`, `Var(ddof)`,
`Quantile(q, interp)`, `Product()` and `NullCount()` on a `LazyFrame`, and on a
`DataFrame` with a context. Each answers one row, every column kept, in its place
(§7.5).

**`ApproxNUnique` was left out of the scope's list.** Polars 1.44.1 deprecates
`DataFrame.approx_n_unique` for `select(pl.all().approx_n_unique())`, which ursus
already spells `Select(All().ApproxNUnique())`.

## 2. Which column each takes: Polars'

Probed over Polars 1.44.1's answers for integers, small integers, floats, Booleans,
text, dates, datetimes, durations, times, Decimals, Binary, Enums, Lists and Structs.
A column an aggregate does not take is a null of its own type, but a product's, which
is of the Null type, as Polars answers it.

| Aggregate | Takes |
|---|---|
| `Sum` | numbers, Booleans (their trues), durations |
| `Mean`, `Median` | numbers, Booleans (as 0 and 1), instants, durations, times |
| `Min`, `Max` | every type with an order, text, Booleans, Binary and Enums among them, but a List or a Struct |
| `Std`, `Var` | numbers and Booleans; an instant is null, as Polars' |
| `Product` | numbers and Booleans |
| `Quantile` | numbers, instants, durations, times; a Boolean is null, as Polars' |
| `NullCount` | every column |

Each answer is the expression aggregate's, through a `Select` of them, so its type is
ursus's.

- **A Boolean** goes to `Mean`, `Median`, `Std` and `Var` as a Float64, and to
  `Product` as an Int64. Polars takes one, and ursus's expression aggregates refuse
  it.
- **The types differ from Polars' where ursus's aggregates already do:** an integer
  sum is an Int128, a product a Float64, a count a Uint64. `feature-gaps.md` §9 lists
  the product and the counts now; the sum's was there.

**Held to Polars on one frame of nine types,** every aggregate, every column. Every
value is Polars', but a float's var and std, an ulp apart (§3). A frame of no rows
answers one row: a sum, a mean, a min and a product null, a null count 0. Polars'
empty sum is 0 and its product 1, §9's rule for an empty sum.

`GroupBy(k).Sum()` and its kin are unchanged: they keep only the columns they take,
as their docs say.

## 3. Found on the way

**`Quantile` refused an instant, a duration and a time "for now",** though `Median`,
its 0.5, took them since step 173.

- The typing now treats the quantile as the median, keeping the type, a Date's a
  Datetime(us).
- The accumulator takes the median's output type and its day's scale.
- `TestTemporalQuantile` holds six quantile and interpolation pairs of a Date, a
  Datetime, a Duration and a Time to Polars' answers, in a `Select`.
- A group-by, a window and `List().Quantile` answered too, probed.
- The rolling quantile still refuses an instant, before it reaches the typing.
- **Tests that flipped:**
  - `TestTemporalStatisticsAreRefusedWithACast`'s quantile case, now a var's;
  - `TestTemporalVarStdAndQuantileStillRefuse`, now `TestTemporalVarAndStdStillRefuse`.

**A float `Var` or `Std` can be an ulp from Polars' on one thread.**

- `var([1.5, 2.5, 4.0])` is 1.5833333333333335 here and 1.5833333333333333 there:
  ursus's is Welford's one pass, shifted by the first value, and Polars' takes two.
- `var([1, 2, 4], ddof 0)` agrees, 1.5555555555555554 in both.
- In §9.

**Polars' frame `median` of a Decimal is null,** though its `quantile` answers. ursus's
answers, as `Expr.Median` does. In §9.

## 4. Spill and stream

A `Select` of aggregates is a keyless group-by.

`TestFrameAggregatesUnderAMemoryLimit` runs a sum, a mean, a min, a std, a null count,
a median and a quantile over 20,000 rows, in batches of 64, under 64 KiB, and holds
each to the run without a limit on one thread.

**The first run held them to the run on every core,** and the std's last bits moved.
That is §1's row: a float aggregate's last bits are the thread count's, and a
group-by under a limit runs on one.

## 5. The site

`docs/guide/aggregation.md`'s whole-frame section has the frame's aggregates, new in
0.6.7, on `dev`, with `Example_aggFrame`: a mean, whose text column is null, and a
max, whose is not. The Polars page maps the frame methods.

## 6. Tests and teeth

**The tests:**

- `TestFrameAggregates`: eleven aggregates' types and values over nine types, the
  List column null, no rows, and the eager forms;
- `TestFrameAggregatesUnderAMemoryLimit`;
- `TestTemporalQuantile`.

They passed at `GODEBUG=simd=128`, and at `-cpu` 1 and 4, three times.

**Teeth, all biting:**

- a Boolean not given as 0 and 1;
- a product's null of the column's type;
- a List's min;
- a Boolean's quantile;
- a column it does not take dropped;
- a Date's quantile unscaled;
- a quantile's temporal output type lost.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
