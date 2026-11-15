# Step 523 — as built

**A float Min or Max is the same at every thread count.** qcheck's self check (step
512) found a keyed float `Min` answering +0.0 on four threads where one thread
answered −0.0. A parallel group-by deals its workers batches round-robin and folds
their partial states in worker order, which is not row order, and an accumulator that
keeps "the first of equal values" kept the first in folding order. Each float Min and
Max now keeps the input position of the row it chose, the dispatcher deals each batch
with the position of its first row, and the fold breaks a tie between −0 and +0, or
between NaNs of different payloads, by position. Max takes the latest of a tie and Min
the earliest, the bits a stable `Sort(...).Last()` and `.First()` give, which the docs
promised and the serial Max did not give. Four more picks of the same class were found
and fixed: Median and Quantile, TopK and BottomK, TopKBy's values, and a float group
key. The parallel group-by keeps running in parallel.

## 1. The finding

```
FINDING mismatch, seed 802018528
  reference: {threads 1, batch 8192, optimizer on, 1 record(s)}
  variant:   {threads 4, batch 3, optimizer off+verify, 1 record(s)}
  row 20 differs in column "a4": float64(0), the reference's math.Copysign(0, -1)
  q := ScanArrowRecords(t0...).Filter(Col("boolc")).
         Join(ScanArrowRecords(t0...).Filter(Lit(false).Not()), JoinOn(Col("f64b")), ...).
         GroupBy(Col("i64a_right"), Col("i64a_right").Alias("k1")).
         Agg(Len(), Lit(int64(321185)).NUnique(), Col("f64b").Min().Alias("a4"), Col("boolc_right").Count())
```

A second seed came in while the step was under way, **807010171**: a group-by of
`Mean`, `Max`, a `Sum` and `Len` over `ScanArrowRecords`, −0 at the reference and +0
at four threads in batches of one. It was reported as the `Mean`; the column that
differs is `a5`, the `Max`, over a group of two rows, −0 and +0, and the `Mean` (`a4`)
is +0 on both sides. It is this step's class and this step fixes it. Both seeds find
nothing now, and run in `qcheck/self`'s `TestFoundSeedsFindNothing`.

## 2. The docs it broke

- **`Expr.Min` and `Expr.Max`** (`agg.go`): "use ursus's TOTAL order, the same one
  Sort uses: NaN sorts above everything and -0.0 equals +0.0. They therefore always
  agree with Sort(...).First() / .Last()". Sort is stable, so `Sort().First()` is the
  earliest of a tie of least values and `Sort().Last()` the latest of a tie of greatest
  ones. The kernel kept the first of equal values for both, so Max broke this on one
  thread too: of [+0, −0] it answered +0, where `Sort().Last()` is −0.
- **`WithThreads`** (`lazy.go`): "Results do not depend on it … the same query returns
  byte-identical output at any thread count", and "Group-by also runs N-ways, and only
  where that cannot change an answer".

No doc changes: the code now meets both.

## 3. The fix

**Positions, kept where a fold needs them** (`internal/kernel/aggminmax.go`,
`aggwhole.go`, `agg.go`):

- `kernel.RowPlacer`, `PlaceRows(base)`: the input position of the next batch's first
  row; the rows of `AddBatch`'s or `AddAll`'s column follow it in order. A filtered
  input or a frozen sink's kept rows are a subset in order, so `base + index` stays
  ordered across batches, which is all a tie needs.
- `extremumNum` of a float (`ties`: Min, Max, NanMin; NanMax is Max): `AddBatch` keeps
  the first of equal values for Min and NanMin and the last for Max, which is the row
  order within one sink. Once placed (`track`), it keeps `at`, each group's chosen
  row's position, and `Merge` takes the other state's value on a tie when its row is
  the earlier for Min, the later for Max.
- Only a zero or a NaN is a tie that bits can tell apart (`tieable`): two equal
  numbers else are the same bits, so their position is never read, never written, and
  `at` is not made until a zero or a NaN is chosen. A tie is found inline in the fold:
  `x == cur` (which holds for −0 and +0) or both NaN.
- A serial group-by never merges, never places, and keeps no position: its only change
  is that Max takes an equal value, the same one comparison. That rule is the
  accumulator's, so Max in a `Select`, a list's `Max`, a window's and `MaxHorizontal`
  (whose doc compares "as Max and Min compare a column's") answer the latest of a tie
  too.

**The dispatcher deals positions** (`internal/physical/parallelsink.go`,
`aggconsume.go`): each job is `dealt{batch, row}`, the cumulative row count before the
batch; a `placedSink` is told it before `Consume`. `hashAggSink.rowBase` is the dealt
position for a worker, or the sink's own row count, which after a fold counts the
folded workers' too, so the serial rest of a group-by that went serial at its budget
continues the positions. A worker places from its first batch (`placing`), and on past
a turn to serial. An empty batch's dealt position is taken before the early return,
so it is never left for the next batch.

**A strict order for the picks that keep no row** (`internal/kernel/order.go`):
`StrictKeyF64` and `StrictKeyF32` map a float's bits one to one onto a key whose
unsigned order is the total order, with −0 below +0 and NaNs ordered by their bits
(the classic sign-flip key less the 2^52−1 keys of the negative NaNs, which wrap to
the top, so every NaN stays above +Inf). The k-th of a multiset is then one bit
pattern however the values arrived.

- **Median and Quantile** (`aggstat.go`): quickselect partitions by `StrictKeyF64`,
  and the upper rank is the strict minimum of the rest. Under the total order alone
  which of two equal values stood at a rank followed the buffer's order, worker-major
  after a fold, so the median of a group of −0 and +0 was either.
- **A spilled quantile** (`quantspill.go`, `physical/valuespill.go`): each run sorts by
  the strict order, and the value spill stores the strict keys as a Uint64 column and
  merges on them, decoding back. Merged as Float64 by the external sort's order key,
  equal values came back in the order of their runs; a serial query under a memory
  limit answered other bits than without one.
- **TopK, BottomK, and TopKBy's values** (`topk.go`, `topkby.go`): ordered by the
  strict keys. They already put −0 below +0 (`compareSignedZero`); two NaNs were still
  a tie, kept in arrival order.

**A float group key's first row** (`internal/physical/aggmerge.go`, `agg.go`): a group
of a float key, or of a Struct, List or Array holding one (`hasFloat`), holds rows of
−0 and +0, or of NaNs, and its key is its first row's, as the serial sink takes it. A
fold kept its own key for a group it had and so kept a later row's bits:
`GroupBy(x)` answered −0 on eight threads where one thread answered +0. Each worker
keeps `firstAt`, each group's first row's position; a fold (the serial `Merge` and the
partitioned `mergeShare`) that meets a group whose first row the other sink held takes
that sink's key as a `keyFix`, and `residentResult` applies the fixes in turn
(`fixKeys`, one `Concat` and `Take` of the keys, only where a fix exists).
`firstRowsHeld` refuses, ErrInternal, a fold of sinks whose first rows were not kept.

## 4. The differential

`floatpicks_test.go`, over frames generated from a seed (`genPicks`): a key `g`;
`v64` and `v32` drawing per group from a subset of −0, +0, −1.5, 2.5 and five NaNs of
distinct bits (a signalling one among them), with nulls; `z64` and `z32`, zeros of
either sign and no null, which a keyless aggregate reads whole (`AddAll`); `n64`, NaNs
only; `by`, for the by-aggregates. No expected value is typed: every answer is held to
a differential or a Go oracle, by its bits (`cellBits`: a float's word, a list's
elements', a null).

- **Oracle 1** (`TestAFloatPickIsTheSameAtEveryThreadCount`): Min, Max, NanMin, NanMax,
  TopK(1), TopK(3), BottomK(2), TopKBy(2), BottomKBy(1), Median, Quantile(0.25) under
  each of the five interpolations, and Min and Max of a filtered input, keyed and
  keyless, bit for bit the reference's (one thread, batches of 8192) at threads {1, 4,
  8} × batch sizes {1, 2, 3, 7, 1024} × MaintainOrder {off, on}, two seeds (523, and
  qcheck's 802018528). ArgMin, ArgMax, MinBy and MaxBy too, in a list of their own:
  they keep a group-by serial, and are the same everywhere.
- **Spilled** (`TestAFloatPickIsTheSameSpilled`): the same list under a memory limit
  that spills (asserted), few groups (the quantiles' values to disk) and many (the
  keys too), at three configurations.
- **Folded partitioned** (`TestAFloatPickIsTheSameFoldedPartitioned`): 160,000 rows of
  80,000 groups, enough that the fold runs a partition each (`foldPartitioned`); and a
  float key folded so, its zero group and its NaN group first met in a batch dealt to
  worker 1, so worker 0, folded first, holds later rows of each.
- **Oracle 2** (`TestFloatMinAndMaxAreTheStableSortsEnds`): the reference's Min and Max
  are each group's values sorted stably in Go by the documented order, first and last,
  and ursus's own `Col(v).DropNulls().Sort().First()` and `.Last()`; NanMin is the
  first NaN or the least; ArgMin and ArgMax the earliest row of the least and of the
  greatest, nulls counted.
- **qcheck's shape** (`TestFloatMinOfAJoinedGroupIsTheSameOnFourThreads`): a join on a
  float key and a group-by of the right side's key, four threads in batches of three.
- **Float keys** (`TestAFloatGroupKeyIsTheSameAtEveryThreadCount`): `GroupBy` of each
  float column at every configuration, and a Go oracle: each key is its group's first
  row's bits.
- **Sums** (`TestAFloatSumOfZerosIsTheSameAtEveryThreadCount`), for seed 807010171:
  Sum, Mean, Var, Std and Product over zeros of either sign, bit for bit at every
  configuration; over NaNs, NaN everywhere, a NaN compared as a NaN (§6).
- **Kernel** (`strictkey_internal_test.go`): `StrictKeyF64` is one to one, `StrictF64`
  undoes it, its order is `OrderKeyF64`'s wherever that one parts two floats, and two
  keys are equal only for the same bits, over generated floats of every class, and
  `StrictKeyF32` likewise; `quantileOf` of a group of −0, +0, ±1 and NaNs answers the
  strict sort's ranks, in bits, in four shuffled orders, at six q and all five
  interpolations. The value spill's test merges by the strict key and compares bits.
- **qcheck** (`qcheck/self`, `TestFoundSeedsFindNothing`): seeds 802018528 and
  807010171, one case each, no finding.

On master (de826b9, this file copied there) six of the seven tests failed, 1,545
lines: every pick above at threads 4 and 8, keyed and keyless, the partitioned fold
among them; the quantiles under a memory limit, on one thread too; Max against both
Sorts' `Last` on one thread; the float keys. The sums' test passed there as here.

## 5. The cost

One keyed float Min group-by, 1,048,576 rows, 100,000 groups, eight threads,
`GOMAXPROCS=8`, a program of one query (median of seven runs after one, four
alternations of master and the fix, under the heavy lock):

| values | master (median of medians, min) | this step | cost |
| --- | --- | --- | --- |
| normal, no zero | 28.9 ms, 26.6 ms | 28.9 ms, 26.9 ms | none measurable |
| normal, 5% zeros of either sign | 29.4 ms, 27.4 ms | 32.1 ms, 30.0 ms | +2.5 ms, about 9% |

Where it goes, by profile: the positions of zero minimums, a store into a second
array a group, and making that array; the fold's tie check is inline and found no
cost. A first version that kept a position for every chosen value cost 10 to 15% on
the column with zeros; keeping one only for a zero or a NaN, made on first use, left
the cost to the columns that hold them. A serial group-by keeps no position.

## 6. Other order-dependent picks

Each aggregate the parallel group-by runs (every one `IsOrderDependent` does not
decline), checked for an answer whose bits depend on which of several rows wins:

| aggregate | finding | |
| --- | --- | --- |
| Min, Max, NanMin, NanMax of a float | −0/+0 and NaN payloads by fold order | fixed, positions (§3) |
| Median, Quantile | the value at a rank among equal values, by buffer order; spilled, by run order | fixed, strict order |
| TopK, BottomK | two NaNs a tie, by arrival | fixed, strict order |
| TopKBy, BottomKBy | two NaN values of one by a tie | fixed, strict order |
| a float group key | the first row's bits, by fold order | fixed, first-row positions |
| Min, Max of an integer, a temporal, a Decimal, a string, a Bool | equal values are the same bits | none |
| Count, Len, NullCount, NUnique, ApproxNUnique, Any, AllTrue, bitwise | counts and bits | none |
| Item | refuses more than one value | none |
| LowerBound, UpperBound | constants | none |
| CorrSpearman | ranks a tie by the mean of its ranks, sums exact | none |
| Sum, Mean, Var, Std, Skew, Kurtosis, Product, Corr, Cov, Entropy | a zero's sign is exact in any order: a sum starts at +0 and is never −0, a product's sign is its factors'; tested over zeros (§4) | none |
| the same, over NaNs of several payloads | **listed, not fixed:** NaN at every thread count, but which payload is the arithmetic's: x86 returns the first NaN operand, so the payload follows the order of the additions, as the last bits of a sum do (D8, step 437, never decided; step 512's float-threads rule). A remedy, if wanted: a float aggregate's Finish answers one canonical NaN. It changes serial answers' NaN bits, so it is the maintainer's to decide with D8 | listed |

First, Last, ArgMin, ArgMax, MinBy, MaxBy, Implode, Mode and IsSorted are declined by
`IsOrderDependent` and keep a group-by serial; the differential shows them the same at
every configuration.

On qcheck: its float-threads rule lets a noisy cell (a float Sum or Mean) differ between
thread counts within its error bound, and a −0 against a +0 is |Δ| = 0, within it. That
is right for a Mean: a Mean is −0 where its sum is a tiny negative number that the
division rounds to zero, and the sum's last bits are the rule's noise. A Sum is never
−0, so a Sum cannot differ by sign alone. Nothing in qcheck changed.

## 7. The teeth

Each from the saved copies, the fixed files saved first and copied back after each, not
through git (`cmp` against the copies after the last):

| tooth | what it restores | the differential |
| --- | --- | --- |
| T1 | the old `extremumNum.Merge`, from a copy of master's file | 613 lines: min, max, nanmin, nanmax, keyed and keyless, threads 4 and 8, partitioned |
| T2 | Max keeping the first of equal values | 626 lines, Oracle 2 among them (240 groups against both Sorts) |
| T3 | quickselect by the total order | 570 lines: every quantile, threads and spilled |
| T4 | the value spill as Float64 by the order key | 80 lines: the spilled quantiles |
| T5 | two NaNs equal in the strict comparison | 398 lines: top-k, top-k-by, quantiles |
| T6 | the float key fixes not applied | 4 lines: the float keys, folded serially and partitioned |
| T7 | every batch dealt at position 0 | 612 lines: the picks and the float keys |

## 8. Checks

After the last edit, on master a5bb9df (steps 513 and 521 merged) with this step on
top, every `go test` with `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`:

| check | outcome |
| --- | --- |
| `go test ./internal/...` | ok, 29 packages |
| `go test .`, in full, under the heavy lock | ok, 189 s |
| `cd qcheck && go test ./...` | ok, `TestFoundSeedsFindNothing` among them |
| `qcheck self -duration 20s` | 1,350 cases, 4,050 comparisons, 0 findings; the float-threads rule fired in 16 of 98, 164 of 168 canaries |
| `GOEXPERIMENT=simd go vet ./...` | clean |
| `go run ./internal/gen/levels` | ok |
| `GOEXPERIMENT=simd go run ./internal/gen/api -check` | 2,481 features, 0 gone since v0.7.2 |
| `go run ./internal/gen/testinventory -check` | 1,225 hand-typed lines of 1,225 allowed; the new tests add none |
| `gofmt -l .` (the GOROOT's) | nothing |
| the differential under `-race` | ok, 80 s |

CI's `qcheck-duckdb` job (step 513, ursus against DuckDB) failed `T-EXTREMUM` on
the first push: group k=2 holds +0 then −0, ursus's Max now answers −0, and the
translation's `u_fmax` still kept the first of a tie, +0. `T-EXTREMUM` follows this
step now: `u_fmax` keeps the last of equal values and `u_fmin` the first
(`qcheck/sqlgen/macros.go`), and the rule's line and step 513's record say so. Step
513 translates no TopK, Median or Quantile, so no other macro encoded a tie.

The spilled test flaked once at its first limit: four threads in batches of seven over
the many-groups frame held, by scheduling, 260 KiB against a limit of 256 KiB, its
operators' share and not the group-by's. The limit is 512 KiB now, which still spills
(asserted), and held through 25 runs.
