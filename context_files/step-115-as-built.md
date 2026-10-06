# Step 115 — as built

**The tail of the misleading errors.** This is `v0.4-scope.md` item 23's remainder:
`audit.md` A9, A12, A13's last sentence, I25, S17, S23, the dynamic grid's
`!next.After` guard, and `spill.Writer.Write`'s bare error. Each was reproduced
against master by a probe before any change. S14 was already closed: step 100's
Int128 `*`, `//` and `%` made `UInt64 * 2` an exact Int128, and it is now pinned.

## 1. What each said, and says

| row | before | after |
| --- | --- | --- |
| A9 | A window inside `Agg`: the hint said to move the whole expression into `WithColumns`, an aggregate it refuses, and then aggregate a bare `Col("w")`, which `Agg` refuses. `x.Sum().Over(g).Max()` never reached that hint: it was refused as *"not an aggregate … every column must be reduced"*, and it is reduced. | The hint moves the window alone: `.WithColumns(col("x").sum().over(col("k")).Alias("w"))`, and `Col("w")` in its place. A window function with no `Over`, moved out of a group-by, also gets: `.Over(the group-by keys)` runs it within each group. |
| A12 | An ordered `First().Over(...)` was "order-independent … except for first". | `first` *"depends on the order; sort the frame by the ordering first"*. `sum` *"does not depend on the order, so drop the ordering"*. |
| A12 | `Any` on an Int64: *"compare first, e.g. `Col("x").Gt(0).any()`"*, a method Go does not have. | `.Any()` and `.AllTrue()`. |
| A12 | An aggregate as a group key: *"group_by produces one output row per input row"*. | *"group_by needs one value per input row; an aggregate produces one value in total"*. |
| A13 | Temporal `Median`, `Quantile`, `Std`, `Var` and `Mean` were refused, and nothing documented it. | Documented on each method: a Duration's mean is a Duration; the rest are refused for now. The refusal adds that `.Cast(ursus.Int64)` gives the ticks. |
| S17 | The doc said `Neg` and `Abs` were defined for the unsigned integers. `Neg` is refused. | The doc says `Abs` of an unsigned integer is itself, and `Neg` is refused, with the cast. |
| S23 | A panic in a `MapName` function: *"recovered a panic … this is a bug in ursus"*. | ErrValue: *"the function passed to MapName panicked on "v""*. |
| I25 | A panic in the `RecordReader` that `ScanArrow`'s factory returns was ursus's bug. A panic in the factory itself was already the stream's I/O error. | Both are I/O: *"reading the Arrow stream panicked"*. |
| grid | A row at the last instant a `time.Time` holds: *"every=1h does not advance … a bug in ursus"*. | ErrValue: *"the window grid cannot step past 292277024627-12-06T15:30:07Z, the edge of what Go's time.Time holds"*. |
| spill | `Writer.Write` returned the flush's error bare: no kind, no file, no word of spilling. `Close` wrapped the same flush. | KindIO, *"spill: writing <file>"*, with `WithSpillDir` as the hint. |

## 2. How

- **A9:**
  - `rejectWindow` walks to the outermost window and renders only that.
  - **`IsAggregation`'s aggregate arm asks `HasUnboundAgg`, not `HasAgg`.** An
    aggregate under a window is bound by it, so `Max` of a windowed sum reduces.
    The window is then refused by name, as unsupported.
  - My first fix moved the window check ahead of the aggregate check instead. That
    turned an unreduced window in `Agg` from a type error into "unsupported".
    `TestWindowRejectedInsideGroupByAgg` caught it, and the order is back.
- **A12:** the ordered-aggregate refusal had two copies: the resolver's, which users
  see, and the physical planner's backstop. Both now call `expr.OrderedAggRefusal`,
  which branches on `IsOrderDependent`.
- **S23:** `MapName` wraps the function. The planner calls it where nothing returns
  an error, so its panic is re-raised with `uerr.Raise`, and Collect's recover
  returns it as it is.
- **I25:** `arrowsrc`'s `callerCode` runs the factory and every `Next`/`RecordBatch`
  under a recover, as `Schema` already ran the factory. A panic and a read error are
  kept apart, so a reader's own error is still wrapped as before.
- **The grid:** the resolver has proven every positive, so `every` can never be the
  cause. A `time.Time` saturates at its last instant. A Datetime(s) cast from an
  Int64 is taken as seconds unchecked, so a row can sit there.
  - **The backward guard** has the same message, and is not reachable from Unix
    seconds: the first instant they reach is far from where a `time.Time`
    saturates. The test pins that this grid answers, counting both rows.

## 3. Tests and teeth

- **`audittail_test.go`:** each refusal says the right thing, and **the rewrite its
  hint recommends runs**. That second check is what A9 lacked.
  - `TestTheWindowHintMovesTheWindow` checks a reduced window in `Agg`, a bare
    window function in `Agg`, and a window in a filter.
  - `TestAggregateHintsSayWhatIsTrue` includes the recipe for an ordered `first`: the
    frame sorted, the window unordered.
  - `TestTemporalStatisticsAreRefusedWithACast`, `TestNegAndAbsOfUnsigned`,
    `TestUInt64ArithmeticWithAnIntLiteral` and `TestADynamicGridAtTheEdgeOfTime`
    cover A13, S17, S14 and the grid.
- **`panic_test.go`** gains two child-process cases: an Arrow reader whose `Next`
  panics on four threads, and a `MapName` whose function panics.
- **`internal/spill`:** `TestAFailedWriteSaysItWasSpilling` closes the file under a
  writer, so its flush fails.

| tooth | result |
| --- | --- |
| the hint moves the whole expression | **bites:** all three window cases |
| `IsAggregation` back to `HasAgg` | **bites** |
| the ordered refusal ignores order dependence | **bites** |
| the Any hint lower-case again | **bites** |
| the group-key wording back | **bites** |
| no temporal hint | **bites:** all four |
| `MapName` unguarded | **bites** |
| the Arrow reader unrecovered | **bites** |
| the grid guard internal again | **bites** |
| the spill write bare | **bites** |

**`audit.md`:** step 115 brings the audit up to date for 0.4. It had not been updated
since step 87, so it also marks the rows that steps 98–111 closed:

- I24 (98);
- J12 and I18 (108);
- J7 (109);
- J9 (110);
- J10, S15, S16, A11 and A15 (111);
- I16 (112);
- S14 (100, pinned here).

Its "known and excluded" list also strikes `rolling`'s accounting (96) and the
planner leak (95).

**Still open from item 23:**

- **J11:** a spilled join's misattributed refusal, whose real cause was never found.
  It needs an investigation, not a message.
- **I26:** `Rows` decoding a List, which is a feature.
- **A8:** `Diff` and `PctChange` inside `.Over()`, which is item 20's.

**Gate:** test-all 110 ok, race 22 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
