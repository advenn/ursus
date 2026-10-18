# Step 279 — as built

**M3 of `v0.6.6-scope.md`, first half: search, peaks and hashing.** These are
Polars' `search_sorted`, `index_of`, `peak_max`, `peak_min` and `hash`. The second
half, step 280, is `unique_counts` and `value_counts`.

## 1. What changed

**The total order as values:**

- **The problem:** Polars compares in a total order, NaN above every number, where
  ursus's `Lt` and `Eq` are IEEE's: `1 < NaN` and `NaN > 1` are both false here. So
  `search_sorted`, `index_of` and the peaks, written in ursus's comparisons, would
  answer wrongly wherever a NaN is. A rule chosen when the expression is built cannot
  tell, as the column's type is not known then.
- **Two functions** give the order as values, in the order `Sort` uses, NaN above
  every number and −0 with 0 (`internal/kernel/ordercmp.go`):
  - **`order_cmp(a, b)`:** horizontal, −1, 0 or 1 a row in the operands' common type,
    null where either is;
  - **`order_sign(x)`:** a value beside its type's zero: a number's sign, NaN's 1, an
    instant's beside the epoch, a Boolean's 1 for true. Text has no zero and is
    refused.
- **The functions are written in them,** and in aggregates and window functions there
  were, so each answers in a `Select`, a group-by's aggregation and a window alike.

**`SearchSorted(value, side, descending)`:**

- **The answer:** the count of values that sort before value, or not after it with
  `SideRight`, in the total order, as a UInt32. `SideAny` is `SideLeft`, which Polars'
  "any" gives in every case probed.
- **Nulls are where Polars' are:** first where the first value is null, an all-null
  column among them, else last. They are counted in only when first.
- **A null value:**
  - goes before the nulls, or after them with `SideRight`;
  - where they are last, that is the count of values, or the length;
  - where there is no null, it is 0.
- **Every case was checked:** the four null layouts and none, both sides, and Polars'
  table of a null and of 2 in each.
- **A difference:** over an unsorted column it is a count, where Polars' binary search
  lands where it lands.

**`IndexOf(value)`:**

- **The answer:** the first row where `order_cmp` is 0, by `ArgMax` of the matches,
  as a UInt32. Null where there is none.
- **Matching:** a null value finds the first null; NaN finds NaN and 0 finds −0, as
  Polars finds them.

**`PeakMax` and `PeakMin`:**

- **The rule:** greater, or less, than both neighbours, by `order_cmp` with the
  value's shift.
- **At an end,** the value is compared with its type's zero, by `order_sign`. That is
  Polars' rule, read off its answers and explained by its shifts filled with 0:
  - the first of [3, 1] is a peak and of [−1, −2] is not;
  - one value is a peak if above zero;
  - a date is compared with the epoch.
- **Ends and nulls are told apart:** an end is found where `x.IsNull().Shift(n)` is
  null, which is never null where the column is. So a real null neighbour still
  makes the answer null, and an end does not.
- **Text is refused,** as Polars refuses it.
- **A group's own peaks are `.Over(key)`.** Polars' group_by gives each group's as a
  list; ursus's aggregation takes aggregates.

**`Hash(seed)`:**

- **The answer:** a UInt64. FNV-1a, seeded, over the bytes a group-by keys a value
  by, then mixed by splitmix64's finalizer.
- **Equal values hash alike:** −0 with 0, NaN with NaN, and each null alike. A Null
  column's values hash as an Int64's nulls do.
- **The numbers are ursus's,** not Polars' (§6.3).

**CallFns:**

- `FnOrderCmp` joins the horizontal block.
- `FnHash` and `FnOrderSign` are a general block of their own at 1200, under
  `IsGeneral`.
- The sweeps run to 1300; the names test counts 201.

**A gap found:**

- **An aggregate inside an aggregate's input is refused:** `Sum` of `a − Mean(a)`,
  in a `Select` or an `Agg`, with "expression is not an aggregate". Polars takes it.
- **Effect here:** `SearchSorted` and `IndexOf` take a Go value or a null, not an Expr
  holding an aggregate such as `Max()`.
- **Recorded:** `feature-gaps.md` §5. It is wider than this step.

## 2. Tests and teeth

Every answer is Polars 1.44.1's, but `Hash`'s.

- **`TestSearchSortedAndIndexOf`:**
  - search_sorted both sides, by group, descending three ways;
  - NaN last, text, no rows, all null;
  - Polars' table of a null and of 2 in five layouts;
  - index_of of a value, text, a null and one absent, by group, of NaN and 0, of a
    null where none is;
  - the types; a side that is none.
- **`TestPeaks`:**
  - Polars' column of nulls and plateaus;
  - NaN above every number;
  - negatives, falling, one value, below zero;
  - Booleans, dates against the epoch;
  - over a group; text refused.
- **`TestHash`:** −0 and 0, NaN and NaN, a null; unequal values apart; the seed
  changes each; deterministic.

| tooth | result |
| --- | --- |
| floats in Go's order, NaN least | **bites** |
| nulls first not counted | **bites** |
| a null to the right of nulls last | **bites** |
| descending searches ascending | **bites**, once an asymmetric case was added |
| right is left | **bites** |
| index_of of none is 0 | **bites** |
| a null neighbour is an end | **bites** |
| an end is always lower | **bites** |
| the seed ignored | **bites** |
| the value ignored | **bites** |

The descending tooth was silent at first: [5, 3, 3, 1] searched for 3 has as many
values above as below, so counting the wrong side answers alike.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
