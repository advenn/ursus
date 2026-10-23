# Step 330 — as built

**`TopKBy` and `BottomKBy`,** Polars' `top_k_by` and `bottom_k_by` (`polars-parity.md`
§2): the k values where another column is greatest, or least, a List a group, as
ursus's `TopK` answers.

## 1. The API

`Col("p").TopKBy(by, k)` and `BottomKBy(by, k)`: paired aggregates, as `MaxBy` is,
`PairOf(value, by)` with `Params.K`.

**What Polars answers, and ursus too:**

- **The order:** the k values in by's order, greatest first for `TopKBy` and least
  first for `BottomKBy`;
- **NaN** the greatest float; true above false; text by its bytes;
- **a null by** after every value, in either direction, so k past a group's
  non-null bys takes its null-by rows last;
- **a value's null** kept, the row's, so a group's list is min(k, its rows) long.

**Where ursus differs:**

- **A List a group, and a List a frame,** as `TopK` is (§9), where Polars' answers
  k rows in a `select`.
- **Rows that tie on by are ordered by their values,** the greater first for
  `TopKBy`, the lesser for `BottomKBy`, a null value last. Polars' sort orders
  ties as it finds them: its answer for six rows was row order, and for a hundred
  rows over three bys it was not (`[47, 5, 8, 11, 14]`). An earlier-row rule, as
  `MaxBy`'s, would make the aggregate order-dependent, so a group-by holding it would
  run on one worker. By the value, no row order matters and the merge is exact in
  any order.
- **k must be at least 1,** as `TopK`'s; Polars answers k of 0 with nothing.

## 2. How

`internal/kernel/topkby.go`, `topKByAcc[K, T]`: per group at most k entries, a by
key and a value, each with its null, best first, copied out of their batch. A row
that does not beat the k-th is refused by one comparison; the rest go in by binary
search. The value's order is `TopK`'s, -0 below +0 so the answer is the same in any
order. Merge offers the other's entries. `newTopKBy` instantiates by by's physical
type, then the value's: a key of the by's own type, ordered as a sort orders it.

In `internal/expr`: `AggTopKBy` and `AggBottomKBy`, appended, paired, typed by
`pairedBinding` as a List of the value's type, and k checked as `TopK`'s.

## 3. Tests

**`TestTopKBy`,** every answer Polars' but the ties:

- a frame's top and bottom 3, a value's null kept, a null by last, by text;
- a group's top and bottom 2, and 5, past the rows;
- NaN and Boolean bys; ties to the value, top and bottom, a null value after the
  values it ties with;
- a thousand rows over three bys and four groups, against a sort by (by, value)
  then `Head(4)`;
- k of 0, a List value and a Struct by refused.

**The aggregate contracts:**

- **merge coverage:** the pair families, merged partitions equal to one pass;
- **the behaviour classes:** a one-row group's list of one;
- **the decimal sweep:** refused over one column, as `MinBy` is.

## 4. Performance

Against Polars 1.44.1, a million rows of 1000 groups, the median of 7:

| operation | ursus | Polars |
| --- | --- | --- |
| a group-by's `TopKBy(by, 3)` | 7.5 ms | 29.9 ms |
| a group-by's `TopK(3)`, for scale | 6.7 ms | 14.7 ms |
| a frame's `TopKBy(by, 10)` | 3.9 ms | 15.9 ms |

Four times Polars' speed. A bounded state a group, on every worker, merged in any
order: the tie rule is what keeps the workers.

## 5. Docs

- **Aggregation:** a bullet beside `MaxBy`.
- **Coming from Polars:** a row.
- **The CHANGELOG, `feature-gaps.md` §9, `polars-parity.md`.**

## 6. Teeth, all 9 biting

- a null by first;
- one direction for both;
- ties in the rows' order, which the merge test and the ties catch;
- a null value first among ties: silent at first, no test holding a null value
  tied with another; one was added;
- the k-th never beaten;
- a value's null dropped;
- a merge that drops the other;
- NaN below every number;
- true below false.

**Gate:** test-all 130 ok, race 26 ok (the root package 538 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
