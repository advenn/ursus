# Step 235 — as built

**W1 of `v0.6.4-scope.md`: a window in an order of its own streams over one huge
partition,** with no partition_by or over one hot key, where it refused.

## 1. Why

The one-partition file (step 197) and the hot key (step 209) stream a window in the
file's order, the input's. A function under an order_by walks the partition in
another order, so `streamNeed` refused it. Its answer still needs only a bounded
context, but in that order.

## 2. What changed

**`internal/physical/winsorted.go`, the sorted pass.** For each distinct order among
the specs:

1. **`project`** reads the one-partition file, stamps each row's position (`__pos`,
   dense from 0, the same on every read), and keeps only what the functions read:
   - the order keys, as `__k{i}`;
   - each function's child and fill, cast as `finishOrdered` casts them, as `__c{j}`
     and `__f{j}`.
2. **A `sortSink` built by hand** sorts that by the order keys, then the position.
   The position makes the order total, equal to the in-memory stable sort whatever
   order the runs merge in.
3. **A `winStreamOp` walks the sorted stream** with the specs rewritten to read the
   projection's columns with no order, so history, lookahead, the carried answer,
   `EwmNext` and the count are reused unchanged.
4. **A second `sortSink`** puts `(__pos, answers)` back in position order, and
   `writeAnswers` drains it into an answer file in the window's spill directory.

Only the narrow projection is sorted, never the rows.

**`winStreamOp`:**

- it reads any run source (`open`), the file or a sorted stream;
- it splices in the answer files beside its rows: `answerReader.take` gives the next
  n rows' answers, checking by their positions that they are those rows';
- `finishOneSpill(ctx)` groups the specs by `orderKey`, the keys with their direction
  and null place, runs a pass for each, and marks those specs to read their answers.

**Elsewhere:**

- **`sortedNeed`** is a spec's need over its own order; `streamsAlone` is either
  that or `streamNeed`. `streamable` and `skewable` use it, so the hot key inherits
  the pass: its answer is still the window's schema in the file's order, merged on
  the ordinal as before.
- **The refusal hints** (`extwindow.go`) and the comments no longer list ordered
  windows.

Rank, the reversed functions, interpolation and rolling by time still refuse; the
next steps take them up.

## 3. Tests and teeth

**`TestAWindowInAnOrderOfItsOwnStreamsPastItsLimit`:**

- 20,000 rows in forty frames, with no partition_by and with a key holding most of
  them.
- **Four orders:**
  - a float with nulls, NaN, −0 and +0 among six values, descending with nulls last,
    then an integer;
  - the same ascending;
  - the same descending with nulls first;
  - `x mod 7`, an expression whose seven values tie across every batch.
- **The functions:** cumulative sums, a shift, cum_count either way, a forward fill
  over long runs of nulls, a rolling mean, a cumulative max of strings, an ewm, and a
  shift forward with a fill. Beside them, a function in the file's order and an
  aggregate.
- **The checks:** under a tenth of the input, on one thread and four, frame-equal to
  the in-memory answer, spilling, and the spill directory empty after.

| tooth | result |
| --- | --- |
| an ordered window never sorted | **bites:** it refuses |
| descending dropped | **bites** |
| nulls last dropped | **bites:** the key's nulls, added for it |
| two directions share a pass | **bites:** the order differing only in direction, added for it |
| the walk's lookahead not set | **bites:** the shift forward |
| the answers not sorted back | **bites** |
| the position stamped from 0 each batch | **bites** |
| a sorted spec read from the wrong column | **bites** |
| the position left out of the order | silent, and equivalent today: the sort is stable and fed in position order. W5's merge cascade will need the key |
| the zip's guard gone | silent, and equivalent: it catches a misaligned answer file, which nothing produces |

The first form of "the answers not sorted back" kept the call in an `if` statement's
init clause, which runs, so it changed nothing. It was rewritten to skip the call.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
