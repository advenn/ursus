# Step 142 — as built

**Item 4 of `v0.5-scope.md`: a filter copies its rows once, and a temporal literal
folds.** Step 141's profile put filters at 17 to 24% of PDS-H q7, q12, q15 and q19.

## 1. Filters copy once

### Evidence first

**`TestAFilterCopiesItsRowsOnce`** (`internal/physical`):

- three comparisons over 64,000 rows of eight Int64 columns;
- they keep a half, then two thirds of that, then a half: a sixth of the rows;
- it reads the bytes `filterOp.Apply` allocates against the bytes of the answer, and
  checks every value of the answer.

**Before: 6.63×.** Each conjunct ran `kernel.FilterBatch`, which gathers every column
of the rows it keeps. So the batch was copied at a half, a third and a sixth of its
rows, most of it to be thrown away by the next conjunct.

### What changed

**`filterOp.Apply`** (`internal/physical/operator.go`) keeps the rows as a selection
into its input. Each conjunct narrows it, composed with the one before.

- **`gatherRead`** gathers, at the selection so far, only the columns the next
  conjunct reads (`expr.RootNames`).
- **Every column is gathered once,** at the end, with `takeBatch`.
- **Evaluation finds a column by name** (`Eval`), so a narrower batch evaluates the
  same. A conjunct still sees only the rows the ones before it kept, as before, so
  an error it would raise on a dropped row is still not raised.
- A filter that keeps every row returns its input, uncopied, as before.

**After: 2.12×,** the rest being the selections and the two predicate columns
gathered on the way.

## 2. A temporal literal folds, and dates prune

### Evidence first

`Lit(t).Cast(ursus.Date)` is how a date is written, and the PDS-H port writes every
date that way. It stayed a cast through the whole optimizer:

- **The folder refused temporal values.** `litFromColumn` listed them with the
  lossy cases, so the cast ran on every batch: up to 1.4% of q19's CPU.
- **The Parquet pruner matches only a column against a literal,** so it never saw
  a date to prune on.
- **And folding came too late anyway.** Simplification runs last, after predicate
  pushdown has asked the scan what it can prune. The optimizer's own comment named
  the fix: register simplification a second time, at the front.

`TestADateLiteralPrunesRowGroups` and `TestATemporalLiteralOfAnotherUnitIsNotPruned`
(root) write 500 days from 1996-01-01 in ten row groups of 50, as a Date and as a
Datetime in milliseconds. **Before, a filter from the last group's first day read all
ten groups,** and Explain showed the cast.

### What changed

- **`litFromColumn`** (`internal/physical/fold.go`) folds:
  - a Date to its ticks as an Int32;
  - a Datetime, Duration or Time to its ticks as an Int64;

  each with its full dtype. `litColumn` builds that back exactly: the `time.Time`
  path, which converts, is never taken.
- **`Lit.String`** (`internal/expr/nodes.go`) renders a temporal literal's ticks with
  `dtype.FormatTemporal`, so Explain reads `lit(1997-03-26)`, not a count of days.
- **Simplification is registered first as well as last** (`internal/plan/optimize.go`),
  so a predicate reaches a scan already folded.
- **`prunable`** (`internal/source/parquet/prune.go`) gains `sameTicks`: a temporal
  literal is compared with a column's statistics only when its dtype is the column's,
  unit and zone included.

  A literal in microseconds against a column in milliseconds holds a thousand times
  the column's ticks. The evaluator casts one side to the other; the pruner compares
  raw numbers, and would skip groups that match.

**After:**

- the date filter reads 1 group of 10;
- a literal in the column's own unit reads 1 of 10;
- a literal in another unit reads all 10, and still answers right.

**One test changed with the behaviour.** `TestLiteralRenderingsCollide` listed a
Duration and an Int64 that both rendered `lit(5)`. The Duration now renders
`lit(5ns)`, so the pair was removed, with a note saying why. The identity key still
holds the type.

**PDS-H gains little from the pruning:** its dates are not ordered within its files.
It is for files written in time order, such as logs and events, where a date range
reads only its row groups.

## 3. Teeth

| tooth | result |
| --- | --- |
| each conjunct gathers every column again | **bites:** the filter allocation test |
| a selection not composed with the one before | **bites:** the filter test, and eight root tests, the optimizer differentials among them |
| the next conjunct evaluated over the whole input | **bites:** the same, five |
| temporal values not folded | **bites:** the date pruning test |
| constants folded only after the pushdowns | **bites:** both temporal tests |
| the pruner compares ticks of any unit | **bites:** the other-unit test |
| a folded date renders as its tick count | **bites:** the date pruning test's Explain check |

## 4. Measured

The four filter-heavy queries at SF=1, one at a time, three iterations, under an
8 GB scope. Beside them, step 141's.

| query | step 141, median | now, median | change |
| --- | --: | --: | --: |
| q19 | 362 ms | 329 ms | −9% |
| q12 | 356 ms | 331 ms | −7% |
| q7 | 450 ms | 425 ms | −6% |
| q15 | 253 ms | 237 ms | −6% |

Step 141's iterations ran under the CPU profiler, which costs a little, so the gain
is somewhat smaller than this. It is about what the copying cost: a share of filters'
17 to 24%.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's. It was run because
simplification now runs before the pushdowns, which touches every plan.
