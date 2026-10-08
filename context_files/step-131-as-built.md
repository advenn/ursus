# Step 131 — as built

**A group-by with as many groups as rows no longer holds its keys twice while it
assembles its answer.** It also fixes a regression step 130 introduced: under the
default budget, `Collect` held its result twice while it concatenated it. No
benchmark was run, by request.

## 1. Evidence first

**`TestAGroupByHoldsItsAnswerOnce`** (internal/memcheck) has h2o `gb10`'s shape:

- 2,097,152 rows, one group per row;
- three String keys and three Int64;
- a sum and a count;
- read from a copying filter, and streamed.

**On the code before this step,** it failed: **517.2 MB to assemble a 159.4 MB
answer,** against the 375.4 MB the group-by counted.

## 2. What held it, found in two steps

1. **The obvious fix moved it only part way.**
   - The key table goes before the answer is built. It holds every key, encoded, and
     the answer needs no lookup.
   - The key parts are concatenated with `ConcatOwned`, which lets each column's
     pieces go once copied.

   **That took it to 301 MB, still 1.9× the answer.**

2. **The ledger keeps alive what it counts.** A `data.BufferID` is a pointer to an
   allocation's first byte, and the ledger is keyed by it.
   - The group-by's account listed every key part until `releaseState`, which ran
     after the answer was built.
   - So whatever `ConcatOwned` let go of, the account kept.

   Releasing the account first took it to **172.4 MB, 1.08× the answer.**

**The same was true of step 130's `Collect`.** Its result account was released by a
`defer`, after `ConcatOwned`, so under the default budget every batch stayed alive
until the result was whole.

**The memory test missed it:** step 129's `Collect` cases set a limit of their own,
and under one there is no result account. Step 130 added that account and was
measured only under a limit.

**A join's freeze already released first** (`joinBuildSink.freeze`), which is why
step 129's tooth on it bit.

## 3. What changed

**`hashAggSink.residentResult`:**

- it takes the key parts and the accumulators, then calls `releaseState` first. That
  drops the key table, the sink's references and the account's claim;
- it concatenates the keys with `kernel.ConcatOwned`;
- it drops each accumulator once its column is finished.

**Its callers:**

- `Finish` no longer calls `releaseState` after it.
- `collectOrdered` closes a sub-sink's partition files before `residentResult`, as
  `Finish` already did. `closeParts` uncharges their buffers, and after a release
  that would drive the total below zero.

**`exec.Collect`** releases the result's account once the stream ends, before it
concatenates.

**`internal/execopt`'s package doc** has a section, "An account keeps alive what it
holds": an operator that drops a buffer to free it must release it first.

**The other releases** in `internal/physical` were read for the same gap. Each one
either comes when its operator drops the data, or comes later because the operator
still reads the data until then: a sort's in-memory remainder, the temporal
group-by's input. None was changed.

## 4. Measured, in-process

| reading, 2,097,152 rows | before | after |
| --- | --: | --: |
| the six-key group-by assembling its 159.4 MB answer, exact | 517.2 MB | 172.4 MB |
| the same group-by, sampled | 517.2 MB | 491.6 MB |
| `Collect` of an 84 MB filter result, default budget, exact | fails its bound with step 130's order | 96.7 MB |

**The sampled reading's largest is now while the group-by aggregates:** 1.31 of what
it counts, against a one-key group-by's 1.17. This step did not change that phase,
and it is not explained. The test bounds it at 1.5, for a second copy of something.

**What this means for `gb10` at ten million rows under 3 GB is not measured.** Its
answer phase held the group-by's state, up to the budget, and a second copy of its
keys, about 0.75 GB. It now holds about the answer and one column. One run would
show it.

## 5. Not done

- **The parallel fold.** `hashAggSink.Merge` still concatenates a worker's keys with
  `Concat` and then takes the new ones. That holds the worker's parts, the
  concatenation and the taken keys at once, one worker at a time. At 8 workers each
  holds about an eighth of the keys. Not measured.
- **The aggregation phase's 1.31,** above.

## 6. Teeth

| tooth | result |
| --- | --- |
| `Collect` releases its account after concatenating, step 130's order | **bites:** the default-budget `Collect` case |
| the group-by concatenates its keys with `Concat` | **bites** |
| the group-by releases its account after its answer | **bites** |
| the key table kept | **bites** |
| each accumulator kept until the answer is built | **silent** |

The last is silent by design. Accumulators finish after the last key column, where
no exact reading lands, and a sum and a count are a fifth of this answer. It matters
for a median or an implode, which hold every value.

**Gate:** test-all 115 ok, race 23 ok (memcheck 62 s), levels, vet ×3 and the
bench engine tests clean. No PDS-H run, by request.

## 7. Measured afterwards, at the maintainer's request

`gb10` and `j5` at ten million rows, ursus alone, one timed run each, under a 3 GB
scope, at `79636f6`. Beside them, the 3 GB run of step 128, before steps 130 and
131.

| query | time | VmHWM | CPU | step 128's run |
| --- | --: | --: | --: | --- |
| `gb10`, CSV | 8.8 s | 3.05 GB | 24.7 s | 7.4 s, 3.05 GB, 23.5 s |
| `j5`, CSV | 10.3 s | 2.91 GB | 52.3 s | 8.4 s, 2.92 GB, 47.7 s |
| `gb10`, Parquet | 4.7 s | 3.20 GB | 22.1 s | 4.8 s, 3.22 GB, 21.8 s |
| `j5`, Parquet | 5.6 s | 2.91 GB | 42.5 s | 4.4 s, 2.91 GB, 42.1 s |

CPU is the scope's, from the journal.

**Nothing was killed, and VmHWM did not move.** It cannot show this step's change.
The runner's own figures for this run:

| query | the budget counted | heap in use, sampled peak | allocated in all | collections |
| --- | --: | --: | --: | --: |
| `gb10`, CSV | 1.72 GB | 3.02 GB | 28.0 GB | 65 |
| `j5`, CSV | 1.13 GB | 2.83 GB | 18.7 GB | 44 |
| `gb10`, Parquet | 1.54 GB | 3.19 GB | 25.6 GB | 46 |
| `j5`, Parquet | 0.95 GB | 2.82 GB | 18.5 GB | 34 |

- **The heap in use is about twice what the budget counts,** and it is past the soft
  limit, nine tenths of 3 GiB or 2.9 GB, for `gb10`.
- **Heap in use counts garbage not yet collected.** `gb10` over Parquet allocated
  25.6 GB in 4.7 s, about 5 GB a second. A collector held to a share of the CPU
  cannot keep pace with that under the soft limit.
- **So the heap fills to the cap whatever the live peak is,** and a lower live peak
  shows only as headroom, which VmHWM does not report. The runs before this one kept
  no heap figures, since each run's raw result replaces the last; whether the live
  peak fell at this size is not known.

**Times:**

- Three of four were 19–26% slower than step 128's run.
- Their CPU time was within 5%, except `j5` over CSV at 10%.
- `j5` over Parquet took the same CPU over a quarter more wall time.

More wall time on the same CPU points at the machine, not at more work. Each is one
iteration, and nothing here separates the two.

**What it points to:** at ten million rows the allocation rate, not what any one
operator holds, is what puts the heap at the cap. An allocation profile of `gb10`
would say where 25 GB goes.
