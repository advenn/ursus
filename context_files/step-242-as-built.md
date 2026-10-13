# Step 242 — as built

**G2 of `v0.6.4-scope.md`: a window aggregate that keeps every value, a median, a
quantile or n_unique in `.Over()`, spills its values over one huge partition,**
where it refused.

## 1. Why

Over one partition, with no partition_by or a hot key, a window's rows go to one
file and are read back, but its aggregates keep their state in memory, fed as group
0. A sum's state is a few scalars. A quantile's is every value and n_unique's every
distinct one, so past the limit the sink refused, though a group-by spills both
(steps 220, 241).

## 2. What changed

**`internal/physical/valuespill.go`.** The group-by's spill driver is shared:

- **`groupSpill.take(acc)`** hands an accumulator's values to the spill: a
  `ValueSpiller`'s to a `valueSpill`, a `KeySpiller`'s to a `keySpill`;
- **`spillState`** spills every accumulator that keeps its state a value at a time,
  making its spill the first time under a prefix the sink gives. `spillable`,
  `setSpillsCtx` and `closeSpills` go with it;
- **`hashAggSink.spillValues`** is now a call to `spillState`, and `spillOf` is gone;
- **a run is written a batch at a time.** The kernel puts its values in chunks of
  8,192 whatever the query's batch, and the merge holds a block of every run it
  reads. At 500 rows a batch under an 80 KB limit this took the peak from 4.8× to
  2.1×.

**The one-partition path (`winstream.go`).** `chargeOneSpill(ctx)` and the new
`chargeAggs` are its memory:

- **over the limit,** it hands the values of every spillable aggregate to its
  spill, `v{i}-` in the window's spill directory (`ensureDir`, split from
  `newFile`), and is charged again;
- **it then refuses only once its own state is past the limit,** as a frozen
  group-by does. Before, it called `Check`, and over a hot key it refused holding
  93 bytes while the other keys' sink held the query over: that sink spills on its
  own next batch. What is left to refuse is implode, whose answer is every value;
- **`finishOneSpill`** has every spilled aggregate put the rest of its values to
  disk before any Finish reads them back, and charges the sink again. Each Finish
  did that itself, so until then the sink was charged for values on disk while
  another's merge held a batch of every run. At 500 rows a batch this took the
  peak from 2.1× to 1.5× the limit;
- **`windowSink.Close`** closes the spills;
- `startOneSpill` and `startSkew` take the context.

**Hints and comments.** `holdsHint("over")` says a window's quantiles and n_unique
spill their values and that implode refuses. The headers of `winstream.go` and
`winskew.go` no longer say rank and interpolation refuse (steps 237, 238).

## 3. Tests and teeth

**`TestAGrowingWindowAggregateSpillsItsValues`:**

- covers a median, a quantile of a float with nulls, NaN and ±0, and n_unique of
  all-distinct integers, of that float and of strings, beside a sum and a
  cumulative sum;
- with no partition_by and over a hot key, on one thread and four, under a tenth of
  the input at 64 rows a batch:
  - the answer is the in-memory one;
  - it spills;
  - the peak is at most twice the limit;
  - the spill directory is empty after;
- with no partition_by, at 500 rows a batch too, the peak stays at most twice the
  limit. Over a hot key the other keys' sink still holds their rows while the merge
  runs, so that shape is held to 2× at 64 rows only;
- implode in `.Over()` still refuses, naming implode;
- under the race detector it runs the hot key on four threads alone. The root
  package's race run takes 508 of its 600 seconds.

**Peaks at 64 rows a batch:** 1.4× the limit with no partition_by and 1.6× over the
hot key. A sum and a cumulative sum alone, over the hot key, peak at 1.6× too.

| tooth | result |
| --- | --- |
| a window never spills values | **bites:** it refuses |
| refuses while others hold the query | **bites:** the hot key refuses |
| charged for spilled values at Finish | **bites:** the 500-row peak, 2.06× |
| a run written in the kernel's chunks | **bites:** the window's peak |
| n_unique's keys to a value spill | **bites** |
| spilled with the wrong context at Finish | silent, and equivalent: a spill keeps the context it was made with, the same one Finish gets |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
