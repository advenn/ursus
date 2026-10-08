# Step 133 — as built

**The key table's arena is chunks that never move, and its per-key arrays
double.** Step 132's profile put half of what h2o `gb10` allocates in the key table's
arrays, grown by `append`. No benchmark was run, by request: the change is checked by
a test of what the table allocates, and by the memory tests.

**The gate also found a panic as old as the repository,** in a spilled full or right
join, which the new table's sizes made a test reach (§4). It is fixed, with a test
that reaches it under the old table too.

## 1. Evidence first

**`TestKeyTableAllocatesAboutWhatItHolds`** (`internal/kernel`) inserts 1,048,576
distinct keys of about 24 bytes. It reads the bytes allocated, from
`/gc/heap/allocs:bytes`, against `NBytes`.

**On the code before this step:** 237.1 MB allocated to hold 57.3 MB, **4.14×**. The
mechanism is step 132's: past 256 elements `append` adds about a quarter at a time,
so the allocations on the way to a final size sum to about five times it.

## 2. What changed, in `internal/kernel/keytable.go`

**The arena is `chunks [][]byte`:**

- The first chunk is 64 bytes, and each new one doubles, up to 1 MiB.
- A chunk is filled to its capacity and never grown, so a key's bytes never move.
- A key that does not fit in the rest of the current chunk starts a new one. A key
  longer than the next chunk gets a chunk of exactly its length, and the doubling
  carries on.

**`offs []int64` became `refs []uint64`:**

- `refs[i+1]` is where key `i` ends: its chunk above bit 40, its offset in the chunk
  below.
- Key `i` starts where key `i-1` ended if that was in the same chunk, and at the
  chunk's start if not. Keys are stored in id order, so that is all a read needs.
- Still 8 bytes a key, and still 64-bit, for audit S24's 2 GiB of keys.

**`hashes` and `refs` grow by doubling,** with `pushDoubling`. They are indexed by id
on every probe, so they stay flat arrays, and doubling allocates about 2F where
`append` allocated about 5F. `hashes` starts with room for 8.

**Small to start, because of tiny limits.** The first version began with a 1 KiB
chunk and room for 64 hashes:

- a table holding one key then counted 2.5 KiB, where it had counted 1;
- the spilling group-by tests run under 2 KiB limits, where each sub-sink holds a
  key or a few;
- two of them partitioned to `maxSpillDepth` and reported "a single group is too
  large".

With 64 bytes and 8, one key counts 1.2 KiB, and they pass.

**Smaller changes:**

- `NBytes` counts the chunks' capacities, tracked as they are made, and their slice
  headers.
- `KeyAt`'s doc: a later insert no longer moves a key, since chunks never move. No
  caller relied on the old warning; every one copies.

## 3. Measured

| reading | before | after |
| --- | --: | --: |
| 1,048,576 keys: allocated / held | 237.1 / 57.3 MB, 4.14× | 84.3 / 52.1 MB, 1.62× |
| six-key group-by, 2,097,152 groups, sampled live, counted | 491.6 MB, 375.4 MB | 367 to 408 MB over seven runs, 362.1 MB |
| the same, assembling its answer, exact | 172.4 MB | 172.4 MB |
| one-key group-by, sampled, counted | 145 MB, 122 MB (step 129) | 137.9 MB, 117.2 MB |

**Step 131's open question is answered, as step 132 guessed.** The six-key group-by
read 1.31 of what it counted while it aggregated. It now reads 1.02 to 1.13. Growing
by `append` held the old arena beside the new one while it copied, and only the new
one was counted.

`TestAGroupByHoldsItsAnswerOnce`'s sampled bound goes back from 1.5 to 1.3, the
other memory tests' bound.

**What the table counts fell too:** 375.4 to 362.1 MB. An `append`-grown arena's
capacity overshoots its length by up to a quarter; a chunked one by at most part of
the last chunk.

## 4. A bug the gate found: a spilled outer join's last flush

**`TestMemoryLimitIsNotASemanticKnob/join_full`, at 16 KiB, panicked:** a nil
`*data.Batch` in `gatherOut`.

A full or right join emits the build rows no probe row matched, in a flush after the
probe. In `joinProbeOp.flushStep`, when the flush reached its last build row and the
join had also spilled:

1. it released the table, to give the replay of the spilled buckets the whole budget;
2. it then emitted its last rows from it. `releaseTable` sets `p.t` to an empty
   table, so the build side it read was nil.

**It has been there since the first commit.** It needs a level that both kept build
rows resident and spilled others, with rows left in the flush's last batch. When that
happens depends on what everything in the budget weighs, and the new table's sizes
moved it to a limit the test runs at.

**Under the old table it is reachable too.** A sweep of full joins found it with 500
build keys at limits of 2,560 and 3,200 bytes.

**The fix:** `flushStep` emits first, then releases.

**`TestASpilledOuterJoinEmitsItsLastFlush`** (root) sweeps:

- full and right joins;
- two build sides, 500 and 3,000 keys, 20 of them matched;
- every limit from 2 KiB to 512 KiB, in steps of a quarter.

A limit too small for one key's rows may refuse, as documented; none may panic or
answer differently. It requires at least 20 limits answered and something spilled,
or it is not reaching the flush.

| version | result |
| --- | --- |
| the old flush, the new table | panics at 4,000 bytes, 500 keys |
| the old flush, the old table | panics at 2,560 bytes, 500 keys |
| the fix, the new table | passes, in 1.9 s |

**Comparing the answers is what cost time.** As a multiset with
`ursustest.AssertFrameEqual`, the sweep took 48 s: that function takes about 170 ms
over these 18,000 rows, ordered or not, and the sweep compares about seventy times.
Each answer is now sorted with no limit, and its three Int64 columns compared value
by value.

## 5. Not measured

- **Speed.** A key read now picks its chunk, one more load and a branch, and an
  insert calls two small functions where it appended inline. No benchmark was run.
- **`gb10` at ten million rows.** At the kernel test's ratio, the key table's 11.5 GB
  of the profile's 22.9 would be about 4.5 GB, and the total about 16 GB. That is
  an estimate.

**Left as they were:** the slot ring, which always doubled; the accumulators'
`Reserve`; the parallel fold; the CSV reader's builders. Step 132 §4 has them in
order.

## 6. Teeth

| tooth | result |
| --- | --- |
| the arena one slice, grown by `append` | **bites:** the allocation test and the every-length test, whose `NBytes` falls short |
| `hashes` and `refs` grown by `append` | **bites:** the allocation test |
| a key read from the previous end across a change of chunk | **bites:** `TestGetOrInsertManyIsGetOrInsertInOrder` |
| a key longer than a chunk not given its own | **bites:** `TestKeyTableKeysOfEveryLength`, whose `NBytes` falls short of its keys |
| `NBytes` leaves out the chunks | **bites:** both new tests |
| the flush releases the table before it emits | **bites:** the sweep, under either table |

`TestKeyTableKeysOfEveryLength` interleaves empty keys, a key of exactly 1 MiB, and
keys up to 2 MiB, eight times over. Every id and key reads back, partway and at the
end, and `NBytes` covers every key byte.

**Gate:** the first run failed: two spilling group-by tests and the full join of §4,
in the root package, which had not been run before the gate. After §2's smaller
first sizes and §4's fix: test-all 115 ok, race 23 ok, levels, vet ×3 and the
bench engine tests clean. The figures in §3 were re-read with the final sizes and
are unchanged. No PDS-H run, by request.

## 7. Measured afterwards, at the maintainer's request

`gb10` and `j5` at ten million rows, ursus alone, one timed run each, under a 3 GB
scope, at `c5c9e24`. Beside each, step 131's run of the same, at `79636f6`. CPU is
the scope's, from the journal.

| query | time | CPU | VmHWM | heap in use | allocated | collections |
| --- | --: | --: | --: | --: | --: | --: |
| `gb10`, CSV | 6.4 s (8.8) | 20.0 s (24.7) | 2.85 GB (3.05) | 2.85 GB (3.02) | 18.5 GB (28.0) | 40 (65) |
| `j5`, CSV | 9.6 s (10.3) | 49.6 s (52.3) | 2.87 GB (2.91) | 2.80 GB (2.83) | 17.1 GB (18.7) | 39 (44) |
| `gb10`, Parquet | 3.8 s (4.7) | 16.0 s (22.1) | 2.88 GB (3.20) | 2.80 GB (3.19) | 17.4 GB (25.6) | 35 (46) |
| `j5`, Parquet | 4.9 s (5.6) | 42.4 s (42.5) | 2.90 GB (2.91) | 2.81 GB (2.82) | 17.4 GB (18.5) | 32 (34) |

**`gb10` allocates a third less, and is off the cap.**

- It allocates 18.5 and 17.4 GB, from 28.0 and 25.6: a third less, about what §5
  estimated for the key table.
- Over Parquet, its VmHWM is 2.88 GB, under the soft limit's 2.9; it was 3.20, the
  whole 3 GiB.
- It uses 19% less CPU over CSV and 28% less over Parquet. CPU time does not depend
  on what else the machine runs, so that is the change and not the machine.
- Its wall time is 27% and 20% shorter, from one iteration each.

**`j5` barely moved:** 9% less allocated over CSV and 6% over Parquet, and the same
CPU within 5%. Its key table is a fraction of what it allocates.

**`j5` over CSV is still slower than step 128's run,** 9.6 s against 8.4, on 4% more
CPU. It is one iteration, on a laptop, and was not looked into.
