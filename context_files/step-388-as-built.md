# Step 388 — as built

**Aggregate windows handed back batch by batch; expression CSE measured and left.**
O2 of `v0.7-scope.md`.

## 1. What O2's probe spends

§4's O2 probe (step 382), `WithColumns` of `x - x.Mean()`, its absolute value,
`x.Sum().Over(g)` and twice that, over a million rows, took 2.2 times Polars'. The
plan already computes each window once, `x.Mean()` and `x.Sum().Over(g)` each one
`__win` column read by both outputs that use it, as `Explain` shows; what is
repeated is the row-wise `x - __win0`, in two outputs. Timed apart, at a load of 1.5
to 2.5, on step 387's build:

| | min–median |
| --- | --- |
| `x - x.Mean()` alone | 18.8–21.9 ms |
| it and its absolute value, the subtraction twice | 27.3–31.6 |
| `x.Sum().Over(g)` alone | 17.1–21.2 |
| it and twice it | 24.6–26.5 |
| all four | 47.7–52.7, Polars 19.1–23.3 |

The repeated subtraction is about a sixth of the whole, and expression-level CSE
would save it; `x - x.Mean()` alone is nearly Polars' time for all four. Where its
time went: the frame's mean is a window, which held every batch, concatenated them
all at its end, and gathered the one value onto each row.

## 2. How

- **`finishByBatch`** (`physical/window.go`): a window sink whose windows are all
  aggregates over their partitions, `x.Mean()`, `x.Sum().Over(g)`, needs no row
  addressable as one, so it hands back its batches as they came, each with its
  windows' values gathered by its own rows' partition ids, the batches at once on the
  query's threads, where every batch was first concatenated into one. A sink with an
  ordered window, a rank, a cumulative or a rolling one, concatenates as before.
- **Expression-level CSE** is not built: a `WithColumns` reads the columns its
  earlier outputs define, so a shared subexpression is the same value only where no
  output between redefines what it reads; the saving measured above is a sixth of
  this probe. Windows and aggregates, the expensive part, were already shared.

## 3. Measured

The same parts on this step's build, alternating:

| | step 387 | here |
| --- | --- | --- |
| `x - x.Mean()` | 18.8–21.9 ms | 14.8–18.2 |
| it and its absolute value | 27.3–31.6 | 18.2–23.2 |
| `x.Sum().Over(g)` | 17.1–21.2 | 17.8–21.8 |
| all four | 47.7–52.7 | 41.5–46.9 |

A collected answer is concatenated still, by `Collect`, which a `DataFrame` needs;
a query that reads on, a filter or an aggregate after the window, no longer pays
it. The keyed window's time is its partition ids, a fifth, the sum at 128 bits and
its gather back.

No suite query computes a window (step 387).

## 4. Tests

**`TestAggregateWindowsAnswerBatchByBatchAsWhole`** (root): 4,000 rows; a frame
mean subtracted, a frame max, a sum over an integer key, a mean over a text key and
a count over both, with nulls, handed back batch by batch, against the same beside
an ordered window, which takes the concatenating path; on one thread and four, in
batches of 97 and the default, row for row. It passed under the race detector.

**Teeth: 2 of 2 bite:** each batch given the first batch's partition ids; an ordered
window taken for an aggregate.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB. `./internal/...` and the root package ran whole before it.
