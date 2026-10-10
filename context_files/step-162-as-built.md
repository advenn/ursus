# Step 162 — as built

**Item 3 of `v0.6-scope.md`, its second half: join sides chosen by what survives.**

## 1. Evidence first

**q12's plan.** `orders` is joined to `lineitem`, and lineitem's four filters are
pushed onto its scan. `EstimateRows` is an upper bound through every filter:

- lineitem is estimated at six million rows, more than twice orders' 1.5 million,
  so `build_side` exchanges the inputs;
- the join builds orders' 1.5 million keys, and probes them with the 31 thousand
  lineitem rows the filters leave.

**A q12 profile** on step 161's build put the build at 27% of the query:
`buildPartitioned` was 0.69 s of 2.51 s of samples. `memmove`, at 11.5%, was mostly
the retained orders batches concatenated for the build.

**The scope offered two fixes:**

- **a selectivity factor:** a guess, which on a wrong guess builds the larger side;
- **a check of the actual sizes:** this step takes it.

## 2. What changed

**The planner marks every join it could exchange** (`plan.Join.SwapAtRuntime`),
whether or not its estimates exchanged it:

- the conditions are the swap's own: an inner join, nothing above it depending on its
  order, no sorted left input, and `SwapJoin` exact;
- when it does exchange a join, it marks the inner join of the result too, since
  exchanging that again is the join as written;
- `Explain` shows the mark as `swap_at_runtime`, so two golden plans gained it.

**The physical join exchanges a marked join by its actual rows** (`joinswap.go`).
When the build side ends, a deferred build has inserted no key: it holds the build
batches and knows their rows. A marked, deferred join then reads its probe side, up
to half as many rows:

- **the probe side ends first:** the join runs exchanged. `plan.SwapJoin` rewrites it
  over in-memory scans of the two sides' batches, and it is planned as any join is.
  It builds partitioned from the small side and probes with the batches the build
  side already holds.
- **it does not:** the batches read go first (`concatOp`), and the join runs as
  planned, in the same order.

**Reading ahead is bounded:** the batches read are charged, and none is read past
half the budget, where a deferred build stops waiting too.

**`plan.SwapJoin`** exports the rule's rewrite. `planJoin`'s doc no longer says the
physical join makes no size decision.

## 3. Measured

A diagnostic build that logged each exchange, over all 22 queries at SF=0.1,
exchanged joins in seven of them:

| query | build rows | probe rows |
| --- | --: | --: |
| q8 | 15,000 and 45,624 | 1,429 and 4,485 |
| q9 | 150,000 and 80,000 | 32,160 each |
| q10 | 15,000 | 5,677 |
| q12 | 150,000 | 3,155 |
| q14 | 20,000 | 7,630 |
| q18 | 15,000 | 5 |
| q21 | 72,884 | 20,145 |

Step 161's runner against this one, alternated, three rounds of five iterations at
SF=1. CPU time per iteration is from rusage, warm-up included:

| query | CPU per iteration | median wall |
| --- | --- | --- |
| q12 | 883 → 674 ms (−24%) | 259 → 214 ms (−17%) |
| q9 | 2,485 → 2,320 ms (−7%) | 421 → 439 ms |
| q14 | 930 → 876 ms (−6%) | 168 → 166 ms |
| q18 | 1,809 → 1,750 ms (−3%) | 370 → 336 ms (−9%) |
| q8 | 1,432 → 1,499 ms | 282 → 278 ms |
| q10 | 1,365 → 1,367 ms | 308 → 299 ms |
| q21 | 5,472 → 5,397 ms | 989 → 983 ms |

q8's runs spread from 1,101 to 1,633 ms on both runners. None of the seven is slower
beyond that spread.

## 4. Tests

- **`TestASmallProbeSideIsBuiltInstead`:** an inner join whose keys and other column
  collide by name, so the exchanged join's Project has a merged key and a suffixed
  column to put back. Three cases:
  - a probe side under half the build side's rows exchanges, and answers as the join
    run serially, as a set;
  - one just over half reads ahead, puts the batches back and does not exchange;
  - a larger one does not exchange either.

  Both cases that do not exchange give the serial join's rows in its order.
- **`TestAnUnmarkedJoinIsNotExchanged`.**
- **`TestReadingAheadStopsAtHalfTheBudget`:** the build drained by hand, the budget then
  taken past half by another account. Nothing is read ahead and nothing exchanged, and
  the join's rows come in its order.
- **`TestBuildSideMarksWhatItCouldSwap`:**
  - marked: an inner join exchanged, and one kept;
  - not marked: a left join, a semi join, `JoinMaintainOrder`, a validated join, a
    sorted left input, a float key.
- **`TestARuntimeSwapKeepsTheAnswer`:** q12's shape through the public API, which the
  rule exchanges and the runtime exchanges back. A logging build confirmed the
  exchange, on eight threads. The answer matches the unswapped one on one thread and
  on eight.

## 5. Teeth

| tooth | result |
| --- | --- |
| a probe side read to its end, however large | **bites:** `ExampleLazyFrame_Join`, `TestJoinOptimizerSoundness` and more, whose row order changed |
| an unmarked join exchanged | **bites:** `TestAnUnmarkedJoinIsNotExchanged`, `TestBuildSideKeepsWhatTheCallerOrdered`, `TestSpilledJoinValidateStillFires` and more |
| read ahead past half the budget | **bites:** `TestReadingAheadStopsAtHalfTheBudget` |
| the batches read ahead dropped | **bites:** `TestBuildSideKeepsTheAnswer`'s five cases, `ExampleLazyFrame_Join` |
| the batches read ahead put last | **bites:** `TestASmallProbeSideIsBuiltInstead`'s two cases that do not exchange |
| no join marked | **bites:** `TestBuildSideMarksWhatItCouldSwap`, `TestARuntimeSwapKeepsTheAnswer`, the two goldens |
| a swapped join's inner join not marked | **bites:** the same two tests |

Two teeth read silent at first because the test run's filter left their tests out.
Rerun with them, both bite.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
