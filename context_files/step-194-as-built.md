# Step 194 — as built

**O3 of `v0.6.1-scope.md`: forward and nearest `JoinAsOf` stream.**

## 1. Why

Since step 183 a backward as-of join has streamed both sides: the latest right row
at or before each left key is all it needs. Forward and nearest still buffered the
right side in `asOfBuildSink` and searched it, and their `Probe` copied it once more.
A forward join over a 300,000-row right side held about 7 MB twice, and under a
limit below two right sides it was refused.

## 2. What changed

**`asOfStreamOp` takes every strategy.** For forward and nearest:

- **`passTo`** moves the join up to the left key k:
  - every queued row before k leaves its by-key's queue for that by-key's latest;
  - the right side is read through k. A row before k becomes its by-key's latest,
    and a row at k is queued, since with exact matches it is a candidate both ways.
- **`forwardOf`** is the by-key's first queued row at or after k, or after it when
  exact matches are off. When none is queued, the right side is read on, every row
  read queued under its by-key, until one of this by-key arrives or the side ends.
- **`backOf`**, for nearest, is the last queued row at k, with exact matches, and the
  by-key's latest otherwise.
- **`preferBack`** chooses between them for nearest: the closer, a tie going back,
  and on a float key by the floats' own distance. It was factored out of the
  buffered join's `nearest`, which used the same rule.
- **`pend`** is every queued row in the order read, which is key order. So the rows
  the left side passes are always its front, and popping them costs one step a row.
- **`compact`** gathers the queued rows with the rest when the held batches reach
  eight, and remaps them.

**What it holds.** The rows read ahead:

- **where the by-keys interleave,** as series of several symbols do, the gap to a
  by-key's next row. Every strategy peaks at 956 KB over a 300,000-row right side,
  backward's figure;
- **a left by-key the right side lacks** reads it to its end and holds what the
  buffered join held, charged to the budget like the rest;
- **a null left by-key** matches nothing, and reads nothing ahead to look.

**The buffered join is removed:** its sink methods, `asOfProbeOp`, `bucket` and
`nearest`. `asOfBuildSink` stays as the join's configuration and rules: the keys,
sortedness, tolerance and the tie-break. It is no longer a `Sink`, so
`TestRefusingSinksRefuse` loses its row, and the `Merge` inventory's vacuity floor
falls from seven to six.

**What a user sees:**

- the budget's refusal hint no longer says `join_asof` "holds its whole right side,
  and a second copy";
- its list of operators that fail rather than spill drops `join_asof`, whose every
  strategy now streams, and `merge_sorted`, which has streamed since step 182;
- `LazyFrame`'s memory doc says the same.

## 3. Tests and teeth

- **`TestForwardAndNearestAsOfStreamingAgreeWithAScan`:**
  - **the sides:** random and sorted, with ties within and across them, by-keys with
    nulls, a by-key the right side never holds, and one it holds rarely;
  - **the cases:** eight, forward and nearest, with exact matches and without,
    tolerances, and with and without by-keys;
  - **the check:** at batch sizes 1, 4, 64 and 4,096, against each left row's match
    found by scanning.
- **`TestBackwardAsOfHoldsARowAByKey`** now runs every strategy. Each peaks under
  1 MiB.
- **`TestSpillingByHand`'s two as-of cases,** which pinned the buffered join, now
  pin the streaming one:
  - a forward join holds a whole right side for a left by-key it lacks;
  - under one right side, forward, nearest and backward answer, a null left by-key
    answers, and the by-key the right side lacks is refused.

| tooth | result |
| --- | --- |
| a forward candidate AT the key refused | **bites:** the scan test, the strategy and key-type tests |
| nearest's backward candidate blind to rows at the key | **bites:** the scan test, the float-key test |
| rows at the key passed into latest | **bites:** the scan test, the strategy tests |
| a nearest tie going forward | **bites:** the tie test, the scan test |
| queued rows not remapped by `compact` | **bites:** the scan test, the memory case |
| a row read before the key queued anyway | **bites:** the key-type and tie tests |
| forward's tolerance not applied | **bites:** the tolerance tests, the scan test |
| a null left by-key matched | **bites:** the memory case. Its answer is the same either way, since a null bucket never gets a right row, and only the reading ahead shows. It was silent until that case was added |
| forward and nearest buffered again | **bites:** the memory case |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
