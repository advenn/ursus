# Step 214 — as built

**S1 of `v0.6.3-scope.md`: the Parquet reader reads ahead, bounded in bytes.**

## 1. Why

Step 213's traces of gb4, q1 and q6 on 0.6.2's code had only 4 to 5.6 of 8 cores
busy. Four to five row-group workers sat parked at their send, and the reader waited
on the head row group about 60% of the run.

A row group's output held two batches. One not at the head stopped three batches in,
and decoded the rest only once it was the head, one row group at a time.

## 2. What changed

- **`ScanSpec.ReadAhead`** (`internal/source/source.go`): the bytes of decoded
  batches a source may hold ahead of its consumer. `planScan` sets it from the budget,
  to a thirty-second of the limit with at most 256 MiB, so four scans at once hold an
  eighth. It is not charged to the ledger, which would make sinks spill for bytes
  about to be consumed.
- **Each row group's channel holds all its batches,** capped at 1,024 so a corrupt
  row count makes no huge channel.
- **An `aheadGate`** bounds what the workers hold.
  - A worker asks it before each batch. It waits only while its row group holds three
    batches or more (`aheadFloor`, the depth before this step) and the workers hold
    the read-ahead.
  - `Next` gives back each batch it takes. A row group the reader waits on holds none,
    so it can always decode its next; the head never deadlocks.
  - `Close` wakes the gate before it waits for the workers, which cannot see `stop`
    while they wait there.
- **Order is unchanged:** batches still come in file order, through each row group's
  channel in turn.
- **`TestReadingAllocatesAboutWhatItProduces`** reads under a read-ahead of a byte.
  With the default, four workers decoded most of its file before the count began, and
  the four-thread case would have measured nothing.

## 3. Tests and teeth

- **`TestARowGroupDecodesAheadOfTheReader`:** four row groups of sixteen batches. With
  the reader holding still after its first batch, every row group decodes to its end:
  63 batches ahead.
- **`TestATinyReadAheadHoldsTheFloor`:** under a read-ahead of a byte, the workers hold
  between one and three batches each, however long the reader waits.
- **`TestReadAheadKeepsTheOrderAndItsBound`:** under a byte, 32 KiB and the default,
  eight workers and a reader that stops now and then give every row in order, under a
  20-second guard, holding at most the read-ahead plus eight times three batches.
- **`TestClosingWakesWorkersAtTheGate`:** Close returns while workers wait at the
  gate, and leaves no goroutine behind.

| tooth | result |
| --- | --- |
| the gate never waits | **bites:** the floor and bound tests |
| no floor, the gate alone | **bites:** the order test, which deadlocks under a byte |
| what the reader takes never given back | **bites:** a deadlock, caught by a 60-second test timeout |
| Close does not wake the gate | **bites:** the close test |
| a channel of two batches again | **bites:** the read-ahead test |
| the bytes sent not counted | **bites:** the floor test, added for it |
| a worker that never asks the gate | **bites** |

## 4. Measured

0.6.2's runner (`runner-s211`, `2cc9012`) against this step's, alternated three
rounds, three iterations:

| query | CPU | wall |
| --- | --: | --: |
| h2o gb1, Parquet | 667 → 696 ms, +4% | 148 → 107 ms, **−28%** |
| h2o gb4, Parquet | 1,059 → 1,186 ms, +12% | 213 → 199 ms, −7% |
| PDS-H q1, SF=1 | 2,325 → 2,388 ms, +3% | 402 → 371 ms, −8% |
| PDS-H q6, SF=1 | 547 → 605 ms, +11% | 115 → 103 ms, −10% |

**Peak RSS,** two runs each: gb4 137 → 225 MB, q1 162 → 229 MB, within the scope's
+100 MB.

**What is left, traced on gb4:**

- The reader no longer blocks: 0.07 s across two runs, from 2.44 s. ZSTD waits are
  0.04 s.
- Five of eight logical CPUs are busy, and about three goroutines at a time wait,
  runnable, for one.
- The laptop has **four physical cores**, two threads each, so gb4 is now bound by
  the CPU. The extra CPU is the price of running more threads at once on four cores.

So:

- **The scope's 35% for gb4 does not fit this machine.** Wall time now comes from
  removing work again.
- **S2, the ZSTD pool,** waits for a trace that shows ZSTD waiting.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.

The first race run failed to link, "no space left on device". `/tmp` is a 7.7 GB
tmpfs, and the session's scratch files had filled it: old benchmark answers and
profiles, 7.2 GB, held in RAM. With the maintainer's agreement the bulky ones were
removed, and the race run passed.
