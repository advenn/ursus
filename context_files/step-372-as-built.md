# Step 372 — as built

**A frame's aggregate without a group-by's machinery:** an aggregate of no keys takes
its columns whole, where it made a group id a row. P4 of `v0.6.8-scope.md`.

## 1. How

**The accumulators** (`internal/kernel/aggwhole.go`) that can take a whole column into
their one group say so, `WholeAdder`: `Whole()` for the input they were built for, and
`AddAll(col)`, whose answer is `AddBatch`'s with every row in group 0, to the bit:

- **counts:** `Len` by the column's length, `Count` by its length less its null count,
  `NullCount` by the null count; and `Len` of a literal, `RowAdder`, told the rows
  without its column: `Len()` is `lit(1).len()`, which was evaluated to a column of
  ones a batch;
- **sums and means** of an integer of 64 bits or fewer, signed or not, or a Boolean,
  exact in 128 bits: an `Int64` with no null in its high and low 32 bits apart, two
  adds a row with no carry between them, which cannot overflow below 2^31 rows; with
  nulls, or narrower or unsigned, by `bits.Add64`'s carries. No copy to `Int128`
  first, which the mean made. A Date's mean, scaled a row, and a 128-bit input, a
  Decimal's or an Int128's, keep `AddBatch`;
- **sums and means of floats** add the same values in the same order, in a local, so
  their bits do not change;
- **minima and maxima:** an integer's compared in place, a float's by its order key in
  place, the first of equal keys kept as `lessFloat64` keeps it, -0 and +0 alike, NaN
  the greatest; `NanMin`'s by its own comparison;
- **first and last:** the first row of the first column, the last of each.

**The sink** (`hashAggSink.Consume`): an aggregate of no keys whose accumulators all
take whole columns evaluates each input once and hands it over (`consumeWhole`), a
filter inside an aggregate, `x.Filter(p).Sum()`, taking its passing rows first. Any
other aggregate, of keys, or of no keys with an accumulator that takes none whole, a
median beside a sum, is as it was. The end of `Consume`, its memory and the budget,
is `consumed`, shared.

## 2. Measured

A million rows of an `Int64` and a `Float64`, a frame's aggregate on four threads, at a
load of 2.3 to 3.0, three runs of thirty:

| | before | after | Polars 1.44.1 |
| --- | --- | --- | --- |
| `Len()` | 2.4–2.7 ms | 0.13–0.16 | 0.016 |
| `Count` | 1.9–2.0 | 0.17–0.19 | 0.021 |
| `First` | 2.9–3.3 | 0.11–0.12 | 0.021 |
| `Sum` of the `Int64` | 2.1–3.1 | 0.93–1.0 | 0.15 |
| `Mean` of the `Int64` | 3.5–4.3 | 0.94–1.1 | 0.41–0.57 |
| `Min` of the `Int64` | 3.3–3.5 | 0.88–1.1 | 0.15–0.17 |
| `Sum` of the `Float64` | 1.9–2.2 | 0.91–0.94 | 0.13–0.14 |
| `Mean` of the `Float64` | 2.1–2.4 | 0.97–1.0 | 0.27–0.32 |
| `Max` of the `Float64` | 4.3–4.4 | 1.9–2.0 | 0.17–0.21 |

The rest is the scan and the pipeline of 123 batches, and the float sums' order: a
float's sum adds in one chain, as it must to keep its bits, where Polars' adds in
lanes. The `Int64`'s sum loop alone, on a million values: 0.99 ms in halves against
1.9–2.3 by carries.

**The suites:** PDS-H's aggregates of no keys, q6, q14, q17 and q19, at SF=1 on four
threads against v0.6.7's runner, both built as the bench builds one, alternating
query by query, their order swapped each round, three rounds of five at a load of 4.3
to 4.8: 0.96 to 1.01 of v0.6.7's. Each aggregates few rows after its filter and its
joins, so the aggregation was never its cost.

**A count over a file,** step 370's: `Select(Len())` over PDS-H's lineitem at SF=1,
six million rows, takes 2.3–2.8 ms, where it took 27–39; Polars 0.4.

## 3. Tests

- **`TestAddAllIsAddBatchOfOneGroup`:** each of `Len`, `Count`, `NullCount`, `Sum`,
  `Mean`, `Min`, `Max`, `NanMin`, `First` and `Last` over a Boolean, `Int8`, `Int32`,
  `Int64`, `Uint16`, `Uint64`, `Float32`, `Float64`, a Datetime and a Duration that
  takes whole columns, fed batches whole and a row at a time into group 0 by two
  accumulators: one with nulls and values at its type's ends, sums past 64 bits both
  ways and floats whose sums round by their order; one all null; one with no null and
  the ends; one plain; one empty; and, for a float, -0 and +0 alone. The answers equal
  to the bit, a Duration's sum past its int64 refused alike; a count of rows told
  them, one of values not.
- **`TestAFramesAggregateAnswersAsAGroupsDoes`:** seventeen aggregates of a frame with
  nulls, past 64 bits too, and a filtered sum; a median beside a sum; a Decimal's sum;
  each as the same of one group, a group-by of a constant, on 1 and 4 threads, in
  batches of 333 and the default, to the bit; an empty frame's one row.

**Teeth: 11 of 11 bite:** a count of values counting nulls; a sum never seen; the
halves' high word unsigned; a signed value's sign dropped; an unsigned carry dropped;
a float sum in two lanes; a float's tie the last; an integer's minimum the maximum; a
last the first row; a filtered aggregate unfiltered; an aggregate not whole taken
whole.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB.
