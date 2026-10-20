# Step 292 — as built

**R4 of `v0.6.7-scope.md`: a frame's sample.** Polars' `DataFrame.sample(n | fraction,
with_replacement, shuffle, seed)`, on both frames here (`framesample.go`).

## 1. What changed

**`SampleN(n, withReplacement, shuffle, seed)`, `SampleFraction(...)` and
`Shuffle(seed)`** on `LazyFrame` and `DataFrame`; Polars samples only an eager frame.

**Every column's values are of the same rows,** so the rows are drawn once, as
indices:

1. the frame's row index is sampled by `Expr.SampleN`'s kernel;
2. each drawn index is numbered by its place in the sample;
3. the draw is joined back to the frame's rows on the index, a left join;
4. the result is sorted by the place, and the two temporaries are dropped.

`All().SampleN` would not do: an expression's draw is seeded by the seed and its own
values, so each column would draw other rows.

**The sort is needed under a memory limit.** A join keeps the left frame's order in
memory, but under a limit no join's order is promised. The sort by the place keeps a
shuffle's order there. A tooth shows it: without the sort the spill test fails and
the rest pass.

**What it answers:**

- **Sizes and refusals are Polars':**
  - a fraction's count is truncated, so 0.4 of six rows is two;
  - more rows than the frame has without replacement are refused, as is any draw
    from no rows, as Polars refuses them.
- **Order:** unshuffled, the rows keep their order. Polars' unshuffled draw comes in
  its generator's order, but for all of the rows.
- **The rows:** the draw is ursus's generator's, seeded by the seed and the frame's
  height, so the rows are not Polars' (v0.6.6-scope.md §6.3, v0.6.7-scope.md §7.3).
  One seed draws the same rows of the same frame each time.

**Under a memory limit:**

- the join and the sort spill;
- the draw holds the row indices, one UInt32 a row, as one list, as an implode holds a
  group's values. It refuses past a limit smaller than they are: 80 KB for 20,000
  rows, where the rows themselves spill.

## 2. Tests and teeth

A draw cannot be Polars', so its properties are held.

- **`TestFrameSample`:**
  - nine draws: 3, half, 0.4, 10 and 1.5 with replacement, none, none of none, 3
    shuffled, and a shuffle. Each is checked for its size, every column's values
    from the same rows, none twice without replacement, the rows' order unshuffled,
    and one seed alike twice;
  - a shuffle out of order; seeds 7 and 8 apart; all six unshuffled in order;
  - the eager forms;
  - Polars' three refusals; a negative n.
- **`TestFrameSampleSpills`:** a shuffle of 20,000 rows under 256 KiB spills and
  equals the shuffle without a limit, row for row; under 16 KiB the indices do not
  fit and it refuses. Ten runs at each SIMD width, five scalar-only and two under the
  race detector.

| tooth | result |
| --- | --- |
| each column drawn apart | **bites** |
| no sort by the place | **bites**, in the spill test alone |
| sorted by the row, not the place | **bites** |
| Shuffle unshuffled | **bites** |
| an inner join | silent |

**The silent tooth is an equivalence:** every drawn index has its row, so an inner
join keeps what a left one keeps, and the sort sets the order either way.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.

**The first gate failed once, elsewhere.** It failed
`TestTemporalMediansSpillTheirValues/rolling_median` under `GODEBUG=simd=128`: the
live heap grew by 11 MB against its 10 MB bound. That is a 0.6.4 test of temporal
groups this step does not touch, and its bound is a measure of when the garbage
collector ran under test-all's load. Fifteen reruns under `simd=128` passed, and a
second gate was clean. `feature-gaps.md` §8 records the bound as one that trips.
